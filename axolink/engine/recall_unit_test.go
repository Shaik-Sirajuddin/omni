//go:build unit

package engine

import (
	"context"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			Retries:     1, SentTime: 100, Prompt: "question?", Refs: "{}",
		}
		require.NoError(t, msgStore.InsertMessage(ctx, msg))

		proc.MessageArrived(ctx, "sender", "ag-prep")
		e1 := cli.waitForExec(t)

		// Preprocessing recall uses buildWarmUpPrompt (YAML), not plain-text buildRecallPrompt.
		// Check for YAML field presence instead of the plain-text callback notice.
		assert.Contains(t, e1.prompt, "messages:",
			"preprocessing recall must use YAML format (buildWarmUpPrompt), not plain text (buildRecallPrompt)")
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
			SentTime:    50, Prompt: "steer", Refs: "{}",
		}
		// In_queue execute to give executeLoop something to pick normally.
		exec := &message.Message{
			ID: "exec-after", To: "ag-instant", From: "sender",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestTypeExecute,
			Status:      message.StatusInQueue,
			SentTime:    100, Prompt: "do work", Refs: "{}",
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
			Retries:     1, SentTime: 100, Prompt: "task", Refs: "{}",
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
			Retries:     4, SentTime: 100, Prompt: "task", Refs: "{}",
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

func TestOnStopRecallIncrementsRetries(t *testing.T) {
	ctx := context.Background()

	t.Run("Retries Incremented On Each Recall Injection", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-recall-ret", "ag-recall-ret", "/ws", "team")
		SetSessionForTest(proc, "ag-recall-ret", "sess-recall-ret")
		SetAgentStatusForTest(proc, "ag-recall-ret", AgentStatusRunning)

		msg := &message.Message{
			ID: "recall-ret-1", To: "ag-recall-ret", From: "sender",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestTypeExecute,
			Status:      message.StatusProcessing,
			Retries:     0, SentTime: 100, Prompt: "task", Refs: "{}",
		}
		require.NoError(t, msgStore.InsertMessage(ctx, msg))

		// First recall injection — retries 0 → 1.
		recall := proc.OnStop(ctx, "ag-recall-ret", "sess-recall-ret")
		require.NotNil(t, recall, "OnStop must return recall when tool not invoked")

		got, err := msgStore.GetMessage(ctx, "recall-ret-1")
		require.NoError(t, err)
		assert.Equal(t, 1, got.Retries, "first recall must increment retries to 1")

		// Second recall injection — retries 1 → 2.
		SetSessionForTest(proc, "ag-recall-ret", "sess-recall-ret")
		SetAgentStatusForTest(proc, "ag-recall-ret", AgentStatusRunning)
		recall2 := proc.OnStop(ctx, "ag-recall-ret", "sess-recall-ret")
		require.NotNil(t, recall2, "second recall must still fire (retries=1 <= max=3)")

		got2, err := msgStore.GetMessage(ctx, "recall-ret-1")
		require.NoError(t, err)
		assert.Equal(t, 2, got2.Retries, "second recall must increment retries to 2")
	})

	t.Run("Recall Stops After Max Retries", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-recall-max", "ag-recall-max", "/ws", "team")
		SetSessionForTest(proc, "ag-recall-max", "sess-recall-max")
		SetAgentStatusForTest(proc, "ag-recall-max", AgentStatusRunning)

		// retries = maxMandatoryToolRetries+1: guard should NOT recall.
		msg := &message.Message{
			ID: "recall-max-1", To: "ag-recall-max", From: "sender",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestTypeExecute,
			Status:      message.StatusProcessing,
			Retries:     maxMandatoryToolRetries + 1, SentTime: 100, Prompt: "task", Refs: "{}",
		}
		require.NoError(t, msgStore.InsertMessage(ctx, msg))

		recall := proc.OnStop(ctx, "ag-recall-max", "sess-recall-max")
		assert.Nil(t, recall, "recall must not fire when retries exceed maxMandatoryToolRetries")

		state, _ := GetAgentStateForTest(proc, "ag-recall-max")
		assert.Equal(t, AgentStatusReady, state.Status, "agent must become Ready after max retries")
	})
}

// ─── TestQueueTimeResetNoDuplicateDelivery ────────────────────────────────────

// TestQueueTimeResetNoDuplicateDelivery verifies that the queue_time reset after
// ExecInSession does not overwrite a StatusDelivered message back to statusQueued.
// Regression test for the duplicate-delivery bug: the stale local msg (statusQueued)
// was passed to UpdateMessage, clobbering StatusDelivered set by OnStop.
