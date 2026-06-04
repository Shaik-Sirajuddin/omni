//go:build integration

package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/engine"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── failingOmniCLI ──────────────────────────────────────────────────────────

// failingOmniCLI returns an error for the first N calls to ExecInSession then
// succeeds. Use maxFails=-1 to fail every call.
type failingOmniCLI struct {
	mu       sync.Mutex
	calls    int
	maxFails int     // number of calls that return errExec; -1 = always fail
	callsCh  chan struct{} // closed on each ExecInSession entry
}

var errExec = errors.New("exec failed: simulated process error")

func newFailingCLI(maxFails int) *failingOmniCLI {
	return &failingOmniCLI{maxFails: maxFails, callsCh: make(chan struct{}, 20)}
}

func (c *failingOmniCLI) ExecInSession(_ context.Context, _, _, _, _ string) error {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	c.callsCh <- struct{}{}
	if c.maxFails < 0 || n <= c.maxFails {
		return errExec
	}
	return nil
}

func (c *failingOmniCLI) GetPromptState(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (c *failingOmniCLI) totalCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// waitForCall blocks until at least one ExecInSession has been called.
func (c *failingOmniCLI) waitForCall(t *testing.T) {
	t.Helper()
	select {
	case <-c.callsCh:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "ExecInSession not called within timeout")
	}
}

// waitForNCalls waits until exactly n calls have been observed.
func (c *failingOmniCLI) waitForNCalls(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.totalCalls() >= n {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	require.Failf(t, "ExecInSession not called %d times within timeout", "%d", n)
}

// ─── Test 1: retry on exec failure below cap ─────────────────────────────────

// TestExecRetry_BelowCap verifies that when ExecInSession returns an error with
// msg.Retries < maxExecRetries, onSessionEnd re-queues the message to StatusInQueue
// and sets the agent back to Ready so the post-loop retry fires a new ExecInSession.
func TestExecRetry_BelowCap(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	// Fail first 2 calls; succeed on the 3rd — so the retry path fires twice and then delivers.
	cli := newFailingCLI(2)

	proc := engine.New(msgStore, engine.WithTestBinary(cli))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "retry-below", "retry-below", "/ws", "team")

	msg := hookMsg("retry-below-msg", "retry-below", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "head", "retry-below")

	// Wait for all 3 ExecInSession calls: 2 failures + 1 success.
	cli.waitForNCalls(t, 3)
	// Allow the 3rd (successful) session to complete + onSessionEnd to settle.
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 3, cli.totalCalls(),
		"engine must retry twice after exec failure before succeeding on the 3rd attempt")

	// After the successful 3rd call, agent should be Ready.
	st, _ := engine.GetAgentStateForTest(proc, "retry-below")
	assert.Equal(t, engine.AgentStatusReady, st.Status,
		"agent must be Ready after successful delivery")

	// Message must be in a terminal state (Delivered after OnStop, or still InQueue if no hooks fired).
	// The key invariant: it must NOT be StatusFailed from an early exec failure.
	m, err := msgStore.GetMessage(ctx, "retry-below-msg")
	require.NoError(t, err)
	assert.NotEqual(t, message.StatusFailed, m.Status,
		"message must not be permanently failed — exec retry must have re-queued it")
}

// ─── Test 2: stop after maxExecRetries ───────────────────────────────────────

// TestExecRetry_StopsAtMaxRetries verifies that after maxExecRetries (3) consecutive
// ExecInSession failures, the agent is set to AgentStatusStopped and the message
// is permanently StatusFailed — no further retries are attempted.
func TestExecRetry_StopsAtMaxRetries(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	// Always fail.
	cli := newFailingCLI(-1)

	proc := engine.New(msgStore, engine.WithTestBinary(cli))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "retry-max", "retry-max", "/ws", "team")

	msg := hookMsg("retry-max-msg", "retry-max", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	proc.MessageArrived(ctx, "head", "retry-max")

	// Wait for exactly maxExecRetries (3) calls to fire — then no more.
	cli.waitForNCalls(t, 3)
	// Allow the final onSessionEnd (which sets Stopped) to settle.
	time.Sleep(100 * time.Millisecond)

	// No 4th call must have fired.
	assert.Equal(t, 3, cli.totalCalls(),
		"engine must stop retrying after maxExecRetries (3) failures")

	st, _ := engine.GetAgentStateForTest(proc, "retry-max")
	assert.Equal(t, engine.AgentStatusStopped, st.Status,
		"agent must be AgentStatusStopped after maxExecRetries failures")

	m, err := msgStore.GetMessage(ctx, "retry-max-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusFailed, m.Status,
		"message must be StatusFailed after maxExecRetries failures")
}

// ─── Test 3: no retry when interrupted ───────────────────────────────────────

// TestExecRetry_NoRetryWhenInterrupted verifies that when IsInterrupted=true,
// an exec failure immediately sets Stopped — no retry is attempted regardless
// of how many retries remain.
func TestExecRetry_NoRetryWhenInterrupted(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	// Fail once, then succeed — but since interrupted, retry must NOT fire.
	cli := newFailingCLI(1)

	proc := engine.New(msgStore, engine.WithTestBinary(cli))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "retry-interrupted", "retry-interrupted", "/ws", "team")

	// Interrupt the agent before exec starts so IsInterrupted=true during onSessionEnd.
	proc.Interrupt("retry-interrupted")

	msg := hookMsg("retry-int-msg", "retry-interrupted", "head", 1000)
	insertEngineMessages(t, ctx, msgStore, msg)

	// Resume to allow executeLoop to run (sets IsInterrupted=false, status=Ready).
	proc.Resume(ctx, "retry-interrupted")

	// Wait for the first (failing) ExecInSession.
	cli.waitForCall(t)

	// Re-interrupt mid-flight to simulate interrupt during exec.
	proc.Interrupt("retry-interrupted")

	// Allow onSessionEnd to run.
	time.Sleep(80 * time.Millisecond)

	// Must be exactly 1 call — no retry because interrupted.
	assert.Equal(t, 1, cli.totalCalls(),
		"interrupted agent must not retry after exec failure")

	st, _ := engine.GetAgentStateForTest(proc, "retry-interrupted")
	assert.Equal(t, engine.AgentStatusStopped, st.Status,
		"interrupted agent must be Stopped after exec failure (no retry)")
}

// ─── Test 4: handleFailedExec cap (AgentCallback path) ───────────────────────

// TestHandleFailedExec_Cap verifies that repeated AgentCallback(failed=true) calls
// stop retrying after maxExecRetries (3) increments — the message is marked Failed
// and the agent is set Stopped without spawning further executeLoop calls.
//
// Drives the HTTP AgentCallback path directly (handleFailedExec), bypassing ExecInSession.
// Message is pre-seeded with Retries=1 (simulating one prior exec attempt).
func TestHandleFailedExec_Cap(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	cli := newFailingCLI(-1) // always fail — but this test drives AgentCallback directly

	proc := engine.New(msgStore, engine.WithTestBinary(cli))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "hfe-agent", "hfe-agent", "/ws", "team")

	// Seed message with Retries=1 (as if executeLoop ran once already).
	msg := &message.Message{
		ID: "hfe-msg", To: "hfe-agent", From: "head",
		FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
		RequestType: message.RequestTypeExecute,
		Prompt: "do it", Refs: "{}",
		Status: message.StatusInQueue, Retries: 1, SentTime: 1000,
	}
	require.NoError(t, msgStore.InsertMessage(ctx, msg))

	cb := func() {
		proc.AgentCallback(ctx, engine.AgentCallbackRequest{
			Source:  engine.MessageRef{MessageID: "hfe-msg"},
			AgentID: "hfe-agent",
		}, true)
	}

	// Call 1: Retries 1→2, below cap → retry fires (executeLoop picks nothing since no messages in InQueue).
	cb()
	time.Sleep(20 * time.Millisecond)

	m, err := msgStore.GetMessage(ctx, "hfe-msg")
	require.NoError(t, err)
	assert.Equal(t, 2, m.Retries, "first failed callback must increment Retries to 2")
	assert.NotEqual(t, message.StatusFailed, m.Status, "Retries=2: not yet at cap — must not be Failed")

	// Call 2: Retries 2→3, at cap → Stopped + Failed.
	cb()
	time.Sleep(20 * time.Millisecond)

	m, err = msgStore.GetMessage(ctx, "hfe-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusFailed, m.Status,
		"message must be StatusFailed after handleFailedExec reaches maxExecRetries (3)")

	st, _ := engine.GetAgentStateForTest(proc, "hfe-agent")
	assert.Equal(t, engine.AgentStatusStopped, st.Status,
		"agent must be Stopped after handleFailedExec cap")

	// No ExecInSession must have fired (message was never InQueue during the callbacks).
	assert.Equal(t, 0, cli.totalCalls(),
		"no ExecInSession must fire — AgentCallback path bypasses ExecInSession")
}
