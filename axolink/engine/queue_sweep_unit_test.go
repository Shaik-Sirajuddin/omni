//go:build unit

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			Retries:     0, QueueTime: 1, // very old — will be treated as stale
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
			Retries:     maxQueueRetries, // exactly at max — should fail
			QueueTime:   1,
			SentTime:    100, Prompt: "do task", Refs: "{}",
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
			Retries:     0, QueueTime: 0, // QueueTime=0 → not stale
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

// ─── TestOnStopRecallIncrementsRetries ────────────────────────────────────────

// TestOnStopRecallIncrementsRetries verifies that each recall injection in OnStop
// increments msg.Retries, so the maxMandatoryToolRetries guard terminates the loop.

func TestQueueTimeResetNoDuplicateDelivery(t *testing.T) {
	ctx := context.Background()

	t.Run("Delivered Message Not Overwritten By QueueTime Reset", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		cli := newLocalBlockingCLI()
		proc := New(msgStore, WithTestBinary(cli))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-nodup", "ag-nodup", "/ws", "team")

		// Seed an instant message for the agent.
		msg := &message.Message{
			ID: "nodup-1", To: "ag-nodup", From: "sender",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestType("instant"),
			Status:      message.StatusInQueue,
			SentTime:    100, Prompt: "response payload", Refs: "{}",
		}
		require.NoError(t, msgStore.InsertMessage(ctx, msg))

		proc.MessageArrived(ctx, "sender", "ag-nodup")
		e1 := cli.waitForExec(t)

		// Simulate OnStop marking the message delivered (as markDelivered would do).
		fresh, err := msgStore.GetMessage(ctx, "nodup-1")
		require.NoError(t, err)
		now := int64(9999)
		fresh.Status = message.StatusDelivered
		fresh.DeliveryTime = &now
		require.NoError(t, msgStore.UpdateMessage(ctx, fresh))

		// Release ExecInSession — triggers queue_time reset in executeLoop.
		close(e1.relCh)

		// Give executeLoop goroutine time to finish the queue_time reset loop.
		time.Sleep(50 * time.Millisecond)

		got, err := msgStore.GetMessage(ctx, "nodup-1")
		require.NoError(t, err)
		assert.Equal(t, message.StatusDelivered, got.Status,
			"queue_time reset must not overwrite StatusDelivered back to statusQueued")
	})
}
