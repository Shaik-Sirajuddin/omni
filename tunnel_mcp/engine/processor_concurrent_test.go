//go:build integration

package engine_test

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

// ─── helpers ─────────────────────────────────────────────────────────────────

func taskExecuteMsg(id, to, from, taskID, creatorID string, sentTime int64) *message.Message {
	return &message.Message{
		ID: id, To: to, From: from,
		FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
		RequestType:    message.RequestTypeExecute,
		ShouldReply:    false,
		Prompt:         "do the task",
		Refs:           "{}",
		Status:         message.StatusInQueue,
		SentTime:       sentTime,
		TaskID:         taskID,
		CreatorAgentID: creatorID,
	}
}

func taskQueryMsg(id, to, from, taskID, creatorID string, sentTime int64) *message.Message {
	return &message.Message{
		ID: id, To: to, From: from,
		FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
		RequestType:    message.RequestType("query"),
		ShouldReply:    true,
		Prompt:         "answer this",
		Refs:           "{}",
		Status:         message.StatusInQueue,
		SentTime:       sentTime,
		TaskID:         taskID,
		CreatorAgentID: creatorID,
	}
}

// fullDeliver fires the hook sequence that marks all StatusProcessing messages
// as delivered (tool invoked → OnStop returns nil).
func fullDeliver(proc *engine.ProcessingEngine, agentID, sessionID string, prompt string) {
	proc.OnPreSessionStart(agentID, agentID, sessionID, "/ws")
	proc.OnUserPromptSubmit(context.Background(), agentID, sessionID, prompt)
	proc.OnPostToolUse(agentID, sessionID, "send_response", nil)
	proc.OnStop(context.Background(), agentID, sessionID)
}

// ─── Multi-agent: concurrent sessions ────────────────────────────────────────

// TestMultiAgent_ConcurrentSessions verifies that two independent agents execute
// simultaneously — each ExecInSession blocks independently and both are live at
// the same time. Releasing both results in both messages delivered.
func TestMultiAgent_ConcurrentSessions(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "concurrent-A", "concurrent-A", "/ws", "team")
	engine.RegisterAgentForTest(proc, "concurrent-B", "concurrent-B", "/ws", "team")

	msgA := hookMsg("conc-msg-A", "concurrent-A", "head", 1000)
	msgB := hookMsg("conc-msg-B", "concurrent-B", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msgA, msgB)

	proc.MessageArrived(ctx, "head", "concurrent-A")
	proc.MessageArrived(ctx, "head", "concurrent-B")

	// Both sessions must start concurrently — collect both exec entries.
	var eA, eB execEntry
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); eA = omni.waitForExec(t) }()
	go func() { defer wg.Done(); eB = omni.waitForExec(t) }()
	wg.Wait()

	// Both must be live at the same time.
	assert.Contains(t, []string{"concurrent-A", "concurrent-B"}, eA.agentID)
	assert.Contains(t, []string{"concurrent-A", "concurrent-B"}, eB.agentID)
	assert.NotEqual(t, eA.agentID, eB.agentID, "two distinct agents must execute concurrently")

	stA, _ := engine.GetAgentStateForTest(proc, "concurrent-A")
	stB, _ := engine.GetAgentStateForTest(proc, "concurrent-B")
	assert.Equal(t, engine.AgentStatusRunning, stA.Status, "agent-A must be Running")
	assert.Equal(t, engine.AgentStatusRunning, stB.Status, "agent-B must be Running")

	// Deliver both via hooks then release.
	fullDeliver(proc, eA.agentID, "sess-"+eA.agentID, eA.prompt)
	close(eA.relCh)
	fullDeliver(proc, eB.agentID, "sess-"+eB.agentID, eB.prompt)
	close(eB.relCh)

	time.Sleep(30 * time.Millisecond)

	mA, _ := msgStore.GetMessage(ctx, "conc-msg-A")
	mB, _ := msgStore.GetMessage(ctx, "conc-msg-B")
	assert.Equal(t, message.StatusDelivered, mA.Status, "msg-A must be delivered")
	assert.Equal(t, message.StatusDelivered, mB.Status, "msg-B must be delivered")
}

// TestMultiAgent_RecallDoesNotBlockOther verifies that agent-A's recall cycle
// (status=Running, message=Processing) does not block agent-B from delivering
// its own message — each agent's state is fully independent.
func TestMultiAgent_RecallDoesNotBlockOther(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "recall-A", "recall-A", "/ws", "team")
	engine.RegisterAgentForTest(proc, "indep-B", "indep-B", "/ws", "team")

	msgA := hookMsg("recall-A-msg", "recall-A", "head", 1000)
	msgB := hookMsg("indep-B-msg", "indep-B", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msgA, msgB)

	proc.MessageArrived(ctx, "head", "recall-A")
	eA := omni.waitForExec(t)

	// Put agent-A into recall (no send_response, status stays Running).
	proc.OnPreSessionStart("recall-A", "recall-A", "sess-recall-A", "/ws")
	proc.OnUserPromptSubmit(ctx, "recall-A", "sess-recall-A", eA.prompt)
	r := proc.OnStop(ctx, "recall-A", "sess-recall-A")
	require.NotNil(t, r, "agent-A must be in recall")

	stA, _ := engine.GetAgentStateForTest(proc, "recall-A")
	assert.Equal(t, engine.AgentStatusRunning, stA.Status, "agent-A must stay Running during recall")

	// Now trigger agent-B — must execute independently of agent-A's recall.
	proc.MessageArrived(ctx, "head", "indep-B")
	eB := omni.waitForExec(t)
	assert.Equal(t, "indep-B", eB.agentID, "agent-B must start independently of agent-A's recall")

	fullDeliver(proc, "indep-B", "sess-indep-B", eB.prompt)
	close(eB.relCh)

	time.Sleep(20 * time.Millisecond)
	mB, _ := msgStore.GetMessage(ctx, "indep-B-msg")
	assert.Equal(t, message.StatusDelivered, mB.Status,
		"agent-B must deliver normally even while agent-A is in recall")

	close(eA.relCh) // cleanup
}

// ─── Recall: success on turn 2 ───────────────────────────────────────────────

// TestRecall_SucceedsOnTurn2 verifies that an agent can recover from a recall
// cycle by calling send_response on the second turn — the message is delivered
// cleanly without forcing max retries.
func TestRecall_SucceedsOnTurn2(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "turn2-agent", "turn2-agent", "/ws", "team")

	msg := hookMsg("turn2-msg", "turn2-agent", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "head", "turn2-agent")
	e1 := omni.waitForExec(t)

	proc.OnPreSessionStart("turn2-agent", "turn2-agent", "sess-t2", "/ws")
	proc.OnUserPromptSubmit(ctx, "turn2-agent", "sess-t2", e1.prompt)

	// Turn 1: mandatory tool not called → recall injected. Retries 1→2.
	r1 := proc.OnStop(ctx, "turn2-agent", "sess-t2")
	require.NotNil(t, r1, "turn 1 must inject recall")

	m, _ := msgStore.GetMessage(ctx, "turn2-msg")
	assert.Equal(t, message.StatusProcessing, m.Status, "message must stay Processing after turn 1 recall")
	assert.Equal(t, 2, m.Retries, "Retries must be 2 after turn 1")

	// Turn 2: agent calls send_response → delivered.
	proc.OnPostToolUse("turn2-agent", "sess-t2", "send_response", nil)
	r2 := proc.OnStop(ctx, "turn2-agent", "sess-t2")
	assert.Nil(t, r2, "turn 2 must deliver (tool was invoked)")

	m, err := msgStore.GetMessage(ctx, "turn2-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusDelivered, m.Status,
		"message must be Delivered after send_response on turn 2")
	assert.Equal(t, 2, m.Retries,
		"Retries must still be 2 — no extra increment on successful delivery")

	st, _ := engine.GetAgentStateForTest(proc, "turn2-agent")
	assert.Equal(t, engine.AgentStatusReady, st.Status, "agent must be Ready after delivery")

	close(e1.relCh)
}

// ─── Interrupt during recall ──────────────────────────────────────────────────

// TestInterruptDuringRecall verifies that when an agent is interrupted mid-recall
// (IsInterrupted=true), OnStop resets all StatusProcessing messages back to
// StatusInQueue and clears the session — the recall is abandoned.
func TestInterruptDuringRecall(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "interrupt-recall", "interrupt-recall", "/ws", "team")

	msg := hookMsg("interrupt-msg", "interrupt-recall", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "head", "interrupt-recall")
	e1 := omni.waitForExec(t)

	proc.OnPreSessionStart("interrupt-recall", "interrupt-recall", "sess-int", "/ws")
	proc.OnUserPromptSubmit(ctx, "interrupt-recall", "sess-int", e1.prompt)

	// Turn 1 recall — status stays Running.
	r1 := proc.OnStop(ctx, "interrupt-recall", "sess-int")
	require.NotNil(t, r1, "turn 1 must recall")

	m, _ := msgStore.GetMessage(ctx, "interrupt-msg")
	assert.Equal(t, message.StatusProcessing, m.Status)

	// Agent is interrupted before the second OnStop fires.
	proc.Interrupt("interrupt-recall")

	st, _ := engine.GetAgentStateForTest(proc, "interrupt-recall")
	assert.Equal(t, engine.AgentStatusPaused, st.Status, "Interrupt must set status to Paused")

	// OnStop fires with IsInterrupted=true → messages reset to InQueue.
	r2 := proc.OnStop(ctx, "interrupt-recall", "sess-int")
	assert.Nil(t, r2, "OnStop during interrupt must not inject recall")

	m, err := msgStore.GetMessage(ctx, "interrupt-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusInQueue, m.Status,
		"interrupt must reset processing message back to StatusInQueue")

	st, _ = engine.GetAgentStateForTest(proc, "interrupt-recall")
	assert.Equal(t, engine.AgentStatusReady, st.Status,
		"agent must be Ready after interrupt+OnStop (IsInterrupted cleared)")

	close(e1.relCh)
}

// ─── Queue: messages drain in order ──────────────────────────────────────────

// TestQueue_ExecutesDrainInSentTimeOrder verifies that when multiple execute
// messages are queued for one agent, they are delivered sequentially in
// sentTime ascending order — each session completes before the next begins.
func TestQueue_ExecutesDrainInSentTimeOrder(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "drain-agent", "drain-agent", "/ws", "team")

	m1 := hookMsg("drain-1", "drain-agent", "head", 1000)
	m2 := hookMsg("drain-2", "drain-agent", "head", 2000)
	m3 := hookMsg("drain-3", "drain-agent", "head", 3000)
	insertEngineMessages(t, ctx, msgStore, m1, m2, m3)

	proc.MessageArrived(ctx, "head", "drain-agent")

	// Session 1: must be m1 (earliest sentTime).
	e1 := omni.waitForExec(t)
	assert.Contains(t, e1.prompt, "drain-1", "first session must deliver drain-1")
	fullDeliver(proc, "drain-agent", "sess-drain-1", e1.prompt)
	close(e1.relCh)

	// Session 2: post-loop retry picks m2.
	e2 := omni.waitForExec(t)
	assert.Contains(t, e2.prompt, "drain-2", "second session must deliver drain-2")
	fullDeliver(proc, "drain-agent", "sess-drain-2", e2.prompt)
	close(e2.relCh)

	// Session 3: post-loop retry picks m3.
	e3 := omni.waitForExec(t)
	assert.Contains(t, e3.prompt, "drain-3", "third session must deliver drain-3")
	fullDeliver(proc, "drain-agent", "sess-drain-3", e3.prompt)
	close(e3.relCh)

	time.Sleep(30 * time.Millisecond)

	for _, id := range []string{"drain-1", "drain-2", "drain-3"} {
		m, err := msgStore.GetMessage(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, message.StatusDelivered, m.Status, "%s must be delivered", id)
	}
}

// ─── Task isolation: two tasks, same agent ───────────────────────────────────

// TestTaskIsolation_TwoTasksDeliverSequentially verifies that execute messages
// for two distinct task_ids on the same agent are delivered one at a time —
// the second task only starts after the first session completes.
func TestTaskIsolation_TwoTasksDeliverSequentially(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "twotask-agent", "twotask-agent", "/ws", "team")

	// task-X arrives first (sentTime=1000), task-Y second (sentTime=2000).
	mx := taskExecuteMsg("twotask-x", "twotask-agent", "head", "task-X", "creator", 1000)
	my := taskExecuteMsg("twotask-y", "twotask-agent", "head", "task-Y", "creator", 2000)
	insertEngineMessages(t, ctx, msgStore, mx, my)

	proc.MessageArrived(ctx, "head", "twotask-agent")

	// First session must carry task-X.
	e1 := omni.waitForExec(t)
	assert.Contains(t, e1.prompt, "twotask-x", "first session must deliver task-X message")
	assert.NotContains(t, e1.prompt, "twotask-y", "task-Y must not appear in task-X's session")

	// task-Y must NOT be dispatched while task-X session is live.
	select {
	case spurious := <-omni.execCh:
		close(spurious.relCh)
		t.Fatal("task-Y must not be dispatched while task-X session is still alive")
	case <-time.After(60 * time.Millisecond):
	}

	// Deliver task-X and release.
	fullDeliver(proc, "twotask-agent", "sess-tx", e1.prompt)
	close(e1.relCh)

	// task-Y must be dispatched by the post-loop retry.
	e2 := omni.waitForExec(t)
	assert.Contains(t, e2.prompt, "twotask-y", "second session must deliver task-Y message")
	assert.NotContains(t, e2.prompt, "twotask-x", "task-X must not appear in task-Y's session")

	fullDeliver(proc, "twotask-agent", "sess-ty", e2.prompt)
	close(e2.relCh)

	time.Sleep(20 * time.Millisecond)
	for id, want := range map[string]message.Status{"twotask-x": message.StatusDelivered, "twotask-y": message.StatusDelivered} {
		m, err := msgStore.GetMessage(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, want, m.Status, "%s must be %s", id, want)
	}
}

// TestTaskIsolation_PauseResumeLive verifies that pausing a task prevents its
// execute from being picked, while unpaused messages for the same agent are
// unaffected; after resume, the paused task is dispatched normally.
func TestTaskIsolation_PauseResumeLive(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "pause-agent", "pause-agent", "/ws", "team")

	// task-P is paused, task-Q is not. task-P arrives first (sentTime=1000).
	mp := taskExecuteMsg("pause-msg", "pause-agent", "head", "task-P", "creator", 1000)
	mq := taskExecuteMsg("run-msg", "pause-agent", "head", "task-Q", "creator", 2000)
	insertEngineMessages(t, ctx, msgStore, mp, mq)

	// Pause task-P before dispatching.
	proc.PauseTask("pause-agent", "task-P", "creator")

	proc.MessageArrived(ctx, "head", "pause-agent")

	// task-P is first by sentTime but paused → pickNextMessages returns nil.
	// The post-loop retry must also see nil → no execution yet.
	select {
	case spurious := <-omni.execCh:
		close(spurious.relCh)
		t.Fatalf("paused task-P must not be dispatched; got exec for %q", spurious.agentID)
	case <-time.After(80 * time.Millisecond):
	}

	// Resume task-P → engine spawns executeLoop → pickNextMessages now picks task-P.
	proc.ResumeTask(ctx, "pause-agent", "task-P", "creator")

	e1 := omni.waitForExec(t)
	assert.Contains(t, e1.prompt, "pause-msg",
		"after resume, task-P must be dispatched")

	fullDeliver(proc, "pause-agent", "sess-p", e1.prompt)
	close(e1.relCh)

	// task-Q must be delivered by the post-loop retry.
	e2 := omni.waitForExec(t)
	assert.Contains(t, e2.prompt, "run-msg", "task-Q must be delivered after task-P")
	fullDeliver(proc, "pause-agent", "sess-q", e2.prompt)
	close(e2.relCh)

	time.Sleep(20 * time.Millisecond)
	mp2, _ := msgStore.GetMessage(ctx, "pause-msg")
	mq2, _ := msgStore.GetMessage(ctx, "run-msg")
	assert.Equal(t, message.StatusDelivered, mp2.Status, "task-P must be delivered after resume")
	assert.Equal(t, message.StatusDelivered, mq2.Status, "task-Q must be delivered")
}

// ─── Mixed batch: execute + co-task query bundled in one session ─────────────

// TestMixedBatch_ExecAndCoTaskQueryBundled verifies that when an execute message
// and a co-task query (same task_id + creator_agent_id) are both pending, they
// are delivered in a single ExecInSession call with both message IDs in the prompt.
func TestMixedBatch_ExecAndCoTaskQueryBundled(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "mixed-agent", "mixed-agent", "/ws", "team")

	exec := taskExecuteMsg("mixed-exec", "mixed-agent", "head", "task-M", "creator-M", 1000)
	query := taskQueryMsg("mixed-query", "mixed-agent", "head", "task-M", "creator-M", 1001)
	insertEngineMessages(t, ctx, msgStore, exec, query)

	proc.MessageArrived(ctx, "head", "mixed-agent")
	e1 := omni.waitForExec(t)

	// Both IDs must appear in the single combined prompt.
	assert.Contains(t, e1.prompt, "mixed-exec",
		"execute message ID must be in the mixed-batch prompt")
	assert.Contains(t, e1.prompt, "mixed-query",
		"co-task query message ID must be bundled in the same prompt")

	// Deliver both with tool invocation.
	proc.OnPreSessionStart("mixed-agent", "mixed-agent", "sess-mixed", "/ws")
	proc.OnUserPromptSubmit(ctx, "mixed-agent", "sess-mixed", e1.prompt)
	proc.OnPostToolUse("mixed-agent", "sess-mixed", "send_response", nil)
	r := proc.OnStop(ctx, "mixed-agent", "sess-mixed")
	assert.Nil(t, r, "OnStop must deliver both messages")

	close(e1.relCh)
	time.Sleep(20 * time.Millisecond)

	for _, id := range []string{"mixed-exec", "mixed-query"} {
		m, err := msgStore.GetMessage(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, message.StatusDelivered, m.Status, "%s must be delivered", id)
	}
}

// ─── Preprocessing recall: multiple processing messages ───────────────────────

// TestPreprocessingRecall_MultipleMessages verifies that when more than one
// non-instant message is stuck in StatusProcessing (from a prior missed session),
// all of them appear in the single preprocessing recall prompt so the agent can
// respond to every outstanding request in one turn.
func TestPreprocessingRecall_MultipleMessages(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "multi-proc", "multi-proc", "/ws", "team")

	// Seed two queries in StatusProcessing.
	for _, id := range []string{"proc-q1", "proc-q2"} {
		m := &message.Message{
			ID: id, To: "multi-proc", From: "head",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestType("query"),
			ShouldReply: true, Prompt: "q", Refs: "{}",
			Status: message.StatusInQueue, SentTime: 1000,
		}
		require.NoError(t, msgStore.InsertMessage(ctx, m))
		m.Status = message.StatusProcessing
		require.NoError(t, msgStore.UpdateMessage(ctx, m))
	}

	proc.MessageArrived(ctx, "head", "multi-proc")
	e1 := omni.waitForExec(t)

	// Both IDs must appear in the preprocessing recall prompt (YAML).
	assert.Contains(t, e1.prompt, "proc-q1",
		"first processing message ID must be in the preprocessing recall prompt")
	assert.Contains(t, e1.prompt, "proc-q2",
		"second processing message ID must be in the preprocessing recall prompt")
	assert.Contains(t, e1.prompt, "send_response",
		"preprocessing recall prompt must instruct agent to call send_response")

	// Both messages must have Retries incremented before ExecInSession.
	for _, id := range []string{"proc-q1", "proc-q2"} {
		m, err := msgStore.GetMessage(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, 1, m.Retries, "%s must have Retries=1 after preprocessing recall", id)
	}

	close(e1.relCh)
}

// ─── Concurrent retries: two agents both in recall simultaneously ─────────────

// TestMultiAgent_ConcurrentRecallCycles verifies that two agents can each be
// in their own independent recall cycle at the same time. Each agent's retry
// counter advances independently; delivering one does not affect the other.
func TestMultiAgent_ConcurrentRecallCycles(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	omni := newBlockingOmniCLI()

	proc := engine.New(msgStore, engine.WithTestBinary(omni))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "rc-X", "rc-X", "/ws", "team")
	engine.RegisterAgentForTest(proc, "rc-Y", "rc-Y", "/ws", "team")

	msgX := hookMsg("rc-x-msg", "rc-X", "head", 1000)
	msgY := hookMsg("rc-y-msg", "rc-Y", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msgX, msgY)

	proc.MessageArrived(ctx, "head", "rc-X")
	proc.MessageArrived(ctx, "head", "rc-Y")

	// Both exec in parallel.
	var eX, eY execEntry
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); eX = omni.waitForExec(t) }()
	go func() { defer wg.Done(); eY = omni.waitForExec(t) }()
	wg.Wait()

	// Map agent → exec entry.
	byAgent := map[string]execEntry{eX.agentID: eX, eY.agentID: eY}

	// Both agents: fire PreSessionStart + UserPromptSubmit.
	proc.OnPreSessionStart("rc-X", "rc-X", "sess-rcX", "/ws")
	proc.OnUserPromptSubmit(ctx, "rc-X", "sess-rcX", byAgent["rc-X"].prompt)
	proc.OnPreSessionStart("rc-Y", "rc-Y", "sess-rcY", "/ws")
	proc.OnUserPromptSubmit(ctx, "rc-Y", "sess-rcY", byAgent["rc-Y"].prompt)

	// Turn 1: both recall (no tool invoked).
	rX1 := proc.OnStop(ctx, "rc-X", "sess-rcX")
	rY1 := proc.OnStop(ctx, "rc-Y", "sess-rcY")
	require.NotNil(t, rX1, "rc-X turn 1 must recall")
	require.NotNil(t, rY1, "rc-Y turn 1 must recall")

	// rc-X delivers on turn 2 (tool invoked); rc-Y continues recall.
	proc.OnPostToolUse("rc-X", "sess-rcX", "send_response", nil)
	rX2 := proc.OnStop(ctx, "rc-X", "sess-rcX")
	rY2 := proc.OnStop(ctx, "rc-Y", "sess-rcY")

	assert.Nil(t, rX2, "rc-X turn 2 must deliver (tool invoked)")
	require.NotNil(t, rY2, "rc-Y turn 2 must still recall (tool not invoked)")

	// rc-X message delivered; rc-Y still Processing.
	mX, _ := msgStore.GetMessage(ctx, "rc-x-msg")
	mY, _ := msgStore.GetMessage(ctx, "rc-y-msg")
	assert.Equal(t, message.StatusDelivered, mX.Status, "rc-X message must be delivered after turn 2")
	assert.Equal(t, message.StatusProcessing, mY.Status, "rc-Y message must still be Processing")

	// rc-X must be Ready; rc-Y must still be Running.
	stX, _ := engine.GetAgentStateForTest(proc, "rc-X")
	stY, _ := engine.GetAgentStateForTest(proc, "rc-Y")
	assert.Equal(t, engine.AgentStatusReady, stX.Status, "rc-X must be Ready after delivery")
	assert.Equal(t, engine.AgentStatusRunning, stY.Status, "rc-Y must stay Running during recall")

	close(byAgent["rc-X"].relCh)
	close(byAgent["rc-Y"].relCh)
}
