package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/engine/hook"
	omnicli "github.com/Shaik-Sirajuddin/memory/mcp/pkg/cli"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/agents"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/session"
	"github.com/Shaik-Sirajuddin/memory/store/codesession"
	"gopkg.in/yaml.v3"
)

const (
	sessionUsageThrottlePercent = 95.0
	defaultHookFireTimeout      = 10 * time.Second // watchdog: OnPreSessionStart must fire within this window
	defaultMaxSessionRuntime    = 5 * time.Minute  // watchdog: force session end if OnStop never fires within this window
	staleRetryDelay             = 15 * time.Second // delay before re-queueing a stale message
	maxQueueRetries             = 3                // max re-queue attempts before permanent failure
	maxExecRetries              = 3                // max ExecInSession failures before agent is Stopped
)

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
	// SendFailureCallback notifies the author of msg that delivery failed permanently
	// (agent exhausted retries without calling send_response). Only fires when msg.ShouldReply=true.
	SendFailureCallback(ctx context.Context, msg *message.Message, fromAgentID string) error
}

// AgentWorkspace exposes the engine's current workspace for a sender agent.
type AgentWorkspace interface {
	GetAgentWorkspace(ctx context.Context, agentID string) (string, bool)
}

// StatusCallbackService receives lifecycle events for message delivery.
type StatusCallbackService interface {
	// SendStatusCallback is called when a message is picked for delivery or fails after max retries.
	SendStatusCallback(ctx context.Context, messageID, agentName, teamName string)
	// SendStatusCallbackBatch is called for a set of message IDs at once (e.g. all messages
	// force-delivered after recall exhaustion), so the status update is one bulk send.
	SendStatusCallbackBatch(ctx context.Context, messageIDs []string, agentName, teamName string)
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
	state              *EngineState
	queue              MessageQueue
	agentStore         agents.AgentStore
	omni               omnicli.OmniCLI
	mcp                *MCPClientRegistry
	syncServer         *SyncServer
	socketPath         string
	reply              ReplyService
	statusCallback     StatusCallbackService
	promptSessionStore session.PromptSessionStore
	taskDelivery       session.TaskDeliveryStore // T5: resumable delivery checkpoints
	deliveryWindow     time.Duration
	hookFireTimeout    time.Duration
	maxSessionRuntime  time.Duration
	ctx                context.Context // engine lifetime context, set in Run
}

// Option configures a ProcessingEngine.
type Option func(*ProcessingEngine)

// WithTestBinary replaces the default omni CLI with a test double.
func WithTestBinary(cli omnicli.OmniCLI) Option {
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

// WithHookFireTimeout overrides how long the watchdog waits for OnPreSessionStart to fire.
// Default is 10s. Use a shorter value in tests.
func WithHookFireTimeout(d time.Duration) Option {
	return func(e *ProcessingEngine) { e.hookFireTimeout = d }
}

// WithMaxSessionRuntime overrides how long executeLoop waits for OnStop before force-ending a
// session that never signals completion (e.g. a Claude process stuck retrying an invalid model).
// Default is 5m. Use a shorter value in tests.
func WithMaxSessionRuntime(d time.Duration) Option {
	return func(e *ProcessingEngine) { e.maxSessionRuntime = d }
}

// WithStatusCallback wires a StatusCallbackService for delivery lifecycle events.
func WithStatusCallback(s StatusCallbackService) Option {
	return func(e *ProcessingEngine) { e.statusCallback = s }
}

// WithPromptSessionStore wires a PromptSessionStore for warm-up/active prompt deduplication.
func WithPromptSessionStore(s session.PromptSessionStore) Option {
	return func(e *ProcessingEngine) { e.promptSessionStore = s }
}

// WithTaskDeliveryStore wires a TaskDeliveryStore for resumable delivery checkpointing (T5).
func WithTaskDeliveryStore(s session.TaskDeliveryStore) Option {
	return func(e *ProcessingEngine) { e.taskDelivery = s }
}

// New creates a ProcessingEngine backed by msgStore.
func New(msgStore message.MessageStore, opts ...Option) *ProcessingEngine {
	agentStore, err := agents.GetStore()
	if err != nil {
		logger.Error("engine: failed to init agent store", "err", err)
	}
	e := &ProcessingEngine{
		state:             newEngineState(),
		queue:             newMessageQueue(msgStore),
		agentStore:        agentStore,
		omni:              omnicli.New("omni"),
		mcp:               newMCPClientRegistry(),
		socketPath:        DefaultSyncSocketPath(),
		deliveryWindow:    10 * time.Second,
		hookFireTimeout:   defaultHookFireTimeout,
		maxSessionRuntime: defaultMaxSessionRuntime,
	}
	for _, opt := range opts {
		opt(e)
	}
	e.syncServer = newSyncServer(e.socketPath, e.onSessionSync, e.onSessionResume)
	return e
}

// RegisterHookRoutes registers the engine's hook handler on the provided mux.
// The caller owns the unix socket server and transport — engine only handles routes.
func (e *ProcessingEngine) RegisterHookRoutes(mux *http.ServeMux) {
	mux.Handle("POST /hook", hook.New(e, e.sessionAgentResolver()))
}

// sessionAgentResolver returns an AgentResolver that looks up the owning agent
// for a session ID. Used to recover hooks that fire before adopt writes the agent_id
// (SessionStart races ca.Resume → registerPTYSession).
func (e *ProcessingEngine) sessionAgentResolver() hook.AgentResolver {
	store, err := codesession.GetCodeSessionStore()
	if err != nil {
		logger.Warn("engine: session agent resolver unavailable", "err", err)
		return nil
	}
	return func(sessionID string) string {
		agentID, _, err := store.GetSessionByID(sessionID)
		if err != nil {
			return ""
		}
		return agentID
	}
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
	agentIDs, err := e.queue.PendingAgents(ctx)
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
		workspace, err := e.queue.WorkspaceForAgent(ctx, agentID)
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

		// T5: restore TaskMux for any in-progress task deliveries so the next pick
		// prioritises the interrupted task (last writer wins if multiple in-progress).
		if e.taskDelivery != nil {
			deliveries, err := e.taskDelivery.GetInProgress(ctx, agentID)
			if err != nil {
				logger.Warn("hydrate state: task delivery lookup failed", "agent_id", agentID, "err", err)
			}
			for _, d := range deliveries {
				e.state.SetTaskMux(agentID, &TaskKey{TaskID: d.TaskID, CreatorAgentID: d.CreatorAgentID})
				logger.Info("hydrate state: restoring in-progress task delivery", "agent_id", agentID, "task_id", d.TaskID, "last_message_id", d.LastMessageID)
			}
		}

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
			// Global stale-queued sweep across all agents (no agent filter).
			stale, err := e.queue.ForAgent(ctx, "", QueryOpts{Status: statusQueued, StaleBefore: cutoff})
			if err != nil {
				logger.Error("queue sweep: query failed", "err", err)
				continue
			}
			for _, msg := range stale {
				// Skip messages whose target agent is currently running: ExecInSession is in
				// flight (or the session is live) and the message is being actively processed,
				// not orphaned. During LLM cold-start (~29s) the message stays statusQueued
				// until the UserPromptSubmit hook flips it to processing — re-queueing it here
				// would strand it in_queue so the eventual send_response can't deliver it.
				// A genuinely-stale message (exec never started, or the session already ended)
				// has its agent in a non-Running state and is still swept on a later tick.
				if st, ok := e.state.GetAgent(msg.To); ok && st.Status == AgentStatusRunning {
					continue
				}
				if msg.Retries < maxQueueRetries {
					// Re-queue so the agent can retry after a short delay.
					msg.Status = message.StatusInQueue
					msg.QueueTime = 0 // cleared so the sweep doesn't immediately re-flag it
					if err := e.queue.Update(ctx, msg); err != nil {
						logger.Error("queue sweep: re-queue failed", "message_id", msg.ID, "err", err)
						continue
					}
					logger.Warn("queue sweep: stale message re-queued with delay",
						"message_id", msg.ID, "retries", msg.Retries)
					agentID := msg.To
					go func() {
						select {
						case <-ctx.Done():
							return
						case <-time.After(staleRetryDelay):
						}
						go e.executeLoop(agentID)
					}()
				} else {
					msg.Status = message.StatusFailed
					if err := e.queue.Update(ctx, msg); err != nil {
						logger.Error("queue sweep: mark failed error", "message_id", msg.ID, "err", err)
					}
					logger.Warn("queue sweep: max retries exceeded, message failed",
						"message_id", msg.ID, "retries", msg.Retries)
				}
			}
			if len(stale) > 0 {
				logger.Warn("queue sweep: processed stale queued messages", "count", len(stale))
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
	workspace, err := e.queue.WorkspaceForAgent(ctx, agentID)
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

	// Throttle at 95% session usage.
	if agentState.SessionUsage.ConsumedPercent >= sessionUsageThrottlePercent {
		logger.Info("execute loop: throttled — session usage high",
			"agent_id", agentID,
			"consumed_percent", agentState.SessionUsage.ConsumedPercent,
		)
		return
	}

	// T3: atomically claim the run. BeginRunIfIdle collapses the running-check and the
	// running-set into one locked operation. The old code checked Status==Running here but
	// only set it ~165 lines later (after recall query, pickNextMessages, queue writes), so
	// two executeLoop goroutines (launched from any of ~12 dispatch sites) could both observe
	// Ready and both call ExecInSession — double dispatch into an in-flight session.
	if !e.state.BeginRunIfIdle(agentID) {
		logger.Debug("execute loop: agent already running/claimed, skipping", "agent_id", agentID)
		return
	}
	agentState.Status = AgentStatusRunning
	// If the run is claimed but we decide there is nothing to dispatch (no messages, query
	// error, no task mux for bypass), release the claim back to Ready so the next trigger
	// can proceed. Set on the path that actually calls ExecInSession.
	dispatched := false
	defer func() {
		if dispatched {
			return
		}
		cur, ok := e.state.GetAgent(agentID)
		if ok && cur.Status == AgentStatusRunning {
			cur.Status = AgentStatusReady
			e.state.SetAgent(agentID, cur)
		}
	}()

	// Preprocessing case (a): check for unacknowledged StatusProcessing non-instant messages FIRST.
	// This must come before the T3 guard — a processing execute would otherwise trigger T3 and
	// return early before we can recall it. Instant (steer) messages are exempt since they don't
	// require a mandatory tool response.
	//
	// IMPORTANT: recall prompt must be YAML (buildWarmUpPrompt) not plain text (buildRecallPrompt).
	// ExecInSession fires OnUserPromptSubmit which runs the stale sweep (StatusProcessing → Failed)
	// then re-advances message IDs found in the YAML payload back to StatusProcessing.
	// Plain-text prompts fail the YAML parse and leave messages permanently Failed.
	//
	// Case (b): no processing messages — fall through to T3 check + normal pick.
	processingRecall, procErr := e.queue.ForAgent(ctx, agentID, QueryOpts{
		Status:         message.StatusProcessing,
		NotRequestType: reqTypeInstant,
	})
	if procErr != nil {
		logger.Error("execute loop: preprocessing recall check failed", "agent_id", agentID, "err", procErr)
		return
	}
	var msgs []*message.Message
	var recallPrompt string
	isPreprocessingRecall := false
	var err error
	if len(processingRecall) > 0 {
		msgs = processingRecall
		// Count this as a delivery attempt so the mandatory-tool retry counter advances.
		// The normal markMessagesQueued path is skipped for preprocessing recall, so we
		// must increment here to prevent the OnStop recall guard from looping forever.
		for _, msg := range msgs {
			msg.Retries++
			if err := e.queue.Update(ctx, msg); err != nil {
				logger.Error("execute loop: preprocessing recall — retries increment failed", "message_id", msg.ID, "err", err)
				return // avoid building recall with inconsistently incremented retries
			}
		}
		recallPrompt = buildWarmUpPrompt(msgs)
		isPreprocessingRecall = true
		logger.Warn("execute loop: preprocessing recall — unacknowledged processing messages",
			"agent_id", agentID, "count", len(msgs))
	} else {
		// Case (b): T3: only an in-flight execute blocks new picks. Queries may bypass when task_id matches (T2).
		var activeExecute []*message.Message
		activeExecute, err = e.queue.ForAgent(ctx, agentID, QueryOpts{
			StatusIn:    []message.Status{statusQueued, message.StatusProcessing},
			RequestType: reqTypeExecute,
			Limit:       1,
		})
		if err != nil {
			logger.Error("execute loop: active execute check failed", "agent_id", agentID, "err", err)
			return
		}

		// bypassTask is non-nil when a query bypass is allowed for an in-flight execute (T2).
		var bypassTask *TaskKey
		if len(activeExecute) > 0 {
			mux := e.state.GetTaskMux(agentID)
			if mux == nil || mux.IsZero() {
				logger.Debug("execute loop: execute in flight, no task mux — skipping", "agent_id", agentID)
				return
			}
			bypassTask = mux
			logger.Debug("execute loop: execute in flight, checking bypass queries", "agent_id", agentID, "task_id", mux.TaskID)
		}

		msgs, err = e.pickNextMessages(agentID, bypassTask)
		if err != nil {
			logger.Error("execute loop: pick messages failed", "agent_id", agentID, "err", err)
			return
		}
		if len(msgs) == 0 {
			if bypassTask != nil {
				logger.Debug("execute loop: no bypass queries for task", "agent_id", agentID, "task_id", bypassTask.TaskID)
			} else {
				logger.Debug("execute loop: no pending messages, clearing delivery flag", "agent_id", agentID)
				e.state.SetPending(agentID, false)
			}
			return
		}
	}

	if !isPreprocessingRecall {
		// T1/T2: update TaskMux on every execute pick.
		// Cleared on untagged execute to prevent stale task from enabling bypass injection.
		// Retained after execute completes (not cleared in markDelivered) for T1 next-loop priority.
		if msgs[0].RequestType == reqTypeExecute {
			if msgs[0].TaskID != "" {
				e.state.SetTaskMux(agentID, &TaskKey{TaskID: msgs[0].TaskID, CreatorAgentID: msgs[0].CreatorAgentID})
			} else {
				e.state.SetTaskMux(agentID, nil)
			}
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
			if err := e.queue.Update(ctx, msg); err != nil {
				logger.Error("execute loop: update message to queued failed", "message_id", msg.ID, "err", err)
			}
		}

		// T5: checkpoint execute delivery so it can be resumed on restart.
		if e.taskDelivery != nil && msgs[0].RequestType == reqTypeExecute && msgs[0].TaskID != "" {
			if err := e.taskDelivery.StartDelivery(ctx, msgs[0].TaskID, agentID, msgs[0].CreatorAgentID, msgs[0].ID); err != nil {
				logger.Warn("execute loop: task delivery checkpoint failed", "task_id", msgs[0].TaskID, "err", err)
			}
		}
	}

	agentState, _ = e.state.GetAgent(agentID)

	// Build the prompt: recall (preprocessing case a) takes precedence over warm-up/active.
	var prompt string
	if isPreprocessingRecall {
		prompt = recallPrompt
	} else {
		// Determine warm-up vs active prompt via PromptSessionStore.
		// warmupSentinel is a fixed key: once marked, all subsequent deliveries to the
		// same session use the lean active prompt instead of the full warm-up prompt.
		const warmupSentinel = "warmup_done"
		sessionID := agentState.CodeSession.SessionID
		isWarmUp := true
		if e.promptSessionStore != nil && sessionID != "" {
			if e.promptSessionStore.IsDelivered(ctx, sessionID, warmupSentinel) {
				isWarmUp = false
			}
		}
		if isWarmUp {
			prompt = buildWarmUpPrompt(msgs)
			if e.promptSessionStore != nil && sessionID != "" {
				if err := e.promptSessionStore.MarkDelivered(ctx, sessionID, warmupSentinel); err != nil {
					logger.Warn("execute loop: mark delivered failed", "session_id", sessionID, "err", err)
				}
			}
		} else {
			senderName := senderNameFromRefs(msgs[0].Refs)
			prompt = buildActivePrompt(msgs, senderName)
		}
	}

	agentState.Status = AgentStatusRunning
	e.state.SetAgent(agentID, agentState)
	// Capture generation AFTER marking Running so the watchdog and onSessionEnd can
	// detect whether a newer session (from markDelivered) has taken over.
	myGeneration := agentState.CodeSession.SessionGeneration
	prevSessionID := agentState.CodeSession.SessionID

	// Open a session-done channel so we can wait for OnStop to fire before running
	// onSessionEnd. omni agent exec --bg is non-blocking: it returns in <1s while the
	// actual Claude session runs asynchronously. Without this wait, onSessionEnd would
	// mark messages as orphaned/failed before any hooks (OnStop, OnUserPromptSubmit)
	// have a chance to process them.
	sessionDoneCh := e.state.OpenSessionDone(agentID)

	// Committed to dispatch: the deferred release must not run — session lifecycle
	// (hooks / onSessionEnd / watchdog) owns Status from here on.
	dispatched = true

	logger.Info("execute loop: executing agent",
		"agent_id", agentID,
		"message_count", len(msgs),
		"first_message_id", msgs[0].ID,
	)

	if e.statusCallback != nil {
		e.statusCallback.SendStatusCallback(ctx, msgs[0].ID, agentState.Agent.Name, agentState.Agent.Team)
	}

	// Watchdog: if OnPreSessionStart doesn't fire within hookFireTimeout (e.g. omni process
	// failed to start), reset to Ready and re-trigger so pending messages aren't lost.
	go func(gen int, prevSID string) {
		select {
		case <-e.ctx.Done():
			return
		case <-time.After(e.hookFireTimeout):
		}
		cur, ok := e.state.GetAgent(agentID)
		if !ok || cur.CodeSession.IsInterrupted {
			return
		}
		// Only intervene when: still Running, our generation is current (no delivery happened),
		// and SessionID is unchanged (OnPreSessionStart never fired for this exec call).
		if cur.Status == AgentStatusRunning &&
			cur.CodeSession.SessionGeneration == gen &&
			cur.CodeSession.SessionID == prevSID {
			logger.Warn("execute loop watchdog: hook timeout, resetting to Ready", "agent_id", agentID)
			cur.Status = AgentStatusReady
			e.state.SetAgent(agentID, cur)
			// Unblock the sessionDoneCh waiter so onSessionEnd can run cleanup.
			e.state.SignalSessionDone(agentID)
			go e.executeLoop(agentID)
		}
	}(myGeneration, prevSessionID)

	execErr := e.omni.ExecInSession(ctx, agentID, agentState.Agent.Name, agentState.Agent.Workspace, prompt)
	if execErr != nil {
		logger.Error("execute loop: exec failed", "agent_id", agentID, "err", execErr)
	}

	// If ExecInSession returned without error, wait for OnStop to signal that the session
	// has truly ended. This handles non-blocking exec implementations (e.g. omni agent exec
	// --bg) where ExecInSession returns before hooks fire. The watchdog also signals this
	// channel if OnPreSessionStart never fires within hookFireTimeout.
	if execErr == nil {
		select {
		case <-sessionDoneCh:
			logger.Debug("execute loop: session done signal received", "agent_id", agentID)
		case <-time.After(e.maxSessionRuntime):
			// Max-runtime watchdog: OnStop never fired within the cap. The Claude process is
			// hung (e.g. retrying an invalid model alias) and would otherwise hold the agent slot
			// Running forever, blocking ALL subsequent messages with no recovery path. Fall through
			// to onSessionEnd: it fail-delivers the in-flight (still queued/processing) message and
			// resets the agent to Ready so the post-loop retry can dispatch the next one.
			// NOTE: this frees the engine slot but does not kill the orphaned OS process — that
			// requires ptydaemon/operator-side termination (tracked separately).
			logger.Warn("execute loop: max session runtime exceeded, forcing session end",
				"agent_id", agentID, "max_runtime", e.maxSessionRuntime)
		case <-e.ctx.Done():
		}
	}
	e.state.ClearSessionDone(agentID)

	// Session-end guard: hooks (OnStop) fire during the session and should have already
	// cleaned up status and messages. If they didn't (crash / no PostPrompt), do it here.
	e.onSessionEnd(agentID, msgs, execErr != nil, myGeneration)

	// Reset queue_time so the cron sweep doesn't re-flag these messages.
	// Re-fetch each message from DB: OnStop (inside ExecInSession) may have already marked them
	// StatusDelivered. Writing the stale local copy (still statusQueued) would overwrite that,
	// causing the post-loop executeLoop to pick and re-deliver the same message.
	now := time.Now().UnixMilli()
	for _, msg := range msgs {
		fresh, ferr := e.queue.Get(ctx, msg.ID)
		if ferr != nil {
			logger.Error("execute loop: get message for queue_time reset failed", "message_id", msg.ID, "err", ferr)
			continue
		}
		if fresh.Status != statusQueued {
			continue // OnStop already advanced the status; don't overwrite
		}
		fresh.QueueTime = now
		if err := e.queue.Update(ctx, fresh); err != nil {
			logger.Error("execute loop: reset queue_time failed", "message_id", msg.ID, "err", err)
		}
	}

	// Post-loop retry: pick any pending messages that arrived while this session was running.
	// markDelivered intentionally does not spawn executeLoop; this is the sole path that
	// triggers the next delivery after ExecInSession fully returns.
	// Skipped on interrupt and stopped (exec-failed) states.
	cur, _ := e.state.GetAgent(agentID)
	if !cur.CodeSession.IsInterrupted && cur.Status != AgentStatusStopped {
		go e.executeLoop(agentID)
	}
}

// onSessionEnd is called after ExecInSession returns to clean up state that hooks may have missed
// (e.g. if the omni process crashed before PostPrompt fired).
// msgs is used only for IDs — status is re-queried from DB to avoid stale overwrites.
// myGeneration is the SessionGeneration captured before ExecInSession was called; it guards
// against resetting Running status when a newer session (spawned by markDelivered) has taken over.
func (e *ProcessingEngine) onSessionEnd(agentID string, msgs []*message.Message, execFailed bool, myGeneration int) {
	ctx := e.ctx
	agentState, _ := e.state.GetAgent(agentID)

	if execFailed {
		agentState.StopReason = StopReasonOther
		agentState.Status = AgentStatusStopped // default; overridden below when retry is allowed
	} else if agentState.Status == AgentStatusRunning && agentState.CodeSession.SessionGeneration == myGeneration {
		// Status still Running AND generation unchanged means PostPrompt never fired for this
		// session (hooks missed). Safe to reset — no newer session has taken over.
		agentState.Status = AgentStatusReady
	}
	// If generation advanced (markDelivered ran concurrently with a new session arrival), leave status as-is.

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

	// On exec failure: re-queue messages below the retry cap for another attempt.
	// Interrupted or cap-exhausted → permanently fail.
	// On exec success with orphaned messages (hooks missed) → original safety-net: mark Failed.
	canRetry := execFailed && !agentState.CodeSession.IsInterrupted
	anyRequeued := false

	for _, id := range ids {
		msg, err := e.queue.Get(ctx, id)
		if err != nil {
			logger.Error("session end: get message failed", "message_id", id, "err", err)
			continue
		}
		if msg.Status != statusQueued && msg.Status != message.StatusProcessing {
			continue // OnStop already handled this message
		}
		if canRetry && msg.Retries < maxExecRetries {
			msg.Status = message.StatusInQueue
			msg.QueueTime = 0
			logger.Warn("session end: exec failed, re-queuing for retry",
				"message_id", id, "retries", msg.Retries)
			if err := e.queue.Update(ctx, msg); err != nil {
				logger.Error("session end: re-queue failed", "message_id", id, "err", err)
			} else {
				anyRequeued = true
			}
		} else {
			logger.Warn("session end: orphaned message, marking failed",
				"message_id", id, "status", msg.Status, "exec_failed", execFailed)
			msg.Status = message.StatusFailed
			if err := e.queue.Update(ctx, msg); err != nil {
				logger.Error("session end: mark failed error", "message_id", id, "err", err)
			}
		}
	}

	// Reset to Ready so the post-loop retry can fire another ExecInSession attempt.
	if anyRequeued {
		agentState, _ = e.state.GetAgent(agentID)
		agentState.Status = AgentStatusReady
		e.state.SetAgent(agentID, agentState)
	}
}

// pickNextMessages selects the next batch of messages for agentID.
//
// bypassTask non-nil: T2 bypass mode — execute in flight, only pick queries for that task.
//
// Normal mode (bypassTask nil):
//
//	Execute: picks 1 execute; bundles co-task non-execute messages (same task_id, then same group_id fallback).
//	Query/instant: accumulate up to 5 from same sender, stop before execute.
//	T1 priority: TaskMux retained from last execute — messages with matching task_id sorted first.
//	T4: messages for paused tasks are skipped.
func (e *ProcessingEngine) pickNextMessages(agentID string, bypassTask *TaskKey) ([]*message.Message, error) {
	// T2 bypass: execute in flight — pick only queries for the matching task.
	if bypassTask != nil && !bypassTask.IsZero() {
		return e.pickBypassQueries(agentID, *bypassTask)
	}

	// T1: use TaskMux for task_id-first ordering when available.
	taskMux := e.state.GetTaskMux(agentID)
	var msgs []*message.Message
	var err error
	opt := QueryOpts{Status: message.StatusInQueue, Limit: 20}
	if taskMux != nil && !taskMux.IsZero() {
		opt.TaskFirst = taskMux // T1: task_id-first ordering
	}
	msgs, err = e.queue.ForAgent(e.ctx, agentID, opt)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nil
	}

	first := msgs[0]

	// T4: skip paused task.
	if first.TaskID != "" && e.state.IsTaskPaused(agentID, first.TaskID) {
		logger.Debug("pick: task is paused, skipping", "agent_id", agentID, "task_id", first.TaskID)
		return nil, nil
	}

	if first.RequestType == reqTypeExecute {
		picked := []*message.Message{first}
		// Bundle co-task non-execute messages: prefer task_id match, fall back to group_id.
		for _, msg := range msgs[1:] {
			if msg.RequestType == reqTypeExecute {
				continue
			}
			if first.TaskID != "" && msg.TaskID == first.TaskID && msg.CreatorAgentID == first.CreatorAgentID {
				picked = append(picked, msg)
			} else if first.TaskID == "" && first.GroupID != "" && msg.GroupID == first.GroupID {
				picked = append(picked, msg)
			}
		}
		return picked, nil
	}

	// Accumulate query/instant from the same sender, up to 5, stop before execute.
	// No task filter here — in normal mode (no active execute) all pending messages are
	// eligible regardless of task_id. Task filtering only applies in T2 bypass mode
	// (pickBypassQueries) where an execute is actively in flight. Filtering in normal mode
	// would permanently block task-T2 messages if TaskMux is stuck on T1 with no new
	// execute arriving to update it.
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

// pickBypassQueries picks pending queries for the given task when an execute is in flight (T2).
func (e *ProcessingEngine) pickBypassQueries(agentID string, key TaskKey) ([]*message.Message, error) {
	return e.queue.ForAgent(e.ctx, agentID, QueryOpts{
		Status:      message.StatusInQueue,
		Task:        &key,
		RequestType: reqTypeQuery,
		Limit:       5,
	})
}

type promptItem struct {
	MessageID   string `yaml:"message_id"`
	RequestType string `yaml:"request_type,omitempty"` // per-item; set in mixed-type batches
	Refs        string `yaml:"refs,omitempty"`
	Prompt      string `yaml:"prompt"`
}

// promptPayload — messages first, instruction last (stronger influence at bottom of prompt).
// warm_up detection is embedded in each item's refs JSON (key "warm_up": true) rather than
// as a top-level field, so the agent reads it from the same refs object as sender identity.
type promptPayload struct {
	Messages    []promptItem `yaml:"messages"`
	Instruction string       `yaml:"instruction"`
}

// isMixedBatch reports whether msgs contains more than one distinct request type.
func isMixedBatch(msgs []*message.Message) bool {
	if len(msgs) == 0 {
		return false
	}
	rt := msgs[0].RequestType
	for _, m := range msgs[1:] {
		if m.RequestType != rt {
			return true
		}
	}
	return false
}

var warmUpInstruction = map[message.RequestType]string{
	reqTypeExecute: "Execute the following task. Call `send_response(message_id, ...)` after completing.",
	reqTypeQuery:   "Answer the following queries. Call `send_response(message_id, response)` for each.",
	reqTypeInstant: "Process the following messages.",
}

const warmUpMixedInstruction = "Complete the task and answer all queries. Call `send_response(message_id, ...)` for each message_id."

var activeInstruction = map[message.RequestType]string{
	reqTypeExecute: "Continue from %s. Call `send_response` when done.",
	reqTypeQuery:   "Reply to %s using `send_response` or `send_response_batch`.",
	reqTypeInstant: "Process the following from %s.",
}

const activeMixedInstruction = "Continue from %s. Call `send_response` for each message_id."

// buildWarmUpPrompt builds a full prompt for a first delivery to a session.
// Supports mixed-type batches (execute + co-task queries): per-item request_type is set,
// instruction is unified and appended last.
func buildWarmUpPrompt(msgs []*message.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	mixed := isMixedBatch(msgs)
	var instruction string
	if mixed {
		instruction = warmUpMixedInstruction
	} else {
		rt := msgs[0].RequestType
		var ok bool
		instruction, ok = warmUpInstruction[rt]
		if !ok {
			instruction = "Process the following."
		}
	}
	items := make([]promptItem, len(msgs))
	for i, msg := range msgs {
		items[i] = promptItem{
			MessageID: msg.ID,
			Refs:      msg.Refs,
			Prompt:    msg.Prompt,
		}
		if mixed {
			items[i].RequestType = string(msg.RequestType)
		}
	}
	payload := promptPayload{
		Messages:    items,
		Instruction: instruction,
	}
	out, err := yaml.Marshal(payload)
	if err != nil {
		logger.Error("build warm-up prompt: yaml marshal failed", "err", err)
		return ""
	}
	return string(out)
}

// buildActivePrompt builds a lean prompt for subsequent deliveries to an already-warm session.
// References sender name in the instruction; omits refs to reduce token usage.
// Supports mixed-type batches with per-item request_type and unified instruction.
func buildActivePrompt(msgs []*message.Message, senderName string) string {
	if len(msgs) == 0 {
		return ""
	}
	if senderName == "" {
		senderName = "sender"
	}
	mixed := isMixedBatch(msgs)
	var instruction string
	if mixed {
		instruction = fmt.Sprintf(activeMixedInstruction, senderName)
	} else {
		rt := msgs[0].RequestType
		tmpl, ok := activeInstruction[rt]
		if !ok {
			tmpl = "Process the following from %s."
		}
		instruction = fmt.Sprintf(tmpl, senderName)
	}
	items := make([]promptItem, len(msgs))
	for i, msg := range msgs {
		items[i] = promptItem{
			MessageID: msg.ID,
			Prompt:    msg.Prompt,
		}
		if mixed {
			items[i].RequestType = string(msg.RequestType)
		}
	}
	payload := promptPayload{
		Messages:    items,
		Instruction: instruction,
	}
	out, err := yaml.Marshal(payload)
	if err != nil {
		logger.Error("build active prompt: yaml marshal failed", "err", err)
		return ""
	}
	return string(out)
}

// senderNameFromRefs extracts author_agent_name from a message refs JSON string.
func senderNameFromRefs(refs string) string {
	if refs == "" {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(refs), &m); err != nil {
		logger.Debug("senderNameFromRefs: JSON parse failed", "err", err)
		return ""
	}
	var name string
	if v, ok := m["author_agent_name"]; ok {
		_ = json.Unmarshal(v, &name)
	}
	return name
}

func (e *ProcessingEngine) handlePostExec(req AgentCallbackRequest) {
	ctx := e.ctx
	msg, err := e.queue.Get(ctx, req.Source.MessageID)
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
	if err := e.queue.Update(ctx, msg); err != nil {
		logger.Error("post exec: update message failed", "message_id", msg.ID, "err", err)
	}

	if msg.ShouldReply {
		e.routeReply(msg, req)
	}

	go e.executeLoop(req.AgentID)
}

func (e *ProcessingEngine) handleFailedExec(req AgentCallbackRequest) {
	ctx := e.ctx
	msg, err := e.queue.Get(ctx, req.Source.MessageID)
	if err != nil {
		logger.Error("failed exec: get message failed", "message_id", req.Source.MessageID, "err", err)
		go e.executeLoop(req.AgentID)
		return
	}

	msg.Retries++
	if err := e.queue.Update(ctx, msg); err != nil {
		logger.Error("failed exec: update retries failed", "message_id", msg.ID, "err", err)
	}

	if msg.Retries >= maxExecRetries {
		logger.Warn("failed exec: max retries reached, stopping agent",
			"message_id", msg.ID, "retries", msg.Retries)
		msg.Status = message.StatusFailed
		if err := e.queue.Update(ctx, msg); err != nil {
			logger.Error("failed exec: mark failed error", "message_id", msg.ID, "err", err)
		}
		agentState, _ := e.state.GetAgent(req.AgentID)
		agentState.Status = AgentStatusStopped
		agentState.StopReason = StopReasonOther
		e.state.SetAgent(req.AgentID, agentState)
		return
	}

	logger.Info("failed exec: retrying message", "message_id", msg.ID, "retries", msg.Retries)
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

// hydrateAgentState is the shared create/resume handler invoked by SyncServer.
// It ensures agentID is present in memory (loading identity from the store via
// bootstrapAgent if absent), clears any interrupted flag, and marks the agent
// Ready — WITHOUT triggering delivery (no executeLoop). It never downgrades an
// agent that is currently Running. Returns the resulting state so callers can
// layer extra fields (e.g. session usage) on top before the final write.
func (e *ProcessingEngine) hydrateAgentState(agentID string) AgentState {
	st, ok := e.state.GetAgent(agentID)
	if !ok || st.Agent.Name == "" {
		if e.bootstrapAgent(agentID) {
			st, _ = e.state.GetAgent(agentID)
		} else if !ok {
			st = AgentState{Agent: Agent{AgentID: agentID}}
		}
	}
	st.CodeSession.IsInterrupted = false
	if st.Status != AgentStatusRunning {
		st.Status = AgentStatusReady
	}
	e.state.SetAgent(agentID, st)
	return st
}

// onSessionSync is called by SyncServer on a KindSessionSync payload (also used as
// the create seed). It hydrates/syncs in-memory state from the store and records
// the latest session usage. No delivery is triggered.
func (e *ProcessingEngine) onSessionSync(agentID string, usage SessionUsage) {
	st := e.hydrateAgentState(agentID)
	st.SessionUsage = usage
	e.state.SetAgent(agentID, st)
	logger.Debug("session sync applied", "agent_id", agentID, "consumed_percent", usage.ConsumedPercent)
}

// onSessionResume is called by SyncServer on a KindResume payload. It routes to the
// same hydrate/sync handler as create — clearing interrupted and ensuring the agent
// is in memory — without retriggering executeLoop.
func (e *ProcessingEngine) onSessionResume(agentID string) {
	e.hydrateAgentState(agentID)
	logger.Debug("session resume applied", "agent_id", agentID)
}

// PauseTask pauses delivery of messages for the given task to agentID (T4).
// Signature matches the service.taskPauser interface (plain strings, no cross-package type).
func (e *ProcessingEngine) PauseTask(agentID, taskID, creatorAgentID string) {
	if taskID == "" {
		return
	}
	e.state.PauseTask(agentID, taskID)
	logger.Info("engine: task paused", "agent_id", agentID, "task_id", taskID)
}

// ResumeTask clears the paused flag and triggers delivery for agentID (T4).
// Signature matches the service.taskResumer interface.
func (e *ProcessingEngine) ResumeTask(ctx context.Context, agentID, taskID, creatorAgentID string) {
	if taskID == "" {
		return
	}
	e.state.ResumeTask(agentID, taskID)
	logger.Info("engine: task resumed", "agent_id", agentID, "task_id", taskID)
	go e.executeLoop(agentID)
}
