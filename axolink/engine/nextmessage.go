package engine

import (
	"context"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/session"
)

// StopOutcome is the decision NextMessage.OnStop returns to the Stop hook handler.
type StopOutcome struct {
	Deliver []*message.Message // confirmed (or instant) → markDelivered(force=false)
	Force   []*message.Message // unconfirmed but retries-exhausted → markDelivered(force=true)
	Recall  *string            // non-nil → inject as systemMessage, keep session Running
	Settle  bool               // nothing left pending → clear session + signal done
}

// SubmitOutcome is the decision NextMessage.OnPromptSubmit returns to the
// UserPromptSubmit hook handler.
type SubmitOutcome struct {
	Fail    []string // orphaned ids (not in the current batch) → Advance(Failed)
	Process []string // current-batch ids → Advance(Processing)
}

// NextMessage owns the selection + decisioning brain of delivery. It reads/writes
// message rows only through MessageQueue; the engine keeps the side-effecting
// runtime (claim, ExecInSession, lifecycle, markDelivered).
type NextMessage interface {
	// Plan selects the next batch to deliver for agentID (T1/T2 ordering, T4 paused
	// skip, execute bundling, query/instant accumulation). bypass non-nil restricts
	// to query bypass for an in-flight execute (T2).
	Plan(ctx context.Context, agentID string, bypass *TaskKey) ([]*message.Message, error)
	// Active returns the agent's in-flight (StatusProcessing) messages — the single
	// "active message detection" used by the Stop and PostToolUse hooks.
	Active(ctx context.Context, agentID string) ([]*message.Message, error)
	// Recall returns unacknowledged processing (non-instant) messages for the
	// executeLoop preprocessing-recall path, incrementing their retry counter.
	Recall(ctx context.Context, agentID string) ([]*message.Message, error)
	// Prompt renders the delivery prompt for msgs: warm-up vs active is chosen via
	// the prompt-session store; isRecall forces the warm-up (YAML) form.
	Prompt(ctx context.Context, agentID string, msgs []*message.Message, isRecall bool) string
	// OnStop decides deliver/force/recall/settle from the processing set and the
	// per-message confirmation predicate; increments recall retry counters.
	OnStop(ctx context.Context, agentID string, processing []*message.Message, confirmed func(id string) bool) (StopOutcome, error)
	// OnPromptSubmit decides which orphans to fail and which current-batch ids to
	// advance to Processing on a UserPromptSubmit.
	OnPromptSubmit(ctx context.Context, agentID string, batchIDs []string) (SubmitOutcome, error)
	// OnToolSuccess records per-message delivery confirmation for the ids named by a
	// successful confirming tool.
	OnToolSuccess(ctx context.Context, agentID string, messageIDs []string)
}

type nextMessage struct {
	queue          MessageQueue
	state          *EngineState
	promptStore    session.PromptSessionStore
	deliveryWindow time.Duration
}

func newNextMessage(queue MessageQueue, state *EngineState, promptStore session.PromptSessionStore, deliveryWindow time.Duration) NextMessage {
	return &nextMessage{queue: queue, state: state, promptStore: promptStore, deliveryWindow: deliveryWindow}
}

// Plan selects the next batch (relocated from ProcessingEngine.pickNextMessages).
func (n *nextMessage) Plan(ctx context.Context, agentID string, bypass *TaskKey) ([]*message.Message, error) {
	// T2 bypass: execute in flight — pick only queries for the matching task.
	if bypass != nil && !bypass.IsZero() {
		return n.queue.ForAgent(ctx, agentID, QueryOpts{
			Status:      message.StatusInQueue,
			Task:        bypass,
			RequestType: reqTypeQuery,
			Limit:       5,
		})
	}

	// T1: use TaskMux for task_id-first ordering when available.
	opt := QueryOpts{Status: message.StatusInQueue, Limit: 20}
	if mux := n.state.GetTaskMux(agentID); mux != nil && !mux.IsZero() {
		opt.TaskFirst = mux
	}
	msgs, err := n.queue.ForAgent(ctx, agentID, opt)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nil
	}

	first := msgs[0]

	// T4: skip paused task.
	if first.TaskID != "" && n.state.IsTaskPaused(agentID, first.TaskID) {
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

func (n *nextMessage) Active(ctx context.Context, agentID string) ([]*message.Message, error) {
	return n.queue.ForAgent(ctx, agentID, QueryOpts{Status: message.StatusProcessing})
}

func (n *nextMessage) Recall(ctx context.Context, agentID string) ([]*message.Message, error) {
	msgs, err := n.queue.ForAgent(ctx, agentID, QueryOpts{
		Status:         message.StatusProcessing,
		NotRequestType: reqTypeInstant,
	})
	if err != nil {
		return nil, err
	}
	// Count this as a delivery attempt so the mandatory-tool retry counter advances.
	for _, msg := range msgs {
		msg.Retries++
		if err := n.queue.Update(ctx, msg); err != nil {
			return nil, err
		}
	}
	return msgs, nil
}

func (n *nextMessage) Prompt(ctx context.Context, agentID string, msgs []*message.Message, isRecall bool) string {
	if isRecall {
		// recall must be the YAML warm-up form so OnUserPromptSubmit can re-advance
		// the referenced message IDs back to StatusProcessing.
		return buildWarmUpPrompt(msgs)
	}
	// Determine warm-up vs active prompt via PromptSessionStore. Once warmed, all
	// subsequent deliveries to the same session use the lean active prompt.
	const warmupSentinel = "warmup_done"
	agentState, _ := n.state.GetAgent(agentID)
	sessionID := agentState.CodeSession.SessionID
	isWarmUp := true
	if n.promptStore != nil && sessionID != "" {
		if n.promptStore.IsDelivered(ctx, sessionID, warmupSentinel) {
			isWarmUp = false
		}
	}
	if isWarmUp {
		if n.promptStore != nil && sessionID != "" {
			if err := n.promptStore.MarkDelivered(ctx, sessionID, warmupSentinel); err != nil {
				logger.Warn("next: mark warmup delivered failed", "session_id", sessionID, "err", err)
			}
		}
		return buildWarmUpPrompt(msgs)
	}
	return buildActivePrompt(msgs, senderNameFromRefs(msgs[0].Refs))
}

func (n *nextMessage) OnStop(ctx context.Context, agentID string, processing []*message.Message, confirmed func(id string) bool) (StopOutcome, error) {
	// Per-message delivery confirmation: instant messages need none; others must be
	// confirmed by a tool callback this session, else they ride the recall loop.
	var confirmedMsgs, unconfirmed []*message.Message
	for _, msg := range processing {
		if msg.RequestType == reqTypeInstant || confirmed(msg.ID) {
			confirmedMsgs = append(confirmedMsgs, msg)
		} else {
			unconfirmed = append(unconfirmed, msg)
		}
	}

	// Split unconfirmed into still-retriable vs exhausted (per-message retry counter).
	var recallable, exhausted []*message.Message
	for _, msg := range unconfirmed {
		if msg.Retries <= maxMandatoryToolRetries {
			recallable = append(recallable, msg)
		} else {
			exhausted = append(exhausted, msg)
		}
	}

	out := StopOutcome{Deliver: confirmedMsgs, Force: exhausted}
	if len(recallable) > 0 {
		// Increment each counter before injecting recall so the per-message guard
		// advances every turn; without this the recall would loop forever.
		for _, msg := range recallable {
			msg.Retries++
			if err := n.queue.Update(ctx, msg); err != nil {
				logger.Error("next: stop — recall retries increment failed", "message_id", msg.ID, "err", err)
			}
		}
		recall := buildRecallPrompt(recallable)
		out.Recall = &recall
	} else {
		out.Settle = true
	}
	return out, nil
}

func (n *nextMessage) OnPromptSubmit(ctx context.Context, agentID string, batchIDs []string) (SubmitOutcome, error) {
	batch := make(map[string]bool, len(batchIDs))
	for _, id := range batchIDs {
		batch[id] = true
	}

	// Orphaned processing messages NOT in the current batch are from a prior session.
	processing, err := n.queue.ForAgent(ctx, agentID, QueryOpts{Status: message.StatusProcessing})
	if err != nil {
		logger.Error("next: prompt submit — query stale processing failed", "agent_id", agentID, "err", err)
	}
	// Stale queued messages whose queue_time exceeded the delivery window.
	cutoff := time.Now().Add(-n.deliveryWindow).UnixMilli()
	staleQueued, err := n.queue.ForAgent(ctx, agentID, QueryOpts{Status: statusQueued, StaleBefore: cutoff})
	if err != nil {
		logger.Error("next: prompt submit — query stale queued failed", "agent_id", agentID, "err", err)
	}

	var fail []string
	for _, msg := range processing {
		if !batch[msg.ID] {
			fail = append(fail, msg.ID)
		}
	}
	for _, msg := range staleQueued {
		if !batch[msg.ID] {
			fail = append(fail, msg.ID)
		}
	}
	return SubmitOutcome{Fail: fail, Process: batchIDs}, nil
}

func (n *nextMessage) OnToolSuccess(ctx context.Context, agentID string, messageIDs []string) {
	if len(messageIDs) == 0 {
		logger.Warn("next: mandatory tool produced no message_id, nothing confirmed", "agent_id", agentID)
		return
	}
	for _, id := range messageIDs {
		n.state.ConfirmMessage(agentID, id)
		logger.Debug("next: message confirmed by tool callback", "agent_id", agentID, "message_id", id)
	}
}
