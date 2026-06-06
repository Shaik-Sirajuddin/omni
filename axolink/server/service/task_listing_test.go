//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func newTaskListingService(t *testing.T) *Service {
	t.Helper()
	return New(
		message.WithTestDB(t),
		nil, // delivery not needed
		nil, // agentStore not needed
		nil, // teamStore not needed
		func() int64 { return 1234 },
	)
}

func seedTaskMsg(t *testing.T, ctx context.Context, svc *Service, id, to, taskID, creatorID string, rt message.RequestType, status message.Status) {
	t.Helper()
	msg := &message.Message{
		ID:             id,
		To:             to,
		From:           "sender",
		FromSpec:       message.SpecOmni,
		ToSpec:         message.SpecOmni,
		RequestType:    rt,
		Status:         status,
		TaskID:         taskID,
		CreatorAgentID: creatorID,
		ShouldReply:    false,
		Prompt:         "prompt",
		Refs:           "{}",
		SentTime:       100,
	}
	require.NoError(t, svc.msgStore.InsertMessage(ctx, msg))
	if status != message.StatusInQueue {
		msg.Status = status
		require.NoError(t, svc.msgStore.UpdateMessage(ctx, msg))
	}
}

// ─── TestListTasks ────────────────────────────────────────────────────────────

func TestListTasks(t *testing.T) {
	ctx := context.Background()

	t.Run("Empty Agent Returns Empty List", func(t *testing.T) {
		svc := newTaskListingService(t)
		_, err := svc.ListTasks(ctx, "")
		require.Error(t, err, "empty agent_id must return an error")
	})

	t.Run("No Tasks Returns Empty Slice", func(t *testing.T) {
		svc := newTaskListingService(t)
		resp, err := svc.ListTasks(ctx, "ag-empty")
		require.NoError(t, err)
		assert.Empty(t, resp.Tasks, "no task messages must return empty tasks slice")
		assert.Equal(t, 0, resp.Count)
	})

	t.Run("Messages Without Task ID Are Not Included", func(t *testing.T) {
		svc := newTaskListingService(t)
		// Insert a message with no task_id — must not appear in ListTasks.
		seedTaskMsg(t, ctx, svc, "msg-notask", "ag-notask", "", "", message.RequestTypeExecute, message.StatusInQueue)
		resp, err := svc.ListTasks(ctx, "ag-notask")
		require.NoError(t, err)
		assert.Empty(t, resp.Tasks, "messages with empty task_id must not appear in ListTasks")
	})

	t.Run("Grouped By Task ID With Correct Counts", func(t *testing.T) {
		svc := newTaskListingService(t)
		// Task T1: 2 in_queue, 1 delivered.
		seedTaskMsg(t, ctx, svc, "t1-a", "ag-grouped", "task-1", "creator-1", message.RequestTypeExecute, message.StatusInQueue)
		seedTaskMsg(t, ctx, svc, "t1-b", "ag-grouped", "task-1", "creator-1", message.RequestType("query"), message.StatusInQueue)
		seedTaskMsg(t, ctx, svc, "t1-c", "ag-grouped", "task-1", "creator-1", message.RequestType("query"), message.StatusDelivered)
		// Task T2: 1 failed.
		seedTaskMsg(t, ctx, svc, "t2-a", "ag-grouped", "task-2", "creator-2", message.RequestTypeExecute, message.StatusFailed)

		resp, err := svc.ListTasks(ctx, "ag-grouped")
		require.NoError(t, err)
		require.Len(t, resp.Tasks, 2, "must group messages into 2 distinct tasks")
		assert.Equal(t, 2, resp.Count)

		taskMap := make(map[string]TaskSummary)
		for _, ts := range resp.Tasks {
			taskMap[ts.TaskID] = ts
		}

		t1 := taskMap["task-1"]
		assert.Equal(t, "creator-1", t1.CreatorAgentID)
		assert.Equal(t, 3, t1.TotalMessages)
		assert.Equal(t, 2, t1.PendingCount, "in_queue messages must be counted as pending")
		assert.Equal(t, 1, t1.DeliveredCount)
		assert.Equal(t, 0, t1.FailedCount)

		t2 := taskMap["task-2"]
		assert.Equal(t, 1, t2.TotalMessages)
		assert.Equal(t, 0, t2.PendingCount)
		assert.Equal(t, 0, t2.DeliveredCount)
		assert.Equal(t, 1, t2.FailedCount)
	})

	t.Run("Scoped To Agent — Other Agents Not Included", func(t *testing.T) {
		svc := newTaskListingService(t)
		seedTaskMsg(t, ctx, svc, "msg-ag1", "ag-scope-1", "task-x", "c", message.RequestTypeExecute, message.StatusInQueue)
		seedTaskMsg(t, ctx, svc, "msg-ag2", "ag-scope-2", "task-x", "c", message.RequestTypeExecute, message.StatusInQueue)

		resp, err := svc.ListTasks(ctx, "ag-scope-1")
		require.NoError(t, err)
		require.Len(t, resp.Tasks, 1)
		assert.Equal(t, "task-x", resp.Tasks[0].TaskID, "ListTasks must only include messages for the requested agent")
	})
}

// ─── TestGetTask ──────────────────────────────────────────────────────────────

func TestGetTask(t *testing.T) {
	ctx := context.Background()

	t.Run("Empty AgentID Returns Error", func(t *testing.T) {
		svc := newTaskListingService(t)
		_, err := svc.GetTask(ctx, "", "task-1")
		require.Error(t, err)
	})

	t.Run("Empty TaskID Returns Error", func(t *testing.T) {
		svc := newTaskListingService(t)
		_, err := svc.GetTask(ctx, "ag-1", "")
		require.Error(t, err)
	})

	t.Run("No Messages Returns Empty Detail", func(t *testing.T) {
		svc := newTaskListingService(t)
		resp, err := svc.GetTask(ctx, "ag-detail", "task-missing")
		require.NoError(t, err)
		assert.Equal(t, "task-missing", resp.TaskID)
		assert.Empty(t, resp.Messages)
		assert.Equal(t, 0, resp.Count)
	})

	t.Run("Returns All Messages For Task Ordered By SentTime", func(t *testing.T) {
		svc := newTaskListingService(t)
		// Insert in reverse sent_time order.
		msg2 := &message.Message{
			ID: "detail-msg2", To: "ag-detail2", From: "s",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestType("query"), Status: message.StatusInQueue,
			TaskID: "task-detail", CreatorAgentID: "c", SentTime: 200, Prompt: "q", Refs: "{}",
		}
		msg1 := &message.Message{
			ID: "detail-msg1", To: "ag-detail2", From: "s",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestTypeExecute, Status: message.StatusDelivered,
			TaskID: "task-detail", CreatorAgentID: "c", SentTime: 100, Prompt: "exec", Refs: "{}",
		}
		require.NoError(t, svc.msgStore.InsertMessage(ctx, msg1))
		require.NoError(t, svc.msgStore.InsertMessage(ctx, msg2))
		msg1.Status = message.StatusDelivered
		require.NoError(t, svc.msgStore.UpdateMessage(ctx, msg1))

		resp, err := svc.GetTask(ctx, "ag-detail2", "task-detail")
		require.NoError(t, err)
		assert.Equal(t, "task-detail", resp.TaskID)
		assert.Equal(t, "c", resp.CreatorAgentID)
		require.Len(t, resp.Messages, 2)
		assert.Equal(t, 2, resp.Count)
		assert.Equal(t, "detail-msg1", resp.Messages[0].ID, "messages must be ordered by sent_time ASC")
		assert.Equal(t, "detail-msg2", resp.Messages[1].ID)
	})

	t.Run("Only Returns Messages For Matching Task", func(t *testing.T) {
		svc := newTaskListingService(t)
		seedTaskMsg(t, ctx, svc, "msg-task-a", "ag-filter", "task-a", "c", message.RequestTypeExecute, message.StatusInQueue)
		seedTaskMsg(t, ctx, svc, "msg-task-b", "ag-filter", "task-b", "c", message.RequestTypeExecute, message.StatusInQueue)

		resp, err := svc.GetTask(ctx, "ag-filter", "task-a")
		require.NoError(t, err)
		require.Len(t, resp.Messages, 1)
		assert.Equal(t, "msg-task-a", resp.Messages[0].ID)
	})
}

// ─── TestListActiveTask ───────────────────────────────────────────────────────

func TestListActiveTask(t *testing.T) {
	ctx := context.Background()

	t.Run("Empty AgentID Returns Error", func(t *testing.T) {
		svc := newTaskListingService(t)
		_, err := svc.ListActiveTask(ctx, "")
		require.Error(t, err)
	})

	t.Run("No Active Tasks Returns Empty Slice", func(t *testing.T) {
		svc := newTaskListingService(t)
		resp, err := svc.ListActiveTask(ctx, "ag-noactive")
		require.NoError(t, err)
		assert.Empty(t, resp.Tasks)
		assert.Equal(t, 0, resp.Count)
	})

	t.Run("Returns Only In-Flight Messages With Task ID", func(t *testing.T) {
		svc := newTaskListingService(t)
		// In-flight: in_queue with task_id.
		seedTaskMsg(t, ctx, svc, "active-1", "ag-active", "task-act", "c", message.RequestTypeExecute, message.StatusInQueue)
		// Delivered — must not appear.
		seedTaskMsg(t, ctx, svc, "delivered-1", "ag-active", "task-act", "c", message.RequestType("query"), message.StatusDelivered)
		// In_queue but NO task_id — must not appear.
		seedTaskMsg(t, ctx, svc, "no-task", "ag-active", "", "", message.RequestTypeExecute, message.StatusInQueue)

		resp, err := svc.ListActiveTask(ctx, "ag-active")
		require.NoError(t, err)
		require.Len(t, resp.Tasks, 1, "only in-flight messages with task_id must be returned")
		assert.Equal(t, 1, resp.Count)
		assert.Equal(t, "task-act", resp.Tasks[0].TaskID)
		assert.Equal(t, "active-1", resp.Tasks[0].MessageID)
	})

	t.Run("Includes InQueue Queued And Processing Statuses", func(t *testing.T) {
		svc := newTaskListingService(t)
		seedTaskMsg(t, ctx, svc, "act-inqueue", "ag-statuses", "task-st", "c", message.RequestTypeExecute, message.StatusInQueue)

		// Insert queued and processing messages directly.
		queued := &message.Message{
			ID: "act-queued", To: "ag-statuses", From: "s",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestTypeExecute, Status: message.StatusInQueue,
			TaskID: "task-st", CreatorAgentID: "c", SentTime: 101, Prompt: "q", Refs: "{}",
		}
		require.NoError(t, svc.msgStore.InsertMessage(ctx, queued))
		queued.Status = message.Status("queued")
		require.NoError(t, svc.msgStore.UpdateMessage(ctx, queued))

		processing := &message.Message{
			ID: "act-processing", To: "ag-statuses", From: "s",
			FromSpec: message.SpecOmni, ToSpec: message.SpecOmni,
			RequestType: message.RequestTypeExecute, Status: message.StatusInQueue,
			TaskID: "task-st", CreatorAgentID: "c", SentTime: 102, Prompt: "p", Refs: "{}",
		}
		require.NoError(t, svc.msgStore.InsertMessage(ctx, processing))
		processing.Status = message.StatusProcessing
		require.NoError(t, svc.msgStore.UpdateMessage(ctx, processing))

		resp, err := svc.ListActiveTask(ctx, "ag-statuses")
		require.NoError(t, err)
		assert.Len(t, resp.Tasks, 3, "in_queue, queued, and processing messages must all be returned")
	})

	t.Run("Scoped To Agent", func(t *testing.T) {
		svc := newTaskListingService(t)
		seedTaskMsg(t, ctx, svc, "active-ag1", "ag-act-1", "task-z", "c", message.RequestTypeExecute, message.StatusInQueue)
		seedTaskMsg(t, ctx, svc, "active-ag2", "ag-act-2", "task-z", "c", message.RequestTypeExecute, message.StatusInQueue)

		resp, err := svc.ListActiveTask(ctx, "ag-act-1")
		require.NoError(t, err)
		require.Len(t, resp.Tasks, 1)
		assert.Equal(t, "active-ag1", resp.Tasks[0].MessageID)
	})
}
