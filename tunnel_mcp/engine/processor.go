package engine

import (
	"context"
	"net/http"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/engine/hook"
	omnicli "github.com/Shaik-Sirajuddin/memory/mcp/engine/impl/cli"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/agents"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/session"
	"gopkg.in/yaml.v3"
)

const sessionUsageThrottlePercent = 95.0

// request type shorthands used by pickNextMessages and buildMessage.
// store agent should expose RequestTypeQuery and RequestTypeInstant as named constants.
var (
	reqTypeExecute = message.RequestTypeExecute
	reqTypeQuery   = message.RequestType("query")
	reqTypeInstant = message.RequestType("instant")
)

// statusQueued is set on messages picked for execution.
// store agent should expose this as message.StatusQueued.
const statusQueued = message.Status("queued")

// ReplyService manages the full reply lifecycle for a delivered message.
// The concrete implementation lives in the server layer and is wired via SetReplyService.
type ReplyService interface {
	SendReply(ctx context.Context, msg *message.Message, fromAgentID, fromAgentName string) error
}

// AgentWorkspace exposes the engine's current workspace for a sender agent.
type AgentWorkspace interface {
	GetAgentWorkspace(ctx context.Context, agentID string) (string, bool)
}

// AgentCallbackRequest carries the details of a completed (or failed) agent execution.
type AgentCallbackRequest struct {
	Source    MessageRef `json:"source"`
	AgentID   string     `json:"agent_id"`
	Workspace string     `json:"agent_workspace"`
}

// MessageRef identifies the message that was being processed.
type MessageRef struct {
	MessageID string `json:"message_id"`
}

// ProcessingEngine is the central coordinator:
//   - receives message-arrived events from the MCP server / HTTP-over-unix server
//   - manages in-memory agent state (status, session usage, code session)
//   - throttles and preflight-checks before triggering `omni agent exec`
//   - handles agent callbacks (success → send next, failure → retry)
//   - runs a unix-socket SyncServer for live session-usage updates from omni-server
//   - registers hook routes on a caller-provided mux via RegisterHookRoutes
type ProcessingEngine struct {
	state          *EngineState
	msgStore       message.MessageStore
	agentStore     agents.AgentStore
	omni           OmniCLI
	mcp            *MCPClientRegistry
	syncServer     *SyncServer
	socketPath     string
	reply          ReplyService
	deliveryWindow time.Duration
	ctx            context.Context // engine lifetime context, set in Run
}

// Option configures a ProcessingEngine.
type Option func(*ProcessingEngine)

// WithTestBinary replaces the default omni CLI with a test double.
func WithTestBinary(cli OmniCLI) Option {
	return func(e *ProcessingEngine) { e.omni = cli }
}

// WithSocketPath overrides the default unix socket path for the sync server.
func WithSocketPath(path string) Option {
	return func(e *ProcessingEngine) { e.socketPath = path }
}

// WithReplyService wires a ReplyService for post-delivery reply routing.
func WithReplyService(r ReplyService) Option {
	return func(e *ProcessingEngine) { e.reply = r }
}

// SetReplyService wires a ReplyService after construction.
// Must be called before Run.
func (e *ProcessingEngine) SetReplyService(r ReplyService) {
	e.reply = r
}

// WithDeliveryWindow sets the timeout after which a queued message is marked failed.
func WithDeliveryWindow(d time.Duration) Option {
	return func(e *ProcessingEngine) { e.deliveryWindow = d }
}

// New creates a ProcessingEngine backed by msgStore.
func New(msgStore message.MessageStore, opts ...Option) *ProcessingEngine {
	agentStore, err := agents.GetStore()
	if err != nil {
		logger.Error("engine: failed to init agent store", "err", err)
	}
	e := &ProcessingEngine{
		state:          newEngineState(),
		msgStore:       msgStore,
		agentStore:     agentStore,
		omni:           omnicli.New("omni"),
		mcp:            newMCPClientRegistry(),
		socketPath:     DefaultSyncSocketPath,
		deliveryWindow: 10 * time.Second,
	}
	for _, opt := range opts {
		opt(e)
	}
	e.syncServer = newSyncServer(e.socketPath, e.state, e.onSessionSync)
	return e
}

// RegisterHookRoutes registers the engine's hook handler on the provided mux.
// The caller owns the unix socket server and transport — engine only handles routes.
func (e *ProcessingEngine) RegisterHookRoutes(mux *http.ServeMux) {
	mux.Handle("POST /hook", hook.New(e))
}

// Run starts the sync server and the startup delivery pass, then blocks until ctx is cancelled.
func (e *ProcessingEngine) Run(ctx context.Context) error {
	e.ctx = ctx
	logger.Info("processing engine starting", "socket_path", e.socketPath)

	go func() {
		if err := e.syncServer.Run(ctx); err != nil {
			logger.Error("sync server stopped", "err", err)
		}
	}()

	go e.runQueueSweep(ctx)

	e.hydrateState(ctx)

	for _, agentID := range e.state.PendingAgentIDs() {
		go e.executeLoop(agentID)
	}

	<-ctx.Done()
	logger.Info("processing engine stopping")
	return nil
}

// MessageArrived is called by the MCP server or HTTP server when a new message is stored for `to`.
func (e *ProcessingEngine) MessageArrived(ctx context.Context, from, to string) {
	logger.Debug("message arrived", "from", from, "to", to)
	e.state.SetPending(to, true)
	go e.executeLoop(to)
}

// AgentCallback is called after an agent completes (failed=false) or fails (failed=true).
func (e *ProcessingEngine) AgentCallback(ctx context.Context, req AgentCallbackRequest, failed bool) {
	logger.Debug("agent callback", "agent_id", req.AgentID, "message_id", req.Source.MessageID, "failed", failed)

	agentState, _ := e.state.GetAgent(req.AgentID)
	agentState.Status = AgentStatusReady
	agentState.Agent = Agent{
		AgentID:   req.AgentID,
		Name:      agentState.Agent.Name, // preserve omni name set by OnPreSessionStart
		Workspace: req.Workspace,
		Team:      agentState.Agent.Team,
	}
	e.state.SetAgent(req.AgentID, agentState)

	if failed {
		e.handleFailedExec(req)
		return
	}
	e.handlePostExec(req)
}

// Interrupt halts delivery to agentID until Resume is called.
func (e *ProcessingEngine) Interrupt(agentID string) {
	logger.Info("agent interrupted", "agent_id", agentID)
	agentState, _ := e.state.GetAgent(agentID)
	agentState.CodeSession.IsInterrupted = true
	agentState.Status = AgentStatusPaused
	e.state.SetAgent(agentID, agentState)
}

// Resume clears the interrupted flag and re-triggers the execute loop.
func (e *ProcessingEngine) Resume(ctx context.Context, agentID string) {
	logger.Info("agent resumed", "agent_id", agentID)
	agentState, _ := e.state.GetAgent(agentID)
	agentState.CodeSession.IsInterrupted = false
	agentState.Status = AgentStatusReady
	e.state.SetAgent(agentID, agentState)
	go e.executeLoop(agentID)
}

// MCPClients exposes the registry so the MCP server can register/unregister clients.
func (e *ProcessingEngine) MCPClients() *MCPClientRegistry {
	return e.mcp
}

func (e *ProcessingEngine) GetAgentWorkspace(_ context.Context, agentID string) (string, bool) {
	agentState, ok := e.state.GetAgent(agentID)
	if !ok || agentState.Agent.Workspace == "" {
		return "", false
	}
	return agentState.Agent.Workspace, true
}

// --- internal ---

var omniStatusToEngine = map[string]AgentStatus{
	"running": AgentStatusRunning,
	"stopped": AgentStatusStopped,
}

var omniStopReasonToEngine = map[string]StopReason{
	"tokens_exhausted": StopReasonTokensExhausted,
	"interrupted":      StopReasonInterrupted,
	"network":          StopReasonNetwork,
}

// hydrateState loads agent and session state from the DB on startup.
// Flow: GetPendingAgents → per agentID: GetWorkspaceForAgent + GetSession → SetAgent + SetPending.
func (e *ProcessingEngine) hydrateState(ctx context.Context) {
	agentIDs, err := e.msgStore.GetPendingAgents(ctx)
	if err != nil {
		logger.Error("hydrate state: get pending agents failed", "err", err)
		return
	}
	if len(agentIDs) == 0 {
		logger.Debug("hydrate state: no pending agents")
		return
	}

	logger.Info("hydrate state: loading agents", "count", len(agentIDs))

	for _, agentID := range agentIDs {
		workspace, err := e.msgStore.GetWorkspaceForAgent(ctx, agentID)
		if err != nil {
			logger.Warn("hydrate state: workspace not found", "agent_id", agentID, "err", err)
		}

		agentName := ""
		if e.agentStore != nil {
			if data, err := e.agentStore.GetAgent(agentID); err == nil && data.Info != nil {
				agentName = data.Info.Name
			} else if err != nil {
				logger.Warn("hydrate state: agent name lookup failed", "agent_id", agentID, "err", err)
			}
		}

		state := AgentState{
			Agent: Agent{
				AgentID:   agentID,
				Name:      agentName,
				Workspace: workspace,
			},
			Status: AgentStatusReady,
		}

		sess, err := session.GetSession(agentID)
		if err != nil {
			logger.Warn("hydrate state: no active session", "agent_id", agentID, "err", err)
		} else {
			if status, ok := omniStatusToEngine[sess.Status]; ok {
				state.Status = status
			}
			stopReason, ok := omniStopReasonToEngine[sess.StopReason]
			if !ok && sess.StopReason != "" {
				stopReason = StopReasonOther
			}
			state.StopReason = stopReason
			state.CodeSession = CodeSession{
				SessionID:     sess.Id,
				IsInterrupted: sess.IsInterrupted,
			}
			state.SessionUsage = SessionUsage{
				ConsumedPercent: sess.TokensConsumedPct,
				Max:             map[string]int64{"total": int64(sess.TokensMax)},
			}
		}

		e.state.SetAgent(agentID, state)
		e.state.SetPending(agentID, true)
		logger.Debug("hydrate state: agent loaded", "agent_id", agentID, "status", state.Status)
	}
}

// runQueueSweep ticks every deliveryWindow/2 and marks stale queued messages as failed.
// A message is stale when its queue_time is older than deliveryWindow, meaning ExecInSession
// never completed or the hook never fired.
func (e *ProcessingEngine) runQueueSweep(ctx context.Context) {
	interval := e.deliveryWindow / 2
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-e.deliveryWindow).UnixMilli()
			stale, err := e.msgStore.RawQuery(ctx,
				`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
				        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id
				 FROM messages WHERE status = ? AND queue_time > 0 AND queue_time < ?`,
				string(statusQueued), cutoff,
			)
			if err != nil {
				logger.Error("queue sweep: query failed", "err", err)
				continue
			}
			for _, msg := range stale {
				msg.Status = message.StatusFailed
				if err := e.msgStore.UpdateMessage(ctx, msg); err != nil {
					logger.Error("queue sweep: mark failed error", "message_id", msg.ID, "err", err)
				}
			}
			if len(stale) > 0 {
				logger.Warn("queue sweep: marked stale queued messages failed", "count", len(stale))
			}
		}
	}
}

// bootstrapAgent loads an unknown agent into EngineState from the omni agent store.
// Returns true if the agent is now known and ready to execute.
func (e *ProcessingEngine) bootstrapAgent(agentID string) bool {
	ctx := e.ctx
	if e.agentStore == nil {
		logger.Error("bootstrap agent: agent store not available", "agent_id", agentID)
		return false
	}
	data, err := e.agentStore.GetAgent(agentID)
	if err != nil || data.Info == nil {
		logger.Error("bootstrap agent: agent not found in omni store", "agent_id", agentID, "err", err)
		return false
	}
	workspace, err := e.msgStore.GetWorkspaceForAgent(ctx, agentID)
	if err != nil {
		logger.Warn("bootstrap agent: workspace not found, will use message workspace", "agent_id", agentID, "err", err)
	}
	e.state.SetAgent(agentID, AgentState{
		Agent: Agent{
			AgentID:   agentID,
			Name:      data.Info.Name,
			Workspace: workspace,
		},
		Status: AgentStatusReady,
	})
	e.state.SetPending(agentID, true)
	logger.Info("bootstrap agent: loaded into state", "agent_id", agentID, "name", data.Info.Name)
	return true
}

// executeLoop is the lifecycle-aware dispatch loop: checks interruption and running
// state, then pick → mark queued → build prompt → exec.
func (e *ProcessingEngine) executeLoop(agentID string) {
	if agentID == "" {
		logger.Error("execute loop: empty agent_id, skipping")
		return
	}
	ctx := e.ctx
	agentState, ok := e.state.GetAgent(agentID)

	if !ok || agentState.Agent.Name == "" {
		if !e.bootstrapAgent(agentID) {
			logger.Error("execute loop: agent unknown and bootstrap failed, cannot exec", "agent_id", agentID)
			return
		}
		agentState, _ = e.state.GetAgent(agentID)
	}

	if agentState.CodeSession.IsInterrupted {
		logger.Info("execute loop: agent interrupted, holding delivery", "agent_id", agentID)
		return
	}
	if agentState.Status == AgentStatusRunning {
		logger.Debug("execute loop: agent already running, skipping", "agent_id", agentID)
		return
	}

	// Throttle at 95% session usage.
	if agentState.SessionUsage.ConsumedPercent >= sessionUsageThrottlePercent {
		logger.Info("execute loop: throttled — session usage high",
			"agent_id", agentID,
			"consumed_percent", agentState.SessionUsage.ConsumedPercent,
		)
		return
	}

	// Guard: skip if a queued or processing delivery is already in flight for this agent.
	activeDelivery, err := e.msgStore.RawQuery(ctx,
		`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id
		 FROM messages WHERE "to" = ? AND status IN (?, ?) LIMIT 1`,
		agentID, string(statusQueued), string(message.StatusProcessing),
	)
	if err != nil {
		logger.Error("execute loop: active delivery check failed", "agent_id", agentID, "err", err)
		return
	}
	if len(activeDelivery) > 0 {
		logger.Debug("execute loop: active delivery in flight, skipping", "agent_id", agentID)
		return
	}

	// TODO: re-enable once omni agent prompt-state subcommand is stable.
	// Skipping prompt-state check for now — messages are sent directly.
	// promptState, err := e.omni.GetPromptState(ctx, agentID)
	// if err != nil {
	// 	logger.Error("execute loop: get prompt state failed", "agent_id", agentID, "err", err)
	// 	return
	// }
	// if promptState != "" {
	// 	logger.Debug("execute loop: agent has pending prompt, skipping", "agent_id", agentID)
	// 	return
	// }

	msgs, err := e.pickNextMessages(agentID)
	if err != nil {
		logger.Error("execute loop: pick messages failed", "agent_id", agentID, "err", err)
		return
	}
	if len(msgs) == 0 {
		logger.Debug("execute loop: no pending messages, clearing delivery flag", "agent_id", agentID)
		e.state.SetPending(agentID, false)
		return
	}

	// Populate workspace from message if not yet set on agent state.
	if agentState.Agent.Workspace == "" && msgs[0].Workspace != "" {
		agentState.Agent.Workspace = msgs[0].Workspace
		e.state.SetAgent(agentID, agentState)
	}

	queueTime := time.Now().UnixMilli()
	for _, msg := range msgs {
		msg.Status = statusQueued
		msg.Retries++
		msg.QueueTime = queueTime
		if err := e.msgStore.UpdateMessage(ctx, msg); err != nil {
			logger.Error("execute loop: update message to queued failed", "message_id", msg.ID, "err", err)
		}
	}

	prompt := buildMessage(msgs)

	agentState, _ = e.state.GetAgent(agentID)
	agentState.Status = AgentStatusRunning
	e.state.SetAgent(agentID, agentState)

	logger.Info("execute loop: executing agent",
		"agent_id", agentID,
		"message_count", len(msgs),
		"first_message_id", msgs[0].ID,
	)

	execErr := e.omni.ExecInSession(ctx, agentID, agentState.Agent.Name, agentState.Agent.Workspace, prompt)
	if execErr != nil {
		logger.Error("execute loop: exec failed", "agent_id", agentID, "err", execErr)
	}

	// Session-end guard: hooks (OnStop) fire during the session and should have already
	// cleaned up status and messages. If they didn't (crash / no PostPrompt), do it here.
	e.onSessionEnd(agentID, msgs, execErr != nil)

	// Reset queue_time so the cron sweep doesn't re-flag these messages.
	now := time.Now().UnixMilli()
	for _, msg := range msgs {
		msg.QueueTime = now
		if err := e.msgStore.UpdateMessage(ctx, msg); err != nil {
			logger.Error("execute loop: reset queue_time failed", "message_id", msg.ID, "err", err)
		}
	}
}

// onSessionEnd is called after ExecInSession returns to clean up state that hooks may have missed
// (e.g. if the omni process crashed before PostPrompt fired).
// msgs is used only for IDs — status is re-queried from DB to avoid stale overwrites.
func (e *ProcessingEngine) onSessionEnd(agentID string, msgs []*message.Message, execFailed bool) {
	ctx := e.ctx
	agentState, _ := e.state.GetAgent(agentID)

	// Always reset the query tool flag — it belongs to a single session.
	agentState.CodeSession.QueryToolInvoked = false

	if execFailed {
		agentState.Status = AgentStatusStopped
		agentState.StopReason = StopReasonOther
	} else if agentState.Status == AgentStatusRunning {
		// Status still Running means PostPrompt never fired — hooks missed.
		agentState.Status = AgentStatusReady
	}

	e.state.SetAgent(agentID, agentState)

	// Re-query current message state from DB — OnStop may have already updated them.
	// Only mark failed if they are still in queued or processing (not delivered/failed already).
	if len(msgs) == 0 {
		return
	}
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	for _, id := range ids {
		msg, err := e.msgStore.GetMessage(ctx, id)
		if err != nil {
			logger.Error("session end: get message failed", "message_id", id, "err", err)
			continue
		}
		if msg.Status == statusQueued || msg.Status == message.StatusProcessing {
			logger.Warn("session end: orphaned message, marking failed", "message_id", id, "status", msg.Status, "exec_failed", execFailed)
			msg.Status = message.StatusFailed
			if err := e.msgStore.UpdateMessage(ctx, msg); err != nil {
				logger.Error("session end: mark failed error", "message_id", id, "err", err)
			}
		}
	}
}

// pickNextMessages selects the next batch of messages for agentID.
// Groups by sender (msg.From): execute picks exactly 1; query/instant accumulate up to 5.
func (e *ProcessingEngine) pickNextMessages(agentID string) ([]*message.Message, error) {
	msgs, err := e.msgStore.RawQuery(e.ctx,
		`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id
		 FROM messages WHERE "to" = ? AND status = ? ORDER BY sent_time ASC LIMIT 10`,
		agentID, string(message.StatusInQueue),
	)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nil
	}

	first := msgs[0]
	if first.RequestType == reqTypeExecute {
		return msgs[:1], nil
	}

	// Accumulate query/instant from the same sender, up to 5, stop before execute.
	senderID := first.From
	var picked []*message.Message
	for _, msg := range msgs {
		if len(picked) >= 5 {
			break
		}
		if msg.From != senderID {
			break
		}
		if msg.RequestType == reqTypeExecute {
			break
		}
		picked = append(picked, msg)
	}
	return picked, nil
}

type promptItem struct {
	MessageID string `yaml:"message_id"`
	Refs      string `yaml:"refs"`
	Prompt    string `yaml:"prompt"`
}

type promptPayload struct {
	RequestType string       `yaml:"request_type"`
	Instruction string       `yaml:"instruction"`
	Messages    []promptItem `yaml:"messages"`
}

var requestTypeInstruction = map[message.RequestType]string{
	reqTypeExecute: "Execute the following task",
	reqTypeQuery:   "Answer the following queries. Use the tunnel_mcp query_result tool (or query_result_batch for multiple) to send your response back, passing the message_id and your answer as the response.",
	reqTypeInstant: "Process the following messages",
}

// buildMessage assembles a strict YAML prompt for ExecInSession.
func buildMessage(msgs []*message.Message) string {
	if len(msgs) == 0 {
		return ""
	}

	instruction, ok := requestTypeInstruction[msgs[0].RequestType]
	if !ok {
		instruction = "Process the following"
	}

	payload := promptPayload{
		RequestType: string(msgs[0].RequestType),
		Instruction: instruction,
		Messages:    make([]promptItem, len(msgs)),
	}
	for i, msg := range msgs {
		payload.Messages[i] = promptItem{
			MessageID: msg.ID,
			Refs:      msg.Refs,
			Prompt:    msg.Prompt,
		}
	}

	out, err := yaml.Marshal(payload)
	if err != nil {
		logger.Error("build message: yaml marshal failed", "err", err)
		return ""
	}
	return string(out)
}

func (e *ProcessingEngine) handlePostExec(req AgentCallbackRequest) {
	ctx := e.ctx
	msg, err := e.msgStore.GetMessage(ctx, req.Source.MessageID)
	if err != nil {
		logger.Error("post exec: get message failed", "message_id", req.Source.MessageID, "err", err)
		go e.executeLoop(req.AgentID)
		return
	}

	// Guard: OnStop (PostPrompt hook) may have already marked this delivered and sent the reply.
	if msg.Status == message.StatusDelivered {
		logger.Debug("post exec: message already delivered by hook, skipping reply", "message_id", msg.ID)
		go e.executeLoop(req.AgentID)
		return
	}

	now := time.Now().UnixMilli()
	msg.Status = message.StatusDelivered
	msg.DeliveryTime = &now
	if err := e.msgStore.UpdateMessage(ctx, msg); err != nil {
		logger.Error("post exec: update message failed", "message_id", msg.ID, "err", err)
	}

	if msg.ShouldReply {
		e.routeReply(msg, req)
	}

	go e.executeLoop(req.AgentID)
}

func (e *ProcessingEngine) handleFailedExec(req AgentCallbackRequest) {
	ctx := e.ctx
	msg, err := e.msgStore.GetMessage(ctx, req.Source.MessageID)
	if err != nil {
		logger.Error("failed exec: get message failed", "message_id", req.Source.MessageID, "err", err)
		go e.executeLoop(req.AgentID)
		return
	}

	msg.Retries++
	if err := e.msgStore.UpdateMessage(ctx, msg); err != nil {
		logger.Error("failed exec: update retries failed", "message_id", msg.ID, "err", err)
	}

	logger.Info("retrying message", "message_id", msg.ID, "retries", msg.Retries)
	go e.executeLoop(req.AgentID)
}

func (e *ProcessingEngine) routeReply(msg *message.Message, req AgentCallbackRequest) {
	if e.reply == nil {
		logger.Warn("route reply: no reply service, reply dropped", "message_id", msg.ID)
		return
	}
	agentState, _ := e.state.GetAgent(req.AgentID)
	if err := e.reply.SendReply(e.ctx, msg, req.AgentID, agentState.Agent.Name); err != nil {
		logger.Error("route reply: send reply failed", "message_id", msg.ID, "err", err)
	}
}

// OnPreSessionStart is called by HookHandler on PreSessionStart events.
// agentID is the UUID key; agentName is the omni-registered name used for exec.
func (e *ProcessingEngine) OnPreSessionStart(agentID, agentName, sessionID, cwd string) {
	logger.Debug("hook: pre session start", "agent_id", agentID, "agent_name", agentName, "session_id", sessionID, "cwd", cwd)

	e.state.SetSession(sessionID, agentID)
	agentState, _ := e.state.GetAgent(agentID)
	agentState.Agent.Name = agentName
	agentState.CodeSession.SessionID = sessionID
	agentState.Status = AgentStatusRunning
	e.state.SetAgent(agentID, agentState)
}

// OnUserPromptSubmit is called by HookHandler on UserPromptSubmit / PrePrompt events.
// Clears orphaned processing messages, then unmarshals the YAML prompt to mark current batch as processing.
func (e *ProcessingEngine) OnUserPromptSubmit(_ context.Context, agentID, sessionID, prompt string) {
	logger.Debug("hook: user prompt submit", "agent_id", agentID, "session_id", sessionID)

	ctx := e.ctx

	// A new prompt means a new session — any messages still in processing are orphaned from a prior session.
	stale, err := e.msgStore.RawQuery(ctx,
		`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id
		 FROM messages WHERE "to" = ? AND status = ?`,
		agentID, string(message.StatusProcessing),
	)
	if err != nil {
		logger.Error("hook: user prompt submit — query stale processing failed", "agent_id", agentID, "err", err)
	}
	for _, msg := range stale {
		msg.Status = message.StatusFailed
		if err := e.msgStore.UpdateMessage(ctx, msg); err != nil {
			logger.Error("hook: mark stale processing failed", "message_id", msg.ID, "err", err)
		}
	}
	if len(stale) > 0 {
		logger.Warn("hook: user prompt submit — cleared stale processing messages", "agent_id", agentID, "count", len(stale))
	}

	// Also sweep stale queued messages whose queue_time exceeded the delivery window.
	cutoff := time.Now().Add(-e.deliveryWindow).UnixMilli()
	staleQueued, err := e.msgStore.RawQuery(ctx,
		`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id
		 FROM messages WHERE "to" = ? AND status = ? AND queue_time > 0 AND queue_time < ?`,
		agentID, string(statusQueued), cutoff,
	)
	if err != nil {
		logger.Error("hook: user prompt submit — query stale queued failed", "agent_id", agentID, "err", err)
	}
	for _, msg := range staleQueued {
		msg.Status = message.StatusFailed
		if err := e.msgStore.UpdateMessage(ctx, msg); err != nil {
			logger.Error("hook: mark stale queued failed", "message_id", msg.ID, "err", err)
		}
	}
	if len(staleQueued) > 0 {
		logger.Warn("hook: user prompt submit — cleared stale queued messages", "agent_id", agentID, "count", len(staleQueued))
	}

	var payload promptPayload
	if err := yaml.Unmarshal([]byte(prompt), &payload); err != nil {
		logger.Debug("hook: user prompt submit — not a engine payload, skipping", "agent_id", agentID)
		return
	}

	for _, item := range payload.Messages {
		if item.MessageID == "" {
			continue
		}
		msg, err := e.msgStore.GetMessage(ctx, item.MessageID)
		if err != nil {
			logger.Error("hook: get message failed", "message_id", item.MessageID, "err", err)
			continue
		}
		msg.Status = message.StatusProcessing
		if err := e.msgStore.UpdateMessage(ctx, msg); err != nil {
			logger.Error("hook: update message to processing failed", "message_id", item.MessageID, "err", err)
		}
	}
	logger.Debug("hook: user prompt submit processed", "agent_id", agentID, "session_id", sessionID, "count", len(payload.Messages))
}

// OnStop is called by HookHandler on Stop / PostPrompt events.
// If IsInterrupted: resets processing messages to in_queue without retry increment.
// If normal: marks processing messages as delivered and routes replies.
func (e *ProcessingEngine) OnStop(_ context.Context, agentID, sessionID string) {
	logger.Debug("hook: stop", "agent_id", agentID, "session_id", sessionID)

	ctx := e.ctx
	agentState, _ := e.state.GetAgent(agentID)

	// Query messages still in processing state for this agent.
	msgs, err := e.msgStore.RawQuery(ctx,
		`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id
		 FROM messages WHERE "to" = ? AND status = ?`,
		agentID, string(message.StatusProcessing),
	)
	if err != nil {
		logger.Error("hook: stop — query processing messages failed", "agent_id", agentID, "err", err)
		return
	}

	if agentState.CodeSession.IsInterrupted {
		logger.Info("hook: stop — interrupted, resetting messages to in_queue", "agent_id", agentID, "count", len(msgs))
		for _, msg := range msgs {
			msg.Status = message.StatusInQueue
			if err := e.msgStore.UpdateMessage(ctx, msg); err != nil {
				logger.Error("hook: reset message failed", "message_id", msg.ID, "err", err)
			}
		}
		agentState.CodeSession.IsInterrupted = false
		agentState.Status = AgentStatusReady
		e.state.SetAgent(agentID, agentState)
	} else {
		queryToolInvoked := agentState.CodeSession.QueryToolInvoked

		// For query-type messages: if the agent never called query_result/query_result_batch,
		// mark them failed and retry — the tool call is the only valid reply path.
		if len(msgs) > 0 && msgs[0].RequestType == reqTypeQuery && !queryToolInvoked {
			logger.Warn("hook: stop — query tool not invoked, marking failed for retry", "agent_id", agentID, "count", len(msgs))
			for _, msg := range msgs {
				msg.Status = message.StatusFailed
				if err := e.msgStore.UpdateMessage(ctx, msg); err != nil {
					logger.Error("hook: mark query failed error", "message_id", msg.ID, "err", err)
				}
			}
			agentState.CodeSession.QueryToolInvoked = false
			agentState.Status = AgentStatusReady
			e.state.SetAgent(agentID, agentState)
			go e.executeLoop(agentID)
		} else {
			logger.Info("hook: stop — success, marking messages delivered", "agent_id", agentID, "count", len(msgs))
			now := time.Now().UnixMilli()
			for _, msg := range msgs {
				msg.Status = message.StatusDelivered
				msg.DeliveryTime = &now
				if err := e.msgStore.UpdateMessage(ctx, msg); err != nil {
					logger.Error("hook: deliver message failed", "message_id", msg.ID, "err", err)
					continue
				}
				// Query type: agent replies via tool call — skip SendReply.
				if msg.RequestType != reqTypeQuery && e.reply != nil {
					if err := e.reply.SendReply(ctx, msg, agentID, agentState.Agent.Name); err != nil {
						logger.Error("hook: send reply failed", "message_id", msg.ID, "err", err)
					}
				}
			}
			agentState.CodeSession.QueryToolInvoked = false
			agentState.Status = AgentStatusReady
			e.state.SetAgent(agentID, agentState)
			go e.executeLoop(agentID)
		}
	}

	e.state.ClearSession(sessionID)
}

// queryToolNames is the set of tunnel_mcp tool names that satisfy a query delivery.
var queryToolNames = map[string]bool{
	"query_result":       true,
	"query_result_batch": true,
}

// OnPreToolUse is called by HookHandler on PreToolUse events.
// Marks QueryToolInvoked when the agent calls a query_result tool.
func (e *ProcessingEngine) OnPreToolUse(agentID, sessionID, toolName string, _ map[string]any) {
	logger.Debug("hook: pre tool use", "agent_id", agentID, "session_id", sessionID, "tool_name", toolName)
	if queryToolNames[toolName] {
		agentState, _ := e.state.GetAgent(agentID)
		agentState.CodeSession.QueryToolInvoked = true
		e.state.SetAgent(agentID, agentState)
		logger.Debug("hook: query tool invoked", "agent_id", agentID, "tool_name", toolName)
	}
}

// OnPostToolUseFailure is called by HookHandler on PostToolUseFailure events.
func (e *ProcessingEngine) OnPostToolUseFailure(agentID, sessionID, toolName, errMsg string) {
	logger.Warn("hook: tool use failure",
		"agent_id", agentID,
		"session_id", sessionID,
		"tool_name", toolName,
		"error", errMsg,
	)
}

// onSessionSync is called by SyncServer after each /session-sync payload.
func (e *ProcessingEngine) onSessionSync(agentID string, usage SessionUsage) {
	logger.Debug("session sync applied", "agent_id", agentID, "consumed_percent", usage.ConsumedPercent)
}
