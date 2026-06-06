//go:build integration

package engine_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/engine"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// hookMsg creates an execute message with StatusInQueue, ready for delivery.
func hookMsg(id, to, from string, sentTime int64) *message.Message {
	return &message.Message{
		ID:          id,
		To:          to,
		From:        from,
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: message.RequestTypeExecute,
		ShouldReply: false,
		Prompt:      "execute this task",
		Refs:        "{}",
		Status:      message.StatusInQueue,
		SentTime:    sentTime,
	}
}

// insertHookMsg inserts a message and optionally advances its status.
func insertHookMsg(t *testing.T, ctx context.Context, store message.MessageStore, msg *message.Message, overrideStatus *message.Status) {
	t.Helper()
	require.NoError(t, store.InsertMessage(ctx, msg))
	if overrideStatus != nil && *overrideStatus != message.StatusInQueue {
		msg.Status = *overrideStatus
		require.NoError(t, store.UpdateMessage(ctx, msg))
	}
}

// yamlPromptForMsg builds a minimal YAML engine prompt so OnUserPromptSubmit advances
// the message to StatusProcessing.
func yamlPromptForMsg(msgID string) string {
	return fmt.Sprintf("messages:\n  - message_id: %s\n    prompt: execute this task\ninstruction: Execute the task.", msgID)
}

// waitForStatus polls until the message reaches wantStatus or the timeout expires.
func waitForStatus(t *testing.T, ctx context.Context, store message.MessageStore, msgID string, wantStatus message.Status, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m, err := store.GetMessage(ctx, msgID)
		if err == nil && m.Status == wantStatus {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	m, err := store.GetMessage(ctx, msgID)
	require.NoError(t, err)
	assert.Equal(t, wantStatus, m.Status, "message %s did not reach status %s within %s", msgID, wantStatus, timeout)
}

// ─── D2 Watchdog tests ────────────────────────────────────────────────────────

// TestD2_WatchdogHookNotFired verifies that when ExecInSession starts but
// OnPreSessionStart never fires, the watchdog resets the agent to Ready.
func TestD2_WatchdogHookNotFired(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore,
		engine.WithTestBinary(omni),
		engine.WithHookFireTimeout(60*time.Millisecond),
	)
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "wd-no-hook", "wd-no-hook", "/ws", "team")

	msg := hookMsg("wd-no-hook-msg", "wd-no-hook", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "head", "wd-no-hook")

	// ExecInSession starts — status transitions to Running.
	e1 := omni.waitForExec(t)
	assert.Equal(t, "wd-no-hook", e1.agentID)

	st, _ := engine.GetAgentStateForTest(proc, "wd-no-hook")
	assert.Equal(t, engine.AgentStatusRunning, st.Status, "agent must be Running while ExecInSession blocks")

	// No OnPreSessionStart fired — wait for watchdog to trigger (hookFireTimeout + margin).
	time.Sleep(150 * time.Millisecond)

	st, _ = engine.GetAgentStateForTest(proc, "wd-no-hook")
	assert.Equal(t, engine.AgentStatusReady, st.Status,
		"watchdog must reset agent to Ready when OnPreSessionStart never fires")

	// Unblock the first exec so the goroutine doesn't leak after the test ends.
	close(e1.relCh)
}

// TestD2_WatchdogNoOpWhenHookFired verifies that when OnPreSessionStart fires
// (changing the SessionID), the watchdog does NOT reset the agent.
func TestD2_WatchdogNoOpWhenHookFired(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore,
		engine.WithTestBinary(omni),
		engine.WithHookFireTimeout(60*time.Millisecond),
	)
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "wd-hook-fired", "wd-hook-fired", "/ws", "team")

	msg := hookMsg("wd-hook-msg", "wd-hook-fired", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "head", "wd-hook-fired")
	e1 := omni.waitForExec(t)

	// Fire OnPreSessionStart — sets a new SessionID so the watchdog guard fails.
	proc.OnPreSessionStart("wd-hook-fired", "wd-hook-fired", "sess-wd-1", "/ws")

	// Wait past hookFireTimeout — watchdog goroutine runs but must be a no-op.
	time.Sleep(150 * time.Millisecond)

	st, _ := engine.GetAgentStateForTest(proc, "wd-hook-fired")
	assert.Equal(t, engine.AgentStatusRunning, st.Status,
		"watchdog must NOT reset status when OnPreSessionStart fired (SessionID changed)")

	close(e1.relCh)
}

// ─── D4 OnStop tests ─────────────────────────────────────────────────────────

// TestD4_OnStop_RecallKeepsRunning verifies that when OnStop fires but the
// mandatory tool was not invoked, the engine returns a recall prompt and keeps
// AgentStatusRunning (preventing concurrent session spawning).
func TestD4_OnStop_RecallKeepsRunning(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "onstop-recall", "onstop-recall", "/ws", "team")

	msg := hookMsg("onstop-recall-msg", "onstop-recall", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "head", "onstop-recall")
	e1 := omni.waitForExec(t)

	// Simulate the hook sequence: PreSessionStart → UserPromptSubmit → Stop (no tool called).
	proc.OnPreSessionStart("onstop-recall", "onstop-recall", "sess-recall-1", "/ws")
	proc.OnUserPromptSubmit(ctx, "onstop-recall", "sess-recall-1", e1.prompt)

	// OnStop — mandatory tool NOT invoked. Retries=1 ≤ maxMandatoryToolRetries(3).
	recall := proc.OnStop(ctx, "onstop-recall", "sess-recall-1")

	require.NotNil(t, recall, "OnStop must return a recall prompt when mandatory tool not invoked")
	assert.Contains(t, *recall, "onstop-recall-msg", "recall prompt must include the message ID")

	st, _ := engine.GetAgentStateForTest(proc, "onstop-recall")
	assert.Equal(t, engine.AgentStatusRunning, st.Status,
		"agent status must stay Running during recall — no concurrent session must be allowed")

	// Message must still be StatusProcessing (not delivered, not failed).
	fresh, err := msgStore.GetMessage(ctx, "onstop-recall-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusProcessing, fresh.Status,
		"message must stay StatusProcessing during recall")

	close(e1.relCh)
}

// TestD4_OnStop_DeliversWhenToolInvoked verifies that when send_response fires
// (PreToolUse with a delivery tool), OnStop marks the message delivered and
// transitions the agent to Ready.
func TestD4_OnStop_DeliversWhenToolInvoked(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "onstop-deliver", "onstop-deliver", "/ws", "team")

	msg := hookMsg("onstop-deliver-msg", "onstop-deliver", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "head", "onstop-deliver")
	e1 := omni.waitForExec(t)

	// Simulate: PreSessionStart → UserPromptSubmit → PreToolUse(send_response) → Stop.
	proc.OnPreSessionStart("onstop-deliver", "onstop-deliver", "sess-deliver-1", "/ws")
	proc.OnUserPromptSubmit(ctx, "onstop-deliver", "sess-deliver-1", e1.prompt)
	proc.OnPreToolUse("onstop-deliver", "sess-deliver-1", "send_response", map[string]any{"message_id": "onstop-deliver-msg"})

	recall := proc.OnStop(ctx, "onstop-deliver", "sess-deliver-1")
	assert.Nil(t, recall, "OnStop must return nil when mandatory tool was invoked")

	// Message must be delivered.
	fresh, err := msgStore.GetMessage(ctx, "onstop-deliver-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, fresh.Status,
		"message must be StatusDelivered when send_response was called before OnStop")

	// Agent must be Ready (generation incremented in markDelivered).
	st, _ := engine.GetAgentStateForTest(proc, "onstop-deliver")
	assert.Equal(t, engine.AgentStatusReady, st.Status,
		"agent must be Ready after successful delivery")

	close(e1.relCh)
}

// TestD4_OnStop_MaxRetriesDelivers verifies that after exceeding maxMandatoryToolRetries,
// OnStop delivers the message (marks failed/delivered) instead of injecting another recall.
func TestD4_OnStop_MaxRetriesDelivers(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "onstop-maxretry", "onstop-maxretry", "/ws", "team")

	msg := hookMsg("onstop-maxretry-msg", "onstop-maxretry", "head", 1000)
	// Pre-seed Retries=4 (> maxMandatoryToolRetries=3) so OnStop falls through to deliver.
	require.NoError(t, msgStore.InsertMessage(ctx, msg))
	msg.Status = message.StatusInQueue
	msg.Retries = 4
	require.NoError(t, msgStore.UpdateMessage(ctx, msg))

	proc.MessageArrived(ctx, "head", "onstop-maxretry")
	e1 := omni.waitForExec(t)

	proc.OnPreSessionStart("onstop-maxretry", "onstop-maxretry", "sess-maxretry", "/ws")
	proc.OnUserPromptSubmit(ctx, "onstop-maxretry", "sess-maxretry", e1.prompt)

	// OnStop — mandatory tool NOT invoked, but Retries(5 after executeLoop++) > maxMandatoryToolRetries(3).
	recall := proc.OnStop(ctx, "onstop-maxretry", "sess-maxretry")
	assert.Nil(t, recall, "OnStop must return nil after max retries exceeded (deliver instead)")

	fresh, err := msgStore.GetMessage(ctx, "onstop-maxretry-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, fresh.Status,
		"message must be force-delivered after max recall retries exceeded")

	close(e1.relCh)
}

// ─── D5 Preprocessing recall tests ───────────────────────────────────────────

// TestD5_PreprocessingRecall_PicksProcessingMsg verifies that when executeLoop finds
// a StatusProcessing non-instant message from a prior missed session, it builds a
// YAML recall prompt (buildWarmUpPrompt) and calls ExecInSession with it — without
// going through the normal queue-mark or T3 guard paths.
func TestD5_PreprocessingRecall_PicksProcessingMsg(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "prep-recall", "prep-recall", "/ws", "team")

	// Insert a query message already in StatusProcessing — simulating a prior missed session.
	msg := &message.Message{
		ID:          "prep-recall-msg",
		To:          "prep-recall",
		From:        "head",
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: message.RequestType("query"),
		ShouldReply: true,
		Prompt:      "answer this query",
		Refs:        "{}",
		Status:      message.StatusInQueue,
		SentTime:    1000,
	}
	statusProcessing := message.StatusProcessing
	insertHookMsg(t, ctx, msgStore, msg, &statusProcessing)

	// MessageArrived triggers executeLoop — preprocessing recall path fires.
	proc.MessageArrived(ctx, "head", "prep-recall")

	e1 := omni.waitForExec(t)

	// The prompt must be a YAML recall (buildWarmUpPrompt) containing the message ID.
	assert.Contains(t, e1.prompt, "prep-recall-msg",
		"preprocessing recall prompt must include the StatusProcessing message ID")
	assert.Contains(t, e1.prompt, "send_response",
		"preprocessing recall prompt must instruct the agent to call send_response")
	assert.Contains(t, e1.prompt, "query",
		"preprocessing recall prompt must include the request_type")

	close(e1.relCh)
}

// TestD5_PreprocessingRecall_InstantExempt verifies that instant (steer) messages
// in StatusProcessing do NOT trigger the preprocessing recall path — they don't
// require send_response and should not block the normal pick flow.
func TestD5_PreprocessingRecall_InstantExempt(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "prep-instant", "prep-instant", "/ws", "team")

	// Insert an instant message in StatusProcessing — must NOT trigger preprocessing recall.
	instant := &message.Message{
		ID:          "prep-instant-stale",
		To:          "prep-instant",
		From:        "head",
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: message.RequestType("instant"),
		ShouldReply: false,
		Prompt:      "steer message",
		Refs:        "{}",
		Status:      message.StatusInQueue,
		SentTime:    500,
	}
	statusProcessing := message.StatusProcessing
	insertHookMsg(t, ctx, msgStore, instant, &statusProcessing)

	// Also insert a normal InQueue execute message — should be picked via normal path.
	execMsg := hookMsg("prep-instant-exec", "prep-instant", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, execMsg)

	proc.MessageArrived(ctx, "head", "prep-instant")
	e1 := omni.waitForExec(t)

	// The exec should have been picked via the normal warm-up path (not preprocessing recall).
	// Its prompt will contain the execute message ID, not the instant stale message.
	assert.Contains(t, e1.prompt, "prep-instant-exec",
		"normal pick must deliver the execute message when only instant messages are processing")
	assert.NotContains(t, e1.prompt, "prep-instant-stale",
		"stale instant message must not appear in the normal warm-up prompt")

	close(e1.relCh)
}

// ─── D1 SessionGeneration integration test ───────────────────────────────────

// TestD1_SessionGenerationGuard verifies the D1 fix end-to-end: when markDelivered
// fires (via OnStop) during a session and increments the generation, the subsequent
// onSessionEnd call from the original executeLoop does NOT reset the new session's
// Running status to Ready.
func TestD1_SessionGenerationGuard(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "d1-guard", "d1-guard", "/ws", "team")

	// Two messages: first triggers a session, second arrives while first is in flight.
	msg1 := hookMsg("d1-msg1", "d1-guard", "head", 1000)
	msg2 := hookMsg("d1-msg2", "d1-guard", "head", 2000)
	insertEngineMessages(t, ctx, msgStore, msg1, msg2)

	proc.MessageArrived(ctx, "head", "d1-guard")
	e1 := omni.waitForExec(t)

	// Fire full hook sequence for msg1 — markDelivered increments generation.
	proc.OnPreSessionStart("d1-guard", "d1-guard", "sess-d1-1", "/ws")
	proc.OnUserPromptSubmit(ctx, "d1-guard", "sess-d1-1", e1.prompt)
	proc.OnPreToolUse("d1-guard", "sess-d1-1", "send_response", nil)
	recall := proc.OnStop(ctx, "d1-guard", "sess-d1-1")
	assert.Nil(t, recall, "msg1 must be delivered cleanly")

	// msg1 delivered; agent is now Ready, generation incremented.
	// Release the first exec — onSessionEnd runs for the original goroutine.
	// It must NOT reset Running→Ready because generation advanced (markDelivered ran first).
	// msg2 is still InQueue; post-loop retry triggers executeLoop for msg2.
	close(e1.relCh)

	// Give the post-loop retry time to fire executeLoop for msg2.
	e2 := omni.waitForExec(t)
	assert.Equal(t, "d1-guard", e2.agentID, "second ExecInSession must be called for msg2")

	close(e2.relCh)

	// Allow onSessionEnd for the second exec to settle.
	time.Sleep(30 * time.Millisecond)

	// msg1 must be delivered.
	fresh1, err := msgStore.GetMessage(ctx, "d1-msg1")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, fresh1.Status, "msg1 must be delivered")
}
