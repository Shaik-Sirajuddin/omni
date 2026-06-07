//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/engine"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Change 1: confirmation moved to OnPostToolUse (not OnPreToolUse) ────────

// TestChange1_PreToolUseAloneDoesNotConfirm verifies that calling OnPreToolUse
// with a delivery tool does NOT confirm the message. OnPreToolUse is now a
// no-op debug log; only OnPostToolUse (tool success) records confirmation.
// With no confirmation, OnStop must inject a recall prompt.
func TestChange1_PreToolUseAloneDoesNotConfirm(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "pre-only-agent", "pre-only-agent", "/ws", "team")

	msg := hookMsg("pre-only-msg", "pre-only-agent", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "head", "pre-only-agent")
	e1 := omni.waitForExec(t)

	proc.OnPreSessionStart("pre-only-agent", "pre-only-agent", "sess-preonly", "/ws")
	proc.OnUserPromptSubmit(ctx, "pre-only-agent", "sess-preonly", e1.prompt)

	// OnPreToolUse fires (tool about to execute) — must NOT confirm delivery.
	proc.OnPreToolUse("pre-only-agent", "sess-preonly", "send_response", map[string]any{"message_id": "pre-only-msg"})

	// OnStop with no OnPostToolUse: tool was not confirmed → must recall.
	recall := proc.OnStop(ctx, "pre-only-agent", "sess-preonly")
	require.NotNil(t, recall, "OnStop must recall when only OnPreToolUse fired — no PostToolUse means no confirmation")
	assert.Contains(t, *recall, "pre-only-msg", "recall prompt must name the unconfirmed message_id")

	st, _ := engine.GetAgentStateForTest(proc, "pre-only-agent")
	assert.Equal(t, engine.AgentStatusRunning, st.Status,
		"agent must stay Running — recall keeps session alive")

	m, err := msgStore.GetMessage(ctx, "pre-only-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusProcessing, m.Status,
		"message must stay Processing — no confirmation was recorded")

	close(e1.relCh)
}

// TestChange1_PostToolUseFailureDoesNotConfirm verifies that a failed tool call
// (OnPostToolUseFailure fires, OnPostToolUse does NOT fire) leaves the message
// unconfirmed — OnStop must still recall.
func TestChange1_PostToolUseFailureDoesNotConfirm(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "fail-tool-agent", "fail-tool-agent", "/ws", "team")

	msg := hookMsg("fail-tool-msg", "fail-tool-agent", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "head", "fail-tool-agent")
	e1 := omni.waitForExec(t)

	proc.OnPreSessionStart("fail-tool-agent", "fail-tool-agent", "sess-failtool", "/ws")
	proc.OnUserPromptSubmit(ctx, "fail-tool-agent", "sess-failtool", e1.prompt)

	// Tool failed — only PreToolUse + PostToolUseFailure fire; PostToolUse does NOT.
	proc.OnPreToolUse("fail-tool-agent", "sess-failtool", "send_response", map[string]any{"message_id": "fail-tool-msg"})
	proc.OnPostToolUseFailure("fail-tool-agent", "sess-failtool", "send_response", "schema validation error")

	// OnStop: no PostToolUse fired → message unconfirmed → recall injected.
	recall := proc.OnStop(ctx, "fail-tool-agent", "sess-failtool")
	require.NotNil(t, recall, "OnStop must recall when send_response failed (PostToolUseFailure, no PostToolUse)")
	assert.Contains(t, *recall, "fail-tool-msg",
		"recall prompt must name the message that was not confirmed due to tool failure")

	m, err := msgStore.GetMessage(ctx, "fail-tool-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusProcessing, m.Status,
		"message must stay Processing — failed tool did not confirm delivery")

	close(e1.relCh)
}

// ─── Change 2: atomic run-claim via BeginRunIfIdle ────────────────────────────

// TestChange2_BeginRunIfIdle_Variants tests BeginRunIfIdle in each key state:
// missing agent, already Running, interrupted, and idle (ready).
func TestChange2_BeginRunIfIdle_Variants(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)

	proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
	engine.StartForTest(proc, ctx)

	t.Run("missing agent returns false", func(t *testing.T) {
		result := engine.BeginRunIfIdleForTest(proc, "no-such-agent")
		assert.False(t, result, "BeginRunIfIdle must return false for unknown agent")
	})

	t.Run("idle agent returns true and becomes Running", func(t *testing.T) {
		engine.RegisterAgentForTest(proc, "idle-agent", "idle-agent", "/ws", "team")
		result := engine.BeginRunIfIdleForTest(proc, "idle-agent")
		assert.True(t, result, "BeginRunIfIdle must return true for an idle (Ready) agent")

		st, _ := engine.GetAgentStateForTest(proc, "idle-agent")
		assert.Equal(t, engine.AgentStatusRunning, st.Status,
			"BeginRunIfIdle must atomically set status to Running when it returns true")
	})

	t.Run("already Running returns false", func(t *testing.T) {
		// "idle-agent" is already Running from the previous subtest.
		result := engine.BeginRunIfIdleForTest(proc, "idle-agent")
		assert.False(t, result, "BeginRunIfIdle must return false when agent is already Running")
	})

	t.Run("interrupted agent returns false", func(t *testing.T) {
		engine.RegisterAgentForTest(proc, "intr-agent", "intr-agent", "/ws", "team")
		proc.Interrupt("intr-agent")
		result := engine.BeginRunIfIdleForTest(proc, "intr-agent")
		assert.False(t, result, "BeginRunIfIdle must return false when agent IsInterrupted=true")
	})
}

// TestChange2_ConcurrentTriggers_OneDispatch verifies that two concurrent
// executeLoop goroutines (from two rapid MessageArrived calls) result in
// exactly ONE ExecInSession call — BeginRunIfIdle collapses the TOCTOU window.
func TestChange2_ConcurrentTriggers_OneDispatch(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "conc2-agent", "conc2-agent", "/ws", "team")

	msg := hookMsg("conc2-msg", "conc2-agent", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	// Fire two concurrent executeLoop triggers for the same idle agent.
	proc.MessageArrived(ctx, "head", "conc2-agent")
	proc.MessageArrived(ctx, "head", "conc2-agent")

	// Exactly ONE ExecInSession must fire — the second goroutine hits BeginRunIfIdle
	// after the first has already set the agent to Running, so it returns early.
	e1 := omni.waitForExec(t)
	assert.Equal(t, "conc2-agent", e1.agentID)

	// No second exec must fire while the first session is still live.
	select {
	case extra := <-omni.execCh:
		close(extra.relCh)
		t.Fatal("second ExecInSession fired — BeginRunIfIdle must prevent double-dispatch for the same agent")
	case <-time.After(80 * time.Millisecond):
	}

	assert.Equal(t, 1, len(omni.execCalls()),
		"exactly one ExecInSession must fire when two goroutines race for a single idle agent")

	close(e1.relCh)
}

// TestChange2_ClaimReleasedWhenNoMessages verifies that when executeLoop claims
// the agent via BeginRunIfIdle but then finds no messages to dispatch (e.g.
// another loop already picked them, or state was cleared), it releases the claim
// back to Ready via the deferred status reset.
func TestChange2_ClaimReleasedWhenNoMessages(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)

	proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "no-msg-agent", "no-msg-agent", "/ws", "team")

	// Trigger executeLoop with no messages in the store.
	// executeLoop will claim the agent (BeginRunIfIdle → Running), find nothing, and defer-release.
	proc.MessageArrived(ctx, "head", "no-msg-agent")

	// Allow the goroutine to complete the no-message path.
	time.Sleep(30 * time.Millisecond)

	st, _ := engine.GetAgentStateForTest(proc, "no-msg-agent")
	assert.Equal(t, engine.AgentStatusReady, st.Status,
		"executeLoop must release the Running claim back to Ready when no messages are found")

	// Confirm the agent is now claimable again (BeginRunIfIdle returns true for Ready agent).
	claimable := engine.BeginRunIfIdleForTest(proc, "no-msg-agent")
	assert.True(t, claimable,
		"after claim release, agent must be claimable again via BeginRunIfIdle")
	// Reset status so subtests don't interfere.
	engine.SetAgentStatusForTest(proc, "no-msg-agent", engine.AgentStatusReady)
}

// ─── Change 3: per-message confirmation scenarios ─────────────────────────────

// recordingReplyService captures SendFailureCallback invocations for assertion.
type recordingReplyService struct {
	mu        sync.Mutex
	failures  []string
	failureCh chan string
}

func newRecordingReplyService() *recordingReplyService {
	return &recordingReplyService{failureCh: make(chan string, 10)}
}

func (r *recordingReplyService) SendReply(_ context.Context, msg *message.Message, _, _ string) error {
	return nil
}

func (r *recordingReplyService) SendFailureCallback(_ context.Context, msg *message.Message, _ string) error {
	r.mu.Lock()
	r.failures = append(r.failures, msg.ID)
	r.mu.Unlock()
	r.failureCh <- msg.ID
	return nil
}

func (r *recordingReplyService) waitForFailure(t *testing.T, msgID string) {
	t.Helper()
	select {
	case id := <-r.failureCh:
		assert.Equal(t, msgID, id, "SendFailureCallback must name the correct message_id")
	case <-time.After(2 * time.Second):
		t.Fatalf("SendFailureCallback for %q not called within timeout", msgID)
	}
}

// TestChange3_PartialConfirm_OneOfTwo verifies the core per-message scenario:
// two messages A and B are in StatusProcessing; the agent confirms only A via
// send_response(message_id=A). On Stop: A must be delivered, B must NOT be
// delivered (still Processing), and the recall prompt must name only B.
func TestChange3_PartialConfirm_OneOfTwo(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "partial-agent", "partial-agent", "/ws", "team")

	// Two query messages from the same sender — engine batches queries from the same sender
	// into one session, so both are StatusProcessing when OnStop fires.
	newQuery := func(id string, sentTime int64) *message.Message {
		return &message.Message{
			ID: id, To: "partial-agent", From: "head",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestType("query"),
			ShouldReply: true, Prompt: "answer this", Refs: "{}",
			Status: message.StatusInQueue, SentTime: sentTime,
		}
	}
	msgA := newQuery("partial-msg-A", 1000)
	msgB := newQuery("partial-msg-B", 2000)
	insertEngineMessages(t, ctx, msgStore, msgA, msgB)

	proc.MessageArrived(ctx, "head", "partial-agent")
	e1 := omni.waitForExec(t)

	// Prompt contains both IDs.
	assert.Contains(t, e1.prompt, "partial-msg-A", "prompt must include msg-A")
	assert.Contains(t, e1.prompt, "partial-msg-B", "prompt must include msg-B")

	proc.OnPreSessionStart("partial-agent", "partial-agent", "sess-partial", "/ws")
	proc.OnUserPromptSubmit(ctx, "partial-agent", "sess-partial", e1.prompt)

	// Confirm only A.
	proc.OnPostToolUse("partial-agent", "sess-partial", "send_response",
		map[string]any{"message_id": "partial-msg-A"})

	// OnStop: A confirmed → delivered; B unconfirmed → recalled.
	recall := proc.OnStop(ctx, "partial-agent", "sess-partial")
	require.NotNil(t, recall, "OnStop must recall because msg-B is unconfirmed")
	assert.Contains(t, *recall, "partial-msg-B", "recall prompt must name only msg-B")
	assert.NotContains(t, *recall, "partial-msg-A", "recall must not mention already-delivered msg-A")

	a, err := msgStore.GetMessage(ctx, "partial-msg-A")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, a.Status, "msg-A must be Delivered (was confirmed)")

	b, err := msgStore.GetMessage(ctx, "partial-msg-B")
	require.NoError(t, err)
	assert.Equal(t, message.StatusProcessing, b.Status, "msg-B must stay Processing (was not confirmed)")
	assert.Equal(t, 2, b.Retries, "msg-B Retries must advance to 2 after first recall")

	// Agent must stay Running (B is still recallable).
	st, _ := engine.GetAgentStateForTest(proc, "partial-agent")
	assert.Equal(t, engine.AgentStatusRunning, st.Status,
		"agent must stay Running while msg-B is still recallable — must not allow concurrent session")

	close(e1.relCh)
}

// TestChange3_BatchConfirm_BothDelivered verifies that send_response_batch
// with a results[] array confirming both messages causes OnStop to deliver
// both and return nil (no recall).
func TestChange3_BatchConfirm_BothDelivered(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "batch-agent", "batch-agent", "/ws", "team")

	// Two query messages from same sender — batched into one session.
	newQ := func(id string, sentTime int64) *message.Message {
		return &message.Message{
			ID: id, To: "batch-agent", From: "head",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestType("query"),
			ShouldReply: false, Prompt: "query", Refs: "{}",
			Status: message.StatusInQueue, SentTime: sentTime,
		}
	}
	msgA := newQ("batch-msg-A", 1000)
	msgB := newQ("batch-msg-B", 2000)
	insertEngineMessages(t, ctx, msgStore, msgA, msgB)

	proc.MessageArrived(ctx, "head", "batch-agent")
	e1 := omni.waitForExec(t)

	proc.OnPreSessionStart("batch-agent", "batch-agent", "sess-batch", "/ws")
	proc.OnUserPromptSubmit(ctx, "batch-agent", "sess-batch", e1.prompt)

	// Confirm both via send_response_batch results[].
	proc.OnPostToolUse("batch-agent", "sess-batch", "send_response_batch", map[string]any{
		"results": []any{
			map[string]any{"message_id": "batch-msg-A", "response": "done A"},
			map[string]any{"message_id": "batch-msg-B", "response": "done B"},
		},
	})

	// OnStop: both confirmed → both delivered, no recall.
	recall := proc.OnStop(ctx, "batch-agent", "sess-batch")
	assert.Nil(t, recall, "OnStop must deliver both and return nil when batch confirms all messages")

	a, err := msgStore.GetMessage(ctx, "batch-msg-A")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, a.Status, "msg-A must be Delivered after batch confirm")

	b, err := msgStore.GetMessage(ctx, "batch-msg-B")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, b.Status, "msg-B must be Delivered after batch confirm")

	st, _ := engine.GetAgentStateForTest(proc, "batch-agent")
	assert.Equal(t, engine.AgentStatusReady, st.Status,
		"agent must be Ready after both messages delivered")

	close(e1.relCh)
}

// TestChange3_MaxRetries_ForceDeliverWithCallback verifies that when a message
// is not confirmed across all recall turns (Retries exhausts maxMandatoryToolRetries),
// OnStop force-delivers it and calls SendFailureCallback on the author if
// msg.ShouldReply=true.
func TestChange3_MaxRetries_ForceDeliverWithCallback(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()
	reply := newRecordingReplyService()

	proc := engine.New(msgStore, engine.WithTestBinary(omni), engine.WithReplyService(reply))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "cb-agent", "cb-agent", "/ws", "team")

	// ShouldReply=true so SendFailureCallback fires on force-delivery.
	msg := &message.Message{
		ID:          "cb-msg",
		To:          "cb-agent",
		From:        "head",
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: message.RequestTypeExecute,
		ShouldReply: true,
		Prompt:      "do it",
		Refs:        "{}",
		Status:      message.StatusInQueue,
		SentTime:    1000,
	}
	require.NoError(t, msgStore.InsertMessage(ctx, msg))

	proc.MessageArrived(ctx, "head", "cb-agent")
	e1 := omni.waitForExec(t)

	proc.OnPreSessionStart("cb-agent", "cb-agent", "sess-cb", "/ws")
	proc.OnUserPromptSubmit(ctx, "cb-agent", "sess-cb", e1.prompt)

	// Retries starts at 1 (executeLoop incremented). Recall each turn without confirming.
	// Turns 1-3: Retries 1→2, 2→3, 3→4 — all still ≤ maxMandatoryToolRetries=3 → recall.
	r1 := proc.OnStop(ctx, "cb-agent", "sess-cb")
	require.NotNil(t, r1, "turn 1 must recall (Retries=1 ≤ 3)")

	r2 := proc.OnStop(ctx, "cb-agent", "sess-cb")
	require.NotNil(t, r2, "turn 2 must recall (Retries=2 ≤ 3)")

	r3 := proc.OnStop(ctx, "cb-agent", "sess-cb")
	require.NotNil(t, r3, "turn 3 must recall (Retries=3 ≤ 3)")

	// Turn 4: Retries=4 > 3 → exhausted → force-deliver + failure callback.
	r4 := proc.OnStop(ctx, "cb-agent", "sess-cb")
	assert.Nil(t, r4, "turn 4 must NOT recall — max retries exhausted, force-deliver instead")

	// SendFailureCallback must fire because ShouldReply=true.
	reply.waitForFailure(t, "cb-msg")

	m, err := msgStore.GetMessage(ctx, "cb-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, m.Status,
		"message must be force-delivered (StatusDelivered) after maxMandatoryToolRetries exhausted")

	st, _ := engine.GetAgentStateForTest(proc, "cb-agent")
	assert.Equal(t, engine.AgentStatusReady, st.Status,
		"agent must be Ready after force-delivery")

	close(e1.relCh)
}

// TestChange3_InstantDeliveredWithoutConfirm verifies that instant (steer)
// messages in StatusProcessing are delivered by OnStop without requiring any
// tool confirmation — they are exempt from the per-message confirmation guard.
func TestChange3_InstantDeliveredWithoutConfirm(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "instant-agent", "instant-agent", "/ws", "team")

	// Instant (steer) message — must be delivered by OnStop without any PostToolUse.
	instant := &message.Message{
		ID:          "instant-msg",
		To:          "instant-agent",
		From:        "head",
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: message.RequestType("instant"),
		ShouldReply: false,
		Prompt:      "steer: stop what you are doing",
		Refs:        "{}",
		Status:      message.StatusInQueue,
		SentTime:    1000,
	}
	require.NoError(t, msgStore.InsertMessage(ctx, instant))

	// The instant message alone won't trigger executeLoop (preprocessing recall only picks
	// non-instant). Insert a regular execute message to drive the session.
	exec := hookMsg("instant-exec-trigger", "instant-agent", "head", 2000)
	insertEngineMessages(t, ctx, msgStore, exec)

	proc.MessageArrived(ctx, "head", "instant-agent")
	e1 := omni.waitForExec(t)

	// Advance the instant message to StatusProcessing manually (simulates it having been
	// bundled in a prior session that missed OnStop — the instant is now orphaned).
	proc.OnPreSessionStart("instant-agent", "instant-agent", "sess-instant", "/ws")
	// Manually force instant to Processing (engine normally does this via UserPromptSubmit YAML parse).
	instant.Status = message.StatusProcessing
	require.NoError(t, msgStore.UpdateMessage(ctx, instant))

	// OnStop: no PostToolUse fired, but instant is exempt from confirmation → delivered.
	// The execute trigger is in statusQueued (not processing), so OnStop only sees the instant.
	recall := proc.OnStop(ctx, "instant-agent", "sess-instant")
	assert.Nil(t, recall, "OnStop must deliver instant message without requiring tool confirmation")

	m, err := msgStore.GetMessage(ctx, "instant-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, m.Status,
		"instant message must be Delivered on Stop with no tool confirmation")

	close(e1.relCh)
}

// TestChange3_MixedTurn_AgentStaysRunning verifies the mixed-turn scenario:
// message A is confirmed (send_response message_id=A) while message B remains
// unconfirmed. On Stop: A is delivered AND the agent stays Running (not Ready)
// because B still needs recall — no concurrent session must be allowed.
func TestChange3_MixedTurn_AgentStaysRunning(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "mixed-turn-agent", "mixed-turn-agent", "/ws", "team")

	// Two query messages from same sender — batched into one session so both are in StatusProcessing.
	newMQ := func(id string, sentTime int64) *message.Message {
		return &message.Message{
			ID: id, To: "mixed-turn-agent", From: "head",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestType("query"),
			ShouldReply: false, Prompt: "q", Refs: "{}",
			Status: message.StatusInQueue, SentTime: sentTime,
		}
	}
	msgA := newMQ("mixed-A", 1000)
	msgB := newMQ("mixed-B", 2000)
	insertEngineMessages(t, ctx, msgStore, msgA, msgB)

	proc.MessageArrived(ctx, "head", "mixed-turn-agent")
	e1 := omni.waitForExec(t)

	proc.OnPreSessionStart("mixed-turn-agent", "mixed-turn-agent", "sess-mixed-turn", "/ws")
	proc.OnUserPromptSubmit(ctx, "mixed-turn-agent", "sess-mixed-turn", e1.prompt)

	// Confirm A only.
	proc.OnPostToolUse("mixed-turn-agent", "sess-mixed-turn", "send_response",
		map[string]any{"message_id": "mixed-A"})

	// OnStop: A delivered + B recalled → agent must stay Running.
	recall := proc.OnStop(ctx, "mixed-turn-agent", "sess-mixed-turn")
	require.NotNil(t, recall, "OnStop must recall for unconfirmed msg-B")

	// A must be delivered.
	a, err := msgStore.GetMessage(ctx, "mixed-A")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, a.Status, "msg-A must be Delivered (was confirmed)")

	// B must still be Processing (not delivered, not failed).
	b, err := msgStore.GetMessage(ctx, "mixed-B")
	require.NoError(t, err)
	assert.Equal(t, message.StatusProcessing, b.Status, "msg-B must stay Processing (unconfirmed)")

	// Agent MUST stay Running — delivering A must not flip the agent to Ready.
	// If it did, the post-loop retry could spawn a concurrent second session while B is still live.
	st, _ := engine.GetAgentStateForTest(proc, "mixed-turn-agent")
	assert.Equal(t, engine.AgentStatusRunning, st.Status,
		"agent must stay Running when B is still recallable — delivering A must not set Ready")

	// No concurrent second ExecInSession must fire while B's recall is in progress.
	select {
	case extra := <-omni.execCh:
		close(extra.relCh)
		t.Fatal("concurrent ExecInSession fired while agent should stay Running for msg-B recall")
	case <-time.After(60 * time.Millisecond):
	}

	close(e1.relCh)
}
