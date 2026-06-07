//go:build integration

package integration

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
	// The 3rd call returned nil (success). executeLoop now waits on sessionDoneCh
	// before calling onSessionEnd. Signal it so the goroutine unblocks.
	engine.SignalSessionDoneForTest(proc, "retry-below")
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 3, cli.totalCalls(),
		"engine must retry twice after exec failure before succeeding on the 3rd attempt")

	// After the successful 3rd call, agent should be Ready.
	st, _ := engine.GetAgentStateForTest(proc, "retry-below")
	assert.Equal(t, engine.AgentStatusReady, st.Status,
		"agent must be Ready after successful delivery")

	// The key invariant is that 3 ExecInSession calls fired (retry path worked) and
	// the agent is Ready. The message ends up StatusFailed via the orphaned-message
	// safety-net in onSessionEnd because no OnStop hook fired for the 3rd (successful)
	// call — this is expected when using a hookless test CLI. The critical distinction
	// is Retries: it reaches 3 only because the retry path ran twice.
	m, err := msgStore.GetMessage(ctx, "retry-below-msg")
	require.NoError(t, err)
	assert.Equal(t, 3, m.Retries,
		"Retries must be 3: initial pick (1) + 2 retry re-queues (2, 3) — proves retry path ran")
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

// TestExecRetry_NoRetryWhenInterrupted verifies that when IsInterrupted=true at
// the time onSessionEnd runs, canRetry=false — the message is marked Failed and
// the agent stays Stopped even when Retries is below maxExecRetries.
//
// Uses OnSessionEndForTest to drive onSessionEnd directly, eliminating any race
// between Interrupt and ExecInSession returning.
func TestExecRetry_NoRetryWhenInterrupted(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)

	proc := engine.New(msgStore, engine.WithTestBinary(newFailingCLI(-1)))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "retry-int", "retry-int", "/ws", "team")

	// Seed a message in statusQueued with Retries=1 (below maxExecRetries — would retry
	// if not interrupted).
	msg := &message.Message{
		ID: "retry-int-msg", To: "retry-int", From: "head",
		FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
		RequestType: message.RequestTypeExecute,
		Prompt: "do work", Refs: "{}",
		Status: message.StatusInQueue, Retries: 1, SentTime: 1000,
	}
	require.NoError(t, msgStore.InsertMessage(ctx, msg))
	msg.Status = message.Status("queued")
	require.NoError(t, msgStore.UpdateMessage(ctx, msg))

	// Interrupt the agent — sets IsInterrupted=true, status=Paused.
	proc.Interrupt("retry-int")

	// Call onSessionEnd with execFailed=true: canRetry = execFailed && !IsInterrupted = false.
	// No retry must happen; message must be Failed and agent Stopped.
	engine.OnSessionEndForTest(proc, "retry-int", []*message.Message{msg}, true, 0)

	st, _ := engine.GetAgentStateForTest(proc, "retry-int")
	assert.Equal(t, engine.AgentStatusStopped, st.Status,
		"interrupted agent must be Stopped after exec failure — IsInterrupted blocks canRetry")

	fresh, err := msgStore.GetMessage(ctx, "retry-int-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusFailed, fresh.Status,
		"message must be Failed — interrupt prevents re-queue even when Retries < maxExecRetries")
}

// ─── Test 4: handleFailedExec cap (AgentCallback path) ───────────────────────

// TestHandleFailedExec_Cap verifies that when AgentCallback(failed=true) is called
// and msg.Retries after increment reaches maxExecRetries (3), handleFailedExec marks
// the message Failed and sets the agent Stopped without spawning another executeLoop.
//
// Seeds message with Retries=2 so one increment reaches 3 (the cap) immediately,
// preventing the executeLoop race where markMessagesQueued would add a 3rd increment.
func TestHandleFailedExec_Cap(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	cli := newFailingCLI(-1)

	proc := engine.New(msgStore, engine.WithTestBinary(cli))
	engine.StartForTest(proc, ctx)
	engine.RegisterAgentForTest(proc, "hfe-agent", "hfe-agent", "/ws", "team")

	// Seed at Retries=2: one more increment (2→3) reaches maxExecRetries.
	// handleFailedExec will see 3 >= 3 → cap → Stopped+Failed, no executeLoop.
	msg := &message.Message{
		ID: "hfe-msg", To: "hfe-agent", From: "head",
		FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
		RequestType: message.RequestTypeExecute,
		Prompt: "do it", Refs: "{}",
		Status: message.StatusInQueue, Retries: 2, SentTime: 1000,
	}
	require.NoError(t, msgStore.InsertMessage(ctx, msg))

	proc.AgentCallback(ctx, engine.AgentCallbackRequest{
		Source:  engine.MessageRef{MessageID: "hfe-msg"},
		AgentID: "hfe-agent",
	}, true)
	time.Sleep(30 * time.Millisecond)

	m, err := msgStore.GetMessage(ctx, "hfe-msg")
	require.NoError(t, err)
	assert.Equal(t, message.StatusFailed, m.Status,
		"message must be Failed when Retries reaches maxExecRetries (2→3)")
	assert.Equal(t, 3, m.Retries, "Retries must be 3 after cap increment")

	st, _ := engine.GetAgentStateForTest(proc, "hfe-agent")
	assert.Equal(t, engine.AgentStatusStopped, st.Status,
		"agent must be Stopped — handleFailedExec must not spawn executeLoop at cap")

	// No ExecInSession must fire — cap was reached before executeLoop was spawned.
	assert.Equal(t, 0, cli.totalCalls(),
		"no ExecInSession must fire when handleFailedExec hits the cap immediately")
}
