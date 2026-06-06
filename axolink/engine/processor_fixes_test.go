//go:build integration

package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/engine"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/database"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Full recall cycle: no send_response → max retries → force-delivered ─────

// TestFullRecallCycle_MaxRetries simulates an agent that never calls send_response
// across multiple consecutive prompt turns. The engine must inject a recall prompt
// each turn (up to maxMandatoryToolRetries=3) and then force-deliver on the fourth
// OnStop. The message status must be StatusDelivered at the end.
//
// Natural flow from Retries=0:
//   executeLoop picks msg → Retries 0→1 (markMessagesQueued)
//   OnUserPromptSubmit → StatusProcessing
//   OnStop turn 1 (no tool): Retries=1 ≤ 3 → recall injected, Retries 1→2
//   OnStop turn 2 (no tool): Retries=2 ≤ 3 → recall injected, Retries 2→3
//   OnStop turn 3 (no tool): Retries=3 ≤ 3 → recall injected, Retries 3→4
//   OnStop turn 4 (no tool): Retries=4 > 3 → force-delivered
func TestFullRecallCycle_MaxRetries(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "recall-cycle", "recall-cycle", "/ws", "team")

	msg := hookMsg("recall-cycle-msg", "recall-cycle", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "recall-cycle", "recall-cycle")
	e1 := omni.waitForExec(t)

	// Verify initial Retries=1 (executeLoop incremented via markMessagesQueued).
	m, err := msgStore.GetMessage(ctx, "recall-cycle-msg")
	require.NoError(t, err)
	assert.Equal(t, 1, m.Retries, "executeLoop must set Retries=1 before first ExecInSession")

	proc.OnPreSessionStart("recall-cycle", "recall-cycle", "sess-rc", "/ws")
	proc.OnUserPromptSubmit(ctx, "recall-cycle", "sess-rc", e1.prompt)

	// Turn 1: no send_response → recall injected, Retries 1→2.
	r1 := proc.OnStop(ctx, "recall-cycle", "sess-rc")
	require.NotNil(t, r1, "turn 1: OnStop must return recall (Retries=1 ≤ 3)")
	m, _ = msgStore.GetMessage(ctx, "recall-cycle-msg")
	assert.Equal(t, 2, m.Retries, "turn 1: Retries must advance to 2")
	assert.Equal(t, message.StatusProcessing, m.Status, "message must stay Processing during recall")

	// Turn 2: no send_response → recall injected, Retries 2→3.
	r2 := proc.OnStop(ctx, "recall-cycle", "sess-rc")
	require.NotNil(t, r2, "turn 2: OnStop must return recall (Retries=2 ≤ 3)")
	m, _ = msgStore.GetMessage(ctx, "recall-cycle-msg")
	assert.Equal(t, 3, m.Retries, "turn 2: Retries must advance to 3")
	assert.Equal(t, message.StatusProcessing, m.Status)

	// Turn 3: no send_response → recall injected, Retries 3→4.
	r3 := proc.OnStop(ctx, "recall-cycle", "sess-rc")
	require.NotNil(t, r3, "turn 3: OnStop must return recall (Retries=3 ≤ 3)")
	m, _ = msgStore.GetMessage(ctx, "recall-cycle-msg")
	assert.Equal(t, 4, m.Retries, "turn 3: Retries must advance to 4")
	assert.Equal(t, message.StatusProcessing, m.Status)

	// Turn 4: Retries=4 > maxMandatoryToolRetries=3 → force-delivered, no recall.
	r4 := proc.OnStop(ctx, "recall-cycle", "sess-rc")
	assert.Nil(t, r4, "turn 4: OnStop must NOT return recall — max retries exceeded")

	m, err = msgStore.GetMessage(ctx, "recall-cycle-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, m.Status,
		"message must be force-delivered after maxMandatoryToolRetries exceeded")
	assert.NotNil(t, m.DeliveryTime, "DeliveryTime must be set on force-delivery")

	// Agent must be Ready after force-delivery.
	st, _ := engine.GetAgentStateForTest(proc, "recall-cycle")
	assert.Equal(t, engine.AgentStatusReady, st.Status,
		"agent must be Ready after force-delivery")

	close(e1.relCh)
}

// TestRecallCycle_PendingMessagesWaitUntilResolved verifies that messages arriving
// during the recall cycle (while the agent is Running and the mandatory tool is not
// being invoked) are held and only dispatched after the recall cycle fully resolves
// (either via send_response or force-delivery). Status stays Running throughout,
// preventing any concurrent session from picking up the waiting messages early.
func TestRecallCycle_PendingMessagesWaitUntilResolved(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "rc-wait", "rc-wait", "/ws", "team")

	// msg1: the task being recalled.
	msg1 := hookMsg("rc-wait-msg1", "rc-wait", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg1)

	proc.MessageArrived(ctx, "rc-wait", "rc-wait")
	e1 := omni.waitForExec(t)

	proc.OnPreSessionStart("rc-wait", "rc-wait", "sess-rcw", "/ws")
	proc.OnUserPromptSubmit(ctx, "rc-wait", "sess-rcw", e1.prompt)

	// Turn 1 recall — status stays Running.
	r1 := proc.OnStop(ctx, "rc-wait", "sess-rcw")
	require.NotNil(t, r1, "turn 1 must recall")

	// msg2 arrives during the recall — MessageArrived spawns executeLoop.
	// Because status is still Running, that loop must return early.
	msg2 := hookMsg("rc-wait-msg2", "rc-wait", "head", 2000)
	insertEngineMessages(t, ctx, msgStore, msg2)
	proc.MessageArrived(ctx, "rc-wait", "rc-wait")

	st, _ := engine.GetAgentStateForTest(proc, "rc-wait")
	assert.Equal(t, engine.AgentStatusRunning, st.Status,
		"agent must stay Running during recall — msg2's executeLoop must exit early")

	// No second ExecInSession must fire while the recall session is active.
	select {
	case spurious := <-omni.execCh:
		close(spurious.relCh)
		t.Fatalf("unexpected ExecInSession for %q during recall cycle — status must block new sessions", spurious.agentID)
	case <-time.After(60 * time.Millisecond):
	}

	// Turn 2 recall — still running, msg2 still waiting.
	r2 := proc.OnStop(ctx, "rc-wait", "sess-rcw")
	require.NotNil(t, r2, "turn 2 must recall")

	select {
	case spurious := <-omni.execCh:
		close(spurious.relCh)
		t.Fatalf("unexpected ExecInSession for %q on turn 2 — msg2 must still be held", spurious.agentID)
	case <-time.After(60 * time.Millisecond):
	}

	// Turn 3 recall.
	r3 := proc.OnStop(ctx, "rc-wait", "sess-rcw")
	require.NotNil(t, r3, "turn 3 must recall")

	// Turn 4: force-delivered — agent becomes Ready, post-loop retry picks msg2.
	r4 := proc.OnStop(ctx, "rc-wait", "sess-rcw")
	assert.Nil(t, r4, "turn 4 must force-deliver (max retries exceeded)")

	m1, err := msgStore.GetMessage(ctx, "rc-wait-msg1")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, m1.Status, "msg1 must be force-delivered")

	// Release e1 → post-loop retry fires → picks msg2.
	close(e1.relCh)

	e2 := omni.waitForExec(t)
	assert.Equal(t, "rc-wait", e2.agentID, "msg2 must be dispatched after recall cycle resolves")
	close(e2.relCh)

	time.Sleep(20 * time.Millisecond)
	m2, err := msgStore.GetMessage(ctx, "rc-wait-msg2")
	require.NoError(t, err)
	assert.NotEqual(t, message.StatusInQueue, m2.Status,
		"msg2 must have been picked after the recall cycle resolved")
}

// ─── Fix 1: queue_time reset does NOT revert StatusDelivered ─────────────────

// TestFix1_QueueTimeResetPreservesDelivered verifies that the post-ExecInSession
// queue_time reset skips messages already in StatusDelivered (set by OnStop/markDelivered
// during the session) and does not revert them back to statusQueued.
func TestFix1_QueueTimeResetPreservesDelivered(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "fix1-agent", "fix1-agent", "/ws", "team")

	msg := hookMsg("fix1-msg", "fix1-agent", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "head", "fix1-agent")
	e1 := omni.waitForExec(t)

	// Full hook sequence: PreSessionStart → UserPromptSubmit → PreToolUse → OnStop.
	// markDelivered sets StatusDelivered during the session (while ExecInSession still blocks).
	proc.OnPreSessionStart("fix1-agent", "fix1-agent", "sess-fix1", "/ws")
	proc.OnUserPromptSubmit(ctx, "fix1-agent", "sess-fix1", e1.prompt)
	proc.OnPostToolUse("fix1-agent", "sess-fix1", "send_response", map[string]any{"message_id": "fix1-msg"})
	recall := proc.OnStop(ctx, "fix1-agent", "sess-fix1")
	require.Nil(t, recall, "OnStop must deliver (no recall) when tool was invoked")

	// Message is now StatusDelivered — verified before releasing exec.
	delivered, err := msgStore.GetMessage(ctx, "fix1-msg")
	require.NoError(t, err)
	require.Equal(t, message.StatusDelivered, delivered.Status, "message must be Delivered after OnStop/markDelivered")

	// Release exec → ExecInSession returns → onSessionEnd runs → queue_time reset loop runs.
	// The reset loop re-fetches the message; since status != statusQueued it must SKIP it.
	close(e1.relCh)

	// Allow onSessionEnd + queue_time loop to complete.
	time.Sleep(30 * time.Millisecond)

	// Status must still be Delivered — not reverted to statusQueued.
	final, err := msgStore.GetMessage(ctx, "fix1-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, final.Status,
		"queue_time reset must skip Delivered messages — status must not be reverted to queued")
}

// ─── Fix 2: recall retries termination ───────────────────────────────────────

// TestFix2a_PreprocessingRecall_IncrementsRetries verifies that the preprocessing
// recall path increments msg.Retries in the DB before calling ExecInSession, so
// the mandatory-tool retry counter advances even when markMessagesQueued is skipped.
func TestFix2a_PreprocessingRecall_IncrementsRetries(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "fix2a-agent", "fix2a-agent", "/ws", "team")

	// Insert a query in StatusProcessing with Retries=0 — simulates a prior missed session.
	msg := &message.Message{
		ID:          "fix2a-msg",
		To:          "fix2a-agent",
		From:        "head",
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: message.RequestType("query"),
		ShouldReply: true,
		Prompt:      "answer this",
		Refs:        "{}",
		Status:      message.StatusInQueue,
		SentTime:    1000,
		Retries:     0,
	}
	require.NoError(t, msgStore.InsertMessage(ctx, msg))
	msg.Status = message.StatusProcessing
	require.NoError(t, msgStore.UpdateMessage(ctx, msg))

	// Preprocessing recall fires, increments Retries before ExecInSession.
	proc.MessageArrived(ctx, "head", "fix2a-agent")
	omni.waitForExec(t) // block — just need to ensure ExecInSession was called

	// Retries must be 1 now (incremented by the preprocessing recall path).
	fresh, err := msgStore.GetMessage(ctx, "fix2a-msg")
	require.NoError(t, err)
	assert.Equal(t, 1, fresh.Retries,
		"preprocessing recall must increment Retries before ExecInSession so the retry counter advances")

	// Allow the exec goroutine to be unblocked by context cancellation on test cleanup.
	// We don't release explicitly — the test is done.
}

// TestFix2b_OnStopRecall_IncrementsRetries verifies that each OnStop recall injection
// increments msg.Retries, so the guard eventually terminates.
func TestFix2b_OnStopRecall_IncrementsRetries(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "fix2b-agent", "fix2b-agent", "/ws", "team")

	msg := hookMsg("fix2b-msg", "fix2b-agent", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "head", "fix2b-agent")
	e1 := omni.waitForExec(t) // Retries → 1 via markMessagesQueued

	proc.OnPreSessionStart("fix2b-agent", "fix2b-agent", "sess-fix2b", "/ws")
	proc.OnUserPromptSubmit(ctx, "fix2b-agent", "sess-fix2b", e1.prompt)

	// First OnStop — mandatory tool not invoked. Retries=1 ≤ 3 → recall → Retries→2.
	recall1 := proc.OnStop(ctx, "fix2b-agent", "sess-fix2b")
	require.NotNil(t, recall1, "first OnStop must return recall (Retries was 1)")

	fresh, err := msgStore.GetMessage(ctx, "fix2b-msg")
	require.NoError(t, err)
	assert.Equal(t, 2, fresh.Retries,
		"OnStop recall must increment Retries from 1→2")

	close(e1.relCh)
}

// TestFix2c_RecallTerminatesAfterMaxRetries verifies that after maxMandatoryToolRetries
// (3) the OnStop path delivers instead of injecting another recall.
func TestFix2c_RecallTerminatesAfterMaxRetries(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "fix2c-agent", "fix2c-agent", "/ws", "team")

	// Insert with Retries=2 — executeLoop will mark it to 3, then OnStop sees 3 ≤ 3 → recall → 4.
	// Second OnStop sees 4 > 3 → deliver.
	msg := hookMsg("fix2c-msg", "fix2c-agent", "head", 1000)
	require.NoError(t, msgStore.InsertMessage(ctx, msg))
	msg.Retries = 2
	require.NoError(t, msgStore.UpdateMessage(ctx, msg))

	proc.MessageArrived(ctx, "head", "fix2c-agent")
	e1 := omni.waitForExec(t) // executeLoop increments Retries: 2→3

	proc.OnPreSessionStart("fix2c-agent", "fix2c-agent", "sess-fix2c", "/ws")
	proc.OnUserPromptSubmit(ctx, "fix2c-agent", "sess-fix2c", e1.prompt)

	// OnStop at Retries=3 → recall returned, Retries→4.
	recall := proc.OnStop(ctx, "fix2c-agent", "sess-fix2c")
	require.NotNil(t, recall, "OnStop at Retries=3 must still return recall (3 ≤ maxMandatoryToolRetries)")

	// Simulate next OnStop (Retries=4 > maxMandatoryToolRetries=3) — must deliver.
	recall2 := proc.OnStop(ctx, "fix2c-agent", "sess-fix2c")
	assert.Nil(t, recall2, "OnStop at Retries=4 must deliver (max retries exceeded)")

	fresh, err := msgStore.GetMessage(ctx, "fix2c-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, fresh.Status,
		"message must be force-delivered after maxMandatoryToolRetries exceeded")

	close(e1.relCh)
}

// ─── Fix 3: task-context query filter in pickNextMessages ────────────────────

// TestFix3_TaskMuxQueryFilter verifies that when TaskMux is set, the query/instant
// accumulation loop skips messages with a non-matching task_id while allowing
// messages with no task_id and messages with the matching task_id.
func TestFix3_TaskMuxQueryFilter(t *testing.T) {
	ctx := context.Background()

	qMsg := func(id, to, taskID string, sentTime int64) *message.Message {
		return &message.Message{
			ID:          id,
			To:          to,
			From:        "sender",
			FromSpec:    message.SpecOmni,
			ToSpec:      message.SpecOmni,
			RequestType: message.RequestType("query"),
			ShouldReply: true,
			Prompt:      "q",
			Refs:        "{}",
			Status:      message.StatusInQueue,
			SentTime:    sentTime,
			TaskID:      taskID,
		}
	}

	t.Run("Query for matching task is picked", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "fix3-match", "fix3-match", "/ws", "")

		engine.SetTaskMuxForTest(proc, "fix3-match", &engine.TaskKey{TaskID: "task-A", CreatorAgentID: "creator-A"})

		q := qMsg("fix3-q-match", "fix3-match", "task-A", 100)
		q.CreatorAgentID = "creator-A"
		require.NoError(t, msgStore.InsertMessage(ctx, q))

		picked, err := engine.PickNextMessagesForTest(proc, "fix3-match")
		require.NoError(t, err)
		require.Len(t, picked, 1, "query for matching task must be picked")
		assert.Equal(t, "fix3-q-match", picked[0].ID)
	})

	t.Run("Query for different task is picked in normal mode (no active execute)", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "fix3-skip", "fix3-skip", "/ws", "")

		engine.SetTaskMuxForTest(proc, "fix3-skip", &engine.TaskKey{TaskID: "task-A", CreatorAgentID: "creator-A"})

		q := qMsg("fix3-q-skip", "fix3-skip", "task-B", 100)
		q.CreatorAgentID = "creator-B"
		require.NoError(t, msgStore.InsertMessage(ctx, q))

		picked, err := engine.PickNextMessagesForTest(proc, "fix3-skip")
		require.NoError(t, err)
		require.Len(t, picked, 1,
			"query for a different task_id must be picked in normal mode — task filter only applies in T2 bypass (active execute in flight)")
		assert.Equal(t, "fix3-q-skip", picked[0].ID)
	})

	t.Run("Query with no task_id is always picked regardless of TaskMux", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "fix3-notask", "fix3-notask", "/ws", "")

		engine.SetTaskMuxForTest(proc, "fix3-notask", &engine.TaskKey{TaskID: "task-A", CreatorAgentID: "creator-A"})

		q := qMsg("fix3-q-notask", "fix3-notask", "", 100) // no task_id
		require.NoError(t, msgStore.InsertMessage(ctx, q))

		picked, err := engine.PickNextMessagesForTest(proc, "fix3-notask")
		require.NoError(t, err)
		require.Len(t, picked, 1, "query with no task_id must always be picked even when TaskMux is set")
		assert.Equal(t, "fix3-q-notask", picked[0].ID)
	})
}

// ─── Fix 4: markDelivered no longer spawns executeLoop ───────────────────────

// TestFix4_NoEarlyDelivery verifies that when a message is delivered inside a session
// (via OnStop/markDelivered), a second queued message is NOT dispatched until after
// the current ExecInSession fully returns — i.e. markDelivered itself must NOT spawn
// a concurrent executeLoop.
func TestFix4_NoEarlyDelivery(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "fix4-agent", "fix4-agent", "/ws", "team")

	msg1 := hookMsg("fix4-msg1", "fix4-agent", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg1)

	// Start first session.
	proc.MessageArrived(ctx, "head", "fix4-agent")
	e1 := omni.waitForExec(t)

	// Fire the full hook sequence to deliver msg1 inside the session.
	proc.OnPreSessionStart("fix4-agent", "fix4-agent", "sess-fix4", "/ws")
	proc.OnUserPromptSubmit(ctx, "fix4-agent", "sess-fix4", e1.prompt)
	proc.OnPostToolUse("fix4-agent", "sess-fix4", "send_response", map[string]any{"message_id": "fix4-msg1"})

	// Insert msg2 WITHOUT calling MessageArrived so no external executeLoop is spawned.
	// The only path that can pick msg2 is the post-loop retry after ExecInSession returns.
	msg2 := hookMsg("fix4-msg2", "fix4-agent", "head", 2000)
	insertEngineMessages(t, ctx, msgStore, msg2)

	// OnStop fires → markDelivered runs → msg1 delivered.
	// With the fix, markDelivered does NOT spawn a new executeLoop.
	recall := proc.OnStop(ctx, "fix4-agent", "sess-fix4")
	require.Nil(t, recall, "msg1 must be delivered (no recall)")

	// Verify: no second ExecInSession while e1 is still blocking.
	// markDelivered must not have triggered delivery of msg2.
	select {
	case spurious := <-omni.execCh:
		close(spurious.relCh)
		t.Fatalf("unexpected ExecInSession for %q — markDelivered must not spawn an executeLoop", spurious.agentID)
	case <-time.After(80 * time.Millisecond):
		// Correct: msg2 is still waiting.
	}

	assert.Equal(t, 1, len(omni.execCalls()),
		"only one ExecInSession must have fired while first session is live")

	// Now release the first exec — onSessionEnd + post-loop retry fires for msg2.
	close(e1.relCh)

	// msg2 should be picked by the post-loop retry.
	e2 := omni.waitForExec(t)
	assert.Equal(t, "fix4-agent", e2.agentID, "post-loop retry must dispatch msg2 after first session ends")
	close(e2.relCh)

	// Allow settlement.
	time.Sleep(20 * time.Millisecond)
}

// ─── Fix 5: hydrateState restores full TaskKey (TaskID + CreatorAgentID) ─────

// TestFix5_HydrateStateRestoresCreatorAgentID verifies that after a simulated
// engine restart (hydrateState call), the TaskMux is restored with both TaskID
// and CreatorAgentID from the task_deliveries table — not just TaskID.
func TestFix5_HydrateStateRestoresCreatorAgentID(t *testing.T) {
	ctx := context.Background()

	// Share a single DB so the message store and task delivery store see the same data.
	db := database.WithTestDB(t)
	msgStore := message.New(db)
	deliveryStore := session.NewTaskDeliveryStoreFromDB(db)

	// Pre-seed: agent has a pending execute message so hydrateState includes it.
	pendingMsg := &message.Message{
		ID:          "fix5-pending-msg",
		To:          "fix5-agent",
		From:        "creator-fix5",
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: message.RequestTypeExecute,
		ShouldReply: false,
		Prompt:      "do work",
		Refs:        "{}",
		Status:      message.StatusInQueue,
		TaskID:      "task-fix5",
		SentTime:    1000,
	}
	require.NoError(t, msgStore.InsertMessage(ctx, pendingMsg))

	// Record a task delivery checkpoint (as if StartDelivery was called before restart).
	require.NoError(t, deliveryStore.StartDelivery(ctx, "task-fix5", "fix5-agent", "creator-fix5", "fix5-pending-msg"))

	// Simulate engine restart: new ProcessingEngine with the same DB.
	proc := engine.New(msgStore,
		engine.WithTestBinary(&noopCLI{}),
		engine.WithTaskDeliveryStore(deliveryStore),
	)
	engine.StartForTest(proc, ctx)

	// hydrateState loads pending agents and restores TaskMux from task_deliveries.
	engine.HydrateStateForTest(proc, ctx)

	mux := engine.GetTaskMuxForTest(proc, "fix5-agent")
	require.NotNil(t, mux, "hydrateState must restore TaskMux for agent with in_progress task delivery")
	assert.Equal(t, "task-fix5", mux.TaskID,
		"restored TaskMux must have the correct TaskID")
	assert.Equal(t, "creator-fix5", mux.CreatorAgentID,
		"restored TaskMux must have the CreatorAgentID — not just TaskID")
}
