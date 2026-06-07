package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"gopkg.in/yaml.v3"
)

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

	// Parse the payload FIRST so the stale sweeps below can exclude the messages that belong to
	// THIS submit cycle. The ptydaemon submit-key retry can fire UserPromptSubmit twice ~9ms apart
	// carrying the SAME YAML payload: the first hook moves the message to StatusProcessing, and
	// without this exclusion the second hook's stale-processing sweep would see that very message
	// as "orphaned" and fail it — leaving send_response unable to deliver and the session hanging.
	// A message referenced in the payload being submitted is by definition current, not orphaned
	// from a prior session, so it must be protected from both sweeps regardless of hook timing.
	var payload promptPayload
	isEnginePayload := yaml.Unmarshal([]byte(prompt), &payload) == nil
	currentBatch := make(map[string]bool)
	if isEnginePayload {
		for _, item := range payload.Messages {
			if item.MessageID != "" {
				currentBatch[item.MessageID] = true
			}
		}
	}

	// A new prompt means a new session — any messages still in processing are orphaned from a prior session.
	// Safety: this hook fires only on the UserPromptSubmit (PrePrompt) event, which Claude Code emits for
	// user-originated prompts only. systemMessage injections (returned via PostPrompt/Stop hook response) do
	// NOT fire this hook — they continue the same session and trigger another Stop event instead. The sweep
	// is therefore safe on the recall path: recall messages stay StatusProcessing across Stop → systemMessage
	// → Stop turns without being cleared here.
	stale, err := e.queue.ForAgent(ctx, agentID, QueryOpts{Status: message.StatusProcessing})
	if err != nil {
		logger.Error("hook: user prompt submit — query stale processing failed", "agent_id", agentID, "err", err)
	}
	cleared := 0
	for _, msg := range stale {
		if currentBatch[msg.ID] {
			continue // belongs to this submit cycle (double-UPS race) — not orphaned
		}
		msg.Status = message.StatusFailed
		if err := e.queue.Update(ctx, msg); err != nil {
			logger.Error("hook: mark stale processing failed", "message_id", msg.ID, "err", err)
		}
		cleared++
	}
	if cleared > 0 {
		logger.Warn("hook: user prompt submit — cleared stale processing messages", "agent_id", agentID, "count", cleared)
	}

	// Also sweep stale queued messages whose queue_time exceeded the delivery window.
	cutoff := time.Now().Add(-e.deliveryWindow).UnixMilli()
	staleQueued, err := e.queue.ForAgent(ctx, agentID, QueryOpts{Status: statusQueued, StaleBefore: cutoff})
	if err != nil {
		logger.Error("hook: user prompt submit — query stale queued failed", "agent_id", agentID, "err", err)
	}
	clearedQueued := 0
	for _, msg := range staleQueued {
		if currentBatch[msg.ID] {
			continue // belongs to this submit cycle — not orphaned
		}
		msg.Status = message.StatusFailed
		if err := e.queue.Update(ctx, msg); err != nil {
			logger.Error("hook: mark stale queued failed", "message_id", msg.ID, "err", err)
		}
		clearedQueued++
	}
	if clearedQueued > 0 {
		logger.Warn("hook: user prompt submit — cleared stale queued messages", "agent_id", agentID, "count", clearedQueued)
	}

	if !isEnginePayload {
		logger.Debug("hook: user prompt submit — not a engine payload, skipping", "agent_id", agentID)
		return
	}

	for _, item := range payload.Messages {
		if item.MessageID == "" {
			continue
		}
		msg, err := e.queue.Get(ctx, item.MessageID)
		if err != nil {
			logger.Error("hook: get message failed", "message_id", item.MessageID, "err", err)
			continue
		}
		msg.Status = message.StatusProcessing
		if err := e.queue.Update(ctx, msg); err != nil {
			logger.Error("hook: update message to processing failed", "message_id", item.MessageID, "err", err)
		}
	}
	logger.Debug("hook: user prompt submit processed", "agent_id", agentID, "session_id", sessionID, "count", len(payload.Messages))
}

// maxMandatoryToolRetries is the number of recall injections before giving up.
// Compared against msg.Retries which executeLoop increments (to 1) before the
// first ExecInSession, so the guard uses <= to get exactly 3 recall attempts.
const maxMandatoryToolRetries = 3

// buildRecallPrompt builds a minimal recall systemMessage that includes the message ID(s)
// so the agent can call the correct tool without re-reading context.
func buildRecallPrompt(msgs []*message.Message) string {
	if len(msgs) == 0 {
		return "Still waiting for your response. Use the `send_response` tool to reply before finishing."
	}
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	if len(msgs) == 1 {
		return fmt.Sprintf("Still waiting for your response to message %s. Use the `send_response` tool for this message (message_id=%s) to reply — or `send_response_batch` with a single-item array. Do not use send_message to reply.", ids[0], ids[0])
	}
	return fmt.Sprintf("Still waiting for your response to %d messages. Use the `send_response` tool for each message_id, or `send_response_batch` for all of them: [%s]. Do not use send_message to reply.", len(msgs), joinIDs(ids))
}

func joinIDs(ids []string) string {
	result := ""
	for i, id := range ids {
		if i > 0 {
			result += ", "
		}
		result += id
	}
	return result
}

// OnStop is called by HookHandler on Stop / PostPrompt events.
// Returns a non-nil recall prompt string when the mandatory tool was not invoked
// and retries remain, so the handler can inject it as a systemMessage.
//
// The HTTP request context (r.Context()) is intentionally discarded in favour of the
// engine lifetime context (e.ctx) so that DB writes are never cancelled by a short-lived
// hook request timeout.
func (e *ProcessingEngine) OnStop(_ context.Context, agentID, sessionID string) *string {
	logger.Debug("hook: stop", "agent_id", agentID, "session_id", sessionID)

	ctx := e.ctx
	agentState, _ := e.state.GetAgent(agentID)

	msgs, err := e.queue.ForAgent(ctx, agentID, QueryOpts{Status: message.StatusProcessing})
	if err != nil {
		logger.Error("hook: stop — query processing messages failed", "agent_id", agentID, "err", err)
		e.state.ClearSession(sessionID)
		e.state.SignalSessionDone(agentID)
		return nil
	}

	if agentState.CodeSession.IsInterrupted {
		logger.Info("hook: stop — interrupted, resetting messages to in_queue", "agent_id", agentID, "count", len(msgs))
		for _, msg := range msgs {
			msg.Status = message.StatusInQueue
			if err := e.queue.Update(ctx, msg); err != nil {
				logger.Error("hook: reset message failed", "message_id", msg.ID, "err", err)
			}
		}
		agentState.CodeSession.IsInterrupted = false
		agentState.Status = AgentStatusReady
		e.state.SetAgent(agentID, agentState)
		e.state.ClearConfirmed(agentID)
		e.state.ClearSession(sessionID)
		e.state.SignalSessionDone(agentID)
		return nil
	}

	// Per-message delivery confirmation. The single source of truth for "delivered" is the
	// agent's tool callback (send_response / batch / legacy alias) naming a specific
	// message_id this session — recorded in OnPostToolUse. A bare Stop turn confirms nothing;
	// unconfirmed execute/query messages ride the recall loop until their own retry counter
	// is exhausted, then force-deliver with a failure callback. Instant (steer) messages need
	// no confirmation and are always delivered.
	var confirmed, unconfirmed []*message.Message
	for _, msg := range msgs {
		if msg.RequestType == reqTypeInstant || e.state.IsMessageConfirmed(agentID, msg.ID) {
			confirmed = append(confirmed, msg)
		} else {
			unconfirmed = append(unconfirmed, msg)
		}
	}

	// Deliver confirmed messages via the normal reply path.
	if len(confirmed) > 0 {
		e.markDelivered(ctx, confirmed, agentID, agentState, false)
		agentState, _ = e.state.GetAgent(agentID) // markDelivered bumped generation / set Ready
	}

	// Split unconfirmed into still-retriable vs exhausted (msg.Retries is per-message, in DB).
	var recallable, exhausted []*message.Message
	for _, msg := range unconfirmed {
		if msg.Retries <= maxMandatoryToolRetries {
			recallable = append(recallable, msg)
		} else {
			exhausted = append(exhausted, msg)
		}
	}

	// Exhausted unconfirmed messages: force-deliver + failure callback to the author.
	if len(exhausted) > 0 {
		logger.Warn("hook: stop — recall retries exhausted, force-delivering",
			"agent_id", agentID, "count", len(exhausted))
		// Bulk status callback for all exhausted messages in one send.
		if e.statusCallback != nil {
			ids := make([]string, len(exhausted))
			for i, msg := range exhausted {
				ids[i] = msg.ID
			}
			e.statusCallback.SendStatusCallbackBatch(ctx, ids, agentState.Agent.Name, agentState.Agent.Team)
		}
		e.markDelivered(ctx, exhausted, agentID, agentState, true)
		agentState, _ = e.state.GetAgent(agentID)
	}

	// Still-retriable unconfirmed messages: inject a recall and keep the SAME session running.
	if len(recallable) > 0 {
		// Increment each message's counter before injecting recall so the per-message guard
		// advances every turn; without this the recall would loop forever.
		for _, msg := range recallable {
			msg.Retries++
			if err := e.queue.Update(ctx, msg); err != nil {
				logger.Error("hook: stop — recall retries increment failed", "message_id", msg.ID, "err", err)
			}
		}
		recall := buildRecallPrompt(recallable)
		logger.Warn("hook: stop — mandatory tool not invoked, injecting recall",
			"agent_id", agentID, "count", len(recallable))
		// Keep the agent Running for the continued session. Even if we delivered confirmed/
		// exhausted messages above (markDelivered set Ready), returning while Ready would let
		// the watchdog or post-loop retry start a concurrent second session. Do NOT clear the
		// session or signal done — the agent gets another Stop turn.
		cur, _ := e.state.GetAgent(agentID)
		cur.Status = AgentStatusRunning
		e.state.SetAgent(agentID, cur)
		return &recall
	}

	// Nothing left pending — settle the session.
	e.state.ClearConfirmed(agentID)
	e.state.ClearSession(sessionID)
	e.state.SignalSessionDone(agentID)
	return nil
}

// markDelivered marks msgs as delivered and sends replies for non-query types.
// markDelivered marks msgs as delivered. isForceDeliver=true means the agent exhausted
// retries without calling send_response — a failure callback is sent to the author for
// any message with ShouldReply=true.
func (e *ProcessingEngine) markDelivered(ctx context.Context, msgs []*message.Message, agentID string, agentState AgentState, isForceDeliver bool) {
	logger.Info("hook: stop — marking messages delivered", "agent_id", agentID, "count", len(msgs), "force", isForceDeliver)
	now := time.Now().UnixMilli()
	for _, msg := range msgs {
		msg.Status = message.StatusDelivered
		msg.DeliveryTime = &now
		if err := e.queue.Update(ctx, msg); err != nil {
			logger.Error("hook: deliver message failed", "message_id", msg.ID, "err", err)
			continue
		}
		// T5: complete delivery checkpoint for execute messages.
		if e.taskDelivery != nil && msg.RequestType == reqTypeExecute && msg.TaskID != "" {
			if err := e.taskDelivery.CompleteDelivery(ctx, msg.TaskID, agentID); err != nil {
				logger.Warn("hook: task delivery complete checkpoint failed", "task_id", msg.TaskID, "err", err)
			}
		}
		if e.reply != nil {
			if isForceDeliver && msg.ShouldReply {
				// Agent never called send_response — notify the author that delivery failed.
				if err := e.reply.SendFailureCallback(ctx, msg, agentID); err != nil {
					logger.Error("hook: send failure callback failed", "message_id", msg.ID, "err", err)
				}
			} else if msg.RequestType != reqTypeQuery {
				if err := e.reply.SendReply(ctx, msg, agentID, agentState.Agent.Name); err != nil {
					logger.Error("hook: send reply failed", "message_id", msg.ID, "err", err)
				}
			}
		}
	}
	// Increment generation so onSessionEnd (called after ExecInSession returns) can detect
	// that delivery already happened and must not reset a concurrently-started session.
	// Do NOT spawn a new executeLoop here: markDelivered runs inside OnStop, which is a
	// hook fired while ExecInSession is still blocking. Spawning an executeLoop now would
	// pick unrelated messages (e.g. from a different task/sender) while the current session
	// is still active. The post-loop retry at the end of executeLoop delivers the next batch
	// after ExecInSession returns.
	agentState.CodeSession.SessionGeneration++
	agentState.Status = AgentStatusReady
	e.state.SetAgent(agentID, agentState)
}

// mandatoryToolNames is the set of axolink tool names that confirm delivery.
// send_response / send_response_batch are the canonical names (T7).
// Legacy names kept as aliases until all agents migrate.
var mandatoryToolNames = map[string]bool{
	"send_response":       true, // canonical
	"send_response_batch": true, // canonical batch
	"update_message":      true, // legacy execute alias
	"update_messages":     true, // legacy execute batch alias
	"query_result":        true, // legacy query alias
	"query_result_batch":  true, // legacy query batch alias
}

// OnPreToolUse is called by HookHandler on PreToolUse events.
// Delivery confirmation is intentionally NOT set here: PreToolUse fires before the
// tool executes, so a mandatory tool that subsequently errors (e.g. send_response
// rejected for an already-failed/delivered message, or a schema failure) would
// falsely confirm delivery and suppress the OnStop recall. The flag is set in
// OnPostToolUse, which fires only after the tool succeeds.
func (e *ProcessingEngine) OnPreToolUse(agentID, sessionID, toolName string, _ map[string]any) {
	logger.Debug("hook: pre tool use", "agent_id", agentID, "session_id", sessionID, "tool_name", toolName)
}

// OnPostToolUse is called by HookHandler on PostToolUse events (tool succeeded).
// When a delivery-confirming tool completes successfully, it records per-message
// confirmation for every message_id named in the tool input. OnStop then delivers
// only the confirmed messages and recalls the rest — confirmation is per-message,
// not session-wide, so answering message A no longer settles an untouched message B.
func (e *ProcessingEngine) OnPostToolUse(agentID, sessionID, toolName string, toolInput map[string]any) {
	logger.Debug("hook: post tool use", "agent_id", agentID, "session_id", sessionID, "tool_name", toolName)
	if !mandatoryToolNames[toolName] {
		return
	}
	ids := messageIDsFromToolInput(toolInput)
	for _, id := range ids {
		e.state.ConfirmMessage(agentID, id)
		logger.Debug("hook: message confirmed by tool callback", "agent_id", agentID, "tool_name", toolName, "message_id", id)
	}
	if len(ids) == 0 {
		logger.Warn("hook: mandatory tool produced no message_id, nothing confirmed", "agent_id", agentID, "tool_name", toolName)
	}
}

// messageIDsFromToolInput extracts confirmed message_ids from a delivery-tool input.
// Single tools (send_response, query_result, update_message) carry a top-level
// "message_id"; batch tools (send_response_batch, ...) carry a "results" array of
// {message_id, response} objects.
func messageIDsFromToolInput(toolInput map[string]any) []string {
	if toolInput == nil {
		return nil
	}
	var ids []string
	if v, ok := toolInput["message_id"].(string); ok && v != "" {
		ids = append(ids, v)
	}
	if results, ok := toolInput["results"].([]any); ok {
		for _, item := range results {
			if m, ok := item.(map[string]any); ok {
				if v, ok := m["message_id"].(string); ok && v != "" {
					ids = append(ids, v)
				}
			}
		}
	}
	return ids
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
