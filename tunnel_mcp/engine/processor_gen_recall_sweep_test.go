//go:build unit

package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── localBlockingCLI ─────────────────────────────────────────────────────────

// localBlockingCLI captures each ExecInSession call and blocks until released.
type localBlockingCLI struct {
	mu     sync.Mutex
	execs  []string
	prompts []string
	execCh chan localExecEntry
}

type localExecEntry struct {
	agentID string
	prompt  string
	relCh   chan struct{}
}

func newLocalBlockingCLI() *localBlockingCLI {
	return &localBlockingCLI{execCh: make(chan localExecEntry, 10)}
}

func (c *localBlockingCLI) ExecInSession(_ context.Context, agentID, _, _, prompt string) error {
	rel := make(chan struct{})
	c.mu.Lock()
	c.execs = append(c.execs, agentID)
	c.prompts = append(c.prompts, prompt)
	c.mu.Unlock()
	c.execCh <- localExecEntry{agentID: agentID, prompt: prompt, relCh: rel}
	<-rel
	return nil
}

func (c *localBlockingCLI) GetPromptState(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (c *localBlockingCLI) waitForExec(t *testing.T) localExecEntry {
	t.Helper()
	select {
	case e := <-c.execCh:
		return e
	case <-time.After(2 * time.Second):
		require.FailNow(t, "ExecInSession not called within timeout")
		return localExecEntry{}
	}
}

// ─── TestSessionGenerationGuard ───────────────────────────────────────────────

func TestSessionGenerationGuard(t *testing.T) {
	ctx := context.Background()

	// When generation is unchanged and status is Running, onSessionEnd resets to Ready.
	t.Run("Running Status Reset To Ready When Generation Unchanged", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-gen", "ag-gen", "/ws", "team")
		SetAgentStatusForTest(proc, "ag-gen", AgentStatusRunning)

		gen, _ := GetAgentStateForTest(proc, "ag-gen")
		capturedGen := gen.CodeSession.SessionGeneration

		OnSessionEndForTest(proc, "ag-gen", nil, false, capturedGen)

		state, _ := GetAgentStateForTest(proc, "ag-gen")
		assert.Equal(t, AgentStatusReady, state.Status,
			"onSessionEnd must reset Running→Ready when generation is unchanged")
	})

	// When generation advanced (markDelivered ran), onSessionEnd must NOT reset Running→Ready.
	t.Run("Running Status Preserved When Generation Advanced", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-adv", "ag-adv", "/ws", "team")
		SetAgentStatusForTest(proc, "ag-adv", AgentStatusRunning)

		// Capture generation BEFORE advancing it — this simulates what executeLoop does.
		genBefore, _ := GetAgentStateForTest(proc, "ag-adv")
		myGeneration := genBefore.CodeSession.SessionGeneration

		// Simulate markDelivered advancing the generation (new session started).
		IncrementGenerationForTest(proc, "ag-adv")

		OnSessionEndForTest(proc, "ag-adv", nil, false, myGeneration)

		state, _ := GetAgentStateForTest(proc, "ag-adv")
		assert.Equal(t, AgentStatusRunning, state.Status,
			"onSessionEnd must NOT reset Running→Ready when generation advanced (newer session took over)")
	})

	// execFailed=true always sets Stopped regardless of generation.
	t.Run("ExecFailed Always Sets Stopped", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-fail", "ag-fail", "/ws", "team")
		SetAgentStatusForTest(proc, "ag-fail", AgentStatusRunning)

		state, _ := GetAgentStateForTest(proc, "ag-fail")
		OnSessionEndForTest(proc, "ag-fail", nil, true, state.CodeSession.SessionGeneration)

		after, _ := GetAgentStateForTest(proc, "ag-fail")
		assert.Equal(t, AgentStatusStopped, after.Status, "exec failure must set Stopped")
	})
}

// ─── TestPreprocessingRecall ──────────────────────────────────────────────────

func TestPreprocessingRecall(t *testing.T) {
	ctx := context.Background()

	// When a non-instant message is in StatusProcessing, executeLoop uses buildWarmUpPrompt
	// (YAML format) as the preprocessing recall so OnUserPromptSubmit can re-advance the message
	// IDs back to StatusProcessing after the stale sweep.
	t.Run("Processing Query Message Triggers Preprocessing Recall Prompt", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		cli := newLocalBlockingCLI()
		proc := New(msgStore, WithTestBinary(cli))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-prep", "ag-prep", "/ws", "team")

		// Seed a processing QUERY message.
		msg := &message.Message{
			ID: "prep-query-1", To: "ag-prep", From: "sender",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: reqTypeQuery,
			Status:      message.StatusProcessing,
			Retries: 1, SentTime: 100, Prompt: "question?", Refs: "{}",
		}
		require.NoError(t, msgStore.InsertMessage(ctx, msg))

		proc.MessageArrived(ctx, "sender", "ag-prep")
		e1 := cli.waitForExec(t)

		// Preprocessing recall uses buildWarmUpPrompt (YAML), not plain-text buildRecallPrompt.
		// Check for YAML field presence instead of the plain-text callback notice.
		assert.Contains(t, e1.prompt, "prep-query-1",
			"preprocessing recall YAML must embed the processing message ID")
		assert.Contains(t, e1.prompt, "send_response",
			"preprocessing recall YAML instruction must reference send_response")
		close(e1.relCh)
	})

	// Instant (steer) messages in StatusProcessing do NOT trigger preprocessing recall.
	t.Run("Instant Processing Message Does Not Trigger Recall", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		cli := newLocalBlockingCLI()
		proc := New(msgStore, WithTestBinary(cli))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-instant", "ag-instant", "/ws", "team")

		// Processing instant message — exempt from recall.
		instant := &message.Message{
			ID: "steer-1", To: "ag-instant", From: "sender",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestType("instant"),
			Status:      message.StatusProcessing,
			SentTime: 50, Prompt: "steer", Refs: "{}",
		}
		// In_queue execute to give executeLoop something to pick normally.
		exec := &message.Message{
			ID: "exec-after", To: "ag-instant", From: "sender",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestTypeExecute,
			Status:      message.StatusInQueue,
			SentTime: 100, Prompt: "do work", Refs: "{}",
		}
		require.NoError(t, msgStore.InsertMessage(ctx, instant))
		require.NoError(t, msgStore.InsertMessage(ctx, exec))

		proc.MessageArrived(ctx, "sender", "ag-instant")
		e1 := cli.waitForExec(t)

		assert.NotContains(t, e1.prompt, "Tool callback not received",
			"instant message in processing must not trigger preprocessing recall")
		close(e1.relCh)
	})
}

// ─── TestOnStopKeepsRunningStatus ─────────────────────────────────────────────

func TestOnStopKeepsRunningStatus(t *testing.T) {
	ctx := context.Background()

	// OnStop with recall (mandatory tool not called, retries <= max) must NOT change
	// agent status from Running to Ready — the agent continues the same session.
	t.Run("Agent Stays Running When Recall Injected", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-running", "ag-running", "/ws", "team")
		SetSessionForTest(proc, "ag-running", "sess-running")
		SetAgentStatusForTest(proc, "ag-running", AgentStatusRunning)

		// Seed a processing message with retries <= maxMandatoryToolRetries (3).
		msg := &message.Message{
			ID: "recall-run-1", To: "ag-running", From: "sender",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestTypeExecute,
			Status:      message.StatusProcessing,
			Retries: 1, SentTime: 100, Prompt: "task", Refs: "{}",
		}
		require.NoError(t, msgStore.InsertMessage(ctx, msg))

		recall := proc.OnStop(ctx, "ag-running", "sess-running")

		require.NotNil(t, recall, "OnStop must return a recall string when tool not invoked")
		state, _ := GetAgentStateForTest(proc, "ag-running")
		assert.Equal(t, AgentStatusRunning, state.Status,
			"agent status must remain Running after recall injection — same session continues")
	})

	// When max retries exceeded, markDelivered runs and sets Ready (not Running).
	t.Run("Agent Becomes Ready After Max Retries Exceeded", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-maxrun", "ag-maxrun", "/ws", "team")
		SetSessionForTest(proc, "ag-maxrun", "sess-maxrun")
		SetAgentStatusForTest(proc, "ag-maxrun", AgentStatusRunning)

		// retries=4 exceeds maxMandatoryToolRetries (3).
		msg := &message.Message{
			ID: "maxrun-1", To: "ag-maxrun", From: "sender",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestTypeExecute,
			Status:      message.StatusProcessing,
			Retries: 4, SentTime: 100, Prompt: "task", Refs: "{}",
		}
		require.NoError(t, msgStore.InsertMessage(ctx, msg))

		recall := proc.OnStop(ctx, "ag-maxrun", "sess-maxrun")

		require.Nil(t, recall, "max retries exceeded must not recall")
		state, _ := GetAgentStateForTest(proc, "ag-maxrun")
		assert.Equal(t, AgentStatusReady, state.Status,
			"agent must become Ready when markDelivered runs after max retries")
	})
}

// ─── TestQueueSweepRetry ──────────────────────────────────────────────────────

func TestQueueSweepRetry(t *testing.T) {
	ctx := context.Background()

	// Stale queued message with Retries < maxQueueRetries (3) is re-queued to StatusInQueue.
	t.Run("Stale Message With Low Retries Re-Queued", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)

		msg := &message.Message{
			ID: "sweep-requeue", To: "ag-sweep", From: "sender",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestTypeExecute,
			Status:      statusQueued,
			Retries: 0, QueueTime: 1, // very old — will be treated as stale
			SentTime: 100, Prompt: "do task", Refs: "{}",
		}
		require.NoError(t, msgStore.InsertMessage(ctx, msg))
		// InsertMessage may set status from the struct; verify it is queued in DB.
		msg.Status = statusQueued
		msg.QueueTime = 1
		require.NoError(t, msgStore.UpdateMessage(ctx, msg))

		RunQueueSweepOnceForTest(proc, ctx)

		got, err := msgStore.GetMessage(ctx, "sweep-requeue")
		require.NoError(t, err)
		assert.Equal(t, message.StatusInQueue, got.Status,
			"stale queued message with retries < maxQueueRetries must be re-queued to StatusInQueue")
		assert.Equal(t, int64(0), got.QueueTime, "re-queued message must have QueueTime cleared")
	})

	// Stale queued message with Retries >= maxQueueRetries is permanently failed.
	t.Run("Stale Message At Max Retries Marked Failed", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)

		msg := &message.Message{
			ID: "sweep-fail", To: "ag-sweep-f", From: "sender",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestTypeExecute,
			Status:      statusQueued,
			Retries: maxQueueRetries, // exactly at max — should fail
			QueueTime: 1,
			SentTime: 100, Prompt: "do task", Refs: "{}",
		}
		require.NoError(t, msgStore.InsertMessage(ctx, msg))
		msg.Status = statusQueued
		msg.QueueTime = 1
		require.NoError(t, msgStore.UpdateMessage(ctx, msg))

		RunQueueSweepOnceForTest(proc, ctx)

		got, err := msgStore.GetMessage(ctx, "sweep-fail")
		require.NoError(t, err)
		assert.Equal(t, message.StatusFailed, got.Status,
			"stale queued message with retries >= maxQueueRetries must be marked StatusFailed")
	})

	// Message with zero queue_time (not yet queued) must not be swept.
	t.Run("Message With Zero QueueTime Not Swept", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)

		msg := &message.Message{
			ID: "sweep-zero-qt", To: "ag-zero", From: "sender",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestTypeExecute,
			Status:      statusQueued,
			Retries: 0, QueueTime: 0, // QueueTime=0 → not stale
			SentTime: 100, Prompt: "task", Refs: "{}",
		}
		require.NoError(t, msgStore.InsertMessage(ctx, msg))
		msg.Status = statusQueued
		msg.QueueTime = 0
		require.NoError(t, msgStore.UpdateMessage(ctx, msg))

		RunQueueSweepOnceForTest(proc, ctx)

		got, err := msgStore.GetMessage(ctx, "sweep-zero-qt")
		require.NoError(t, err)
		assert.Equal(t, statusQueued, got.Status,
			"message with zero queue_time must not be swept (not yet timed out)")
	})
}
