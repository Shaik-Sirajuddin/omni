//go:build unit

package session_test

import (
	"context"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/database"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── TestTaskDeliveryStore ────────────────────────────────────────────────────

func TestTaskDeliveryStore(t *testing.T) {
	ctx := context.Background()

	newStore := func(t *testing.T) session.TaskDeliveryStore {
		t.Helper()
		return session.NewTaskDeliveryStoreFromDB(database.WithTestDB(t))
	}

	t.Run("StartDelivery inserts row and GetInProgress returns it", func(t *testing.T) {
		store := newStore(t)

		err := store.StartDelivery(ctx, "task-1", "agent-1", "msg-1")
		require.NoError(t, err, "StartDelivery must not error")

		deliveries, err := store.GetInProgress(ctx, "agent-1")
		require.NoError(t, err, "GetInProgress must not error")
		require.Len(t, deliveries, 1, "GetInProgress must return the inserted delivery")
		assert.Equal(t, "task-1", deliveries[0].TaskID)
		assert.Equal(t, "agent-1", deliveries[0].AgentID)
		assert.Equal(t, "msg-1", deliveries[0].LastMessageID)
		assert.Equal(t, "in_progress", deliveries[0].Status)
		assert.Greater(t, deliveries[0].UpdatedAt, int64(0), "UpdatedAt must be set")
	})

	t.Run("StartDelivery twice upserts: last_message_id and updated_at updated", func(t *testing.T) {
		store := newStore(t)

		require.NoError(t, store.StartDelivery(ctx, "task-up", "agent-up", "msg-first"))
		first, err := store.GetInProgress(ctx, "agent-up")
		require.NoError(t, err)
		require.Len(t, first, 1)
		firstUpdatedAt := first[0].UpdatedAt

		require.NoError(t, store.StartDelivery(ctx, "task-up", "agent-up", "msg-second"))
		second, err := store.GetInProgress(ctx, "agent-up")
		require.NoError(t, err)
		require.Len(t, second, 1, "upsert must not create a duplicate row")
		assert.Equal(t, "msg-second", second[0].LastMessageID, "upsert must update last_message_id")
		assert.GreaterOrEqual(t, second[0].UpdatedAt, firstUpdatedAt, "upsert must update updated_at")
	})

	t.Run("CompleteDelivery: status becomes completed, not in GetInProgress", func(t *testing.T) {
		store := newStore(t)

		require.NoError(t, store.StartDelivery(ctx, "task-done", "agent-done", "msg-done"))
		err := store.CompleteDelivery(ctx, "task-done", "agent-done")
		require.NoError(t, err, "CompleteDelivery must not error")

		deliveries, err := store.GetInProgress(ctx, "agent-done")
		require.NoError(t, err)
		assert.Empty(t, deliveries, "completed delivery must not appear in GetInProgress")
	})

	t.Run("GetInProgress with no rows returns empty slice", func(t *testing.T) {
		store := newStore(t)

		deliveries, err := store.GetInProgress(ctx, "agent-none")
		require.NoError(t, err, "GetInProgress with no rows must not error")
		assert.NotNil(t, deliveries, "GetInProgress with no rows must return non-nil slice")
		assert.Empty(t, deliveries, "GetInProgress with no rows must return empty slice")
	})

	t.Run("GetInProgress returns only in_progress deliveries for the agent", func(t *testing.T) {
		store := newStore(t)

		require.NoError(t, store.StartDelivery(ctx, "task-a", "agent-multi", "msg-a"))
		require.NoError(t, store.StartDelivery(ctx, "task-b", "agent-multi", "msg-b"))
		require.NoError(t, store.CompleteDelivery(ctx, "task-b", "agent-multi"))

		deliveries, err := store.GetInProgress(ctx, "agent-multi")
		require.NoError(t, err)
		require.Len(t, deliveries, 1, "only in_progress deliveries should be returned")
		assert.Equal(t, "task-a", deliveries[0].TaskID)
	})

	t.Run("GetInProgress scoped to agent: other agents not returned", func(t *testing.T) {
		store := newStore(t)

		require.NoError(t, store.StartDelivery(ctx, "task-x", "agent-x", "msg-x"))
		require.NoError(t, store.StartDelivery(ctx, "task-y", "agent-y", "msg-y"))

		deliveries, err := store.GetInProgress(ctx, "agent-x")
		require.NoError(t, err)
		require.Len(t, deliveries, 1)
		assert.Equal(t, "agent-x", deliveries[0].AgentID, "GetInProgress must only return deliveries for the requested agent")
	})
}
