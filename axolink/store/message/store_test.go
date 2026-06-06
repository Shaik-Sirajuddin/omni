//go:build unit

package message_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/database"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageStore(t *testing.T) {
	ctx := context.Background()

	t.Run("Insert And Get Message", func(t *testing.T) {
		db := database.WithTestDB(t)
		store := message.New(db)
		deliveryTime := int64(1200)
		msg := testMessage("msg-insert", "engine", "head", 1000)
		msg.DeliveryTime = &deliveryTime
		msg.Status = message.StatusDelivered

		err := store.InsertMessage(ctx, msg)
		require.NoError(t, err, "InsertMessage should persist a valid message")

		got, err := store.GetMessage(ctx, msg.ID)
		require.NoError(t, err, "GetMessage should load an inserted message")
		assertMessageEqual(t, msg, got)
	})

	t.Run("Get Workspace For Agent", func(t *testing.T) {
		db := database.WithTestDB(t)
		store := message.New(db)
		msg := testMessage("msg-workspace", "engine", "head", 1000)
		msg.Workspace = "/workspace"

		require.NoError(t, store.InsertMessage(ctx, msg), "InsertMessage should seed workspace data")

		got, err := store.GetWorkspaceForAgent(ctx, "engine")
		require.NoError(t, err, "GetWorkspaceForAgent should load workspace from messages")
		assert.Equal(t, "/workspace", got, "GetWorkspaceForAgent should return the stored workspace")
	})

	t.Run("Update Message", func(t *testing.T) {
		db := database.WithTestDB(t)
		store := message.New(db)
		msg := testMessage("msg-update", "engine", "head", 1000)
		require.NoError(t, store.InsertMessage(ctx, msg), "InsertMessage should seed update test data")

		deliveryTime := int64(3000)
		msg.Prompt = "updated prompt"
		msg.Status = message.StatusDelivered
		msg.Retries = 2
		msg.DeliveryTime = &deliveryTime

		err := store.UpdateMessage(ctx, msg)
		require.NoError(t, err, "UpdateMessage should update an existing message")

		got, err := store.GetMessage(ctx, msg.ID)
		require.NoError(t, err, "GetMessage should load an updated message")
		assertMessageEqual(t, msg, got)
	})

	t.Run("Insert Messages Group", func(t *testing.T) {
		db := database.WithTestDB(t)
		store := message.New(db)
		groupID := "group-1"
		msgs := []*message.Message{
			testMessage("msg-group-1", "engine", "head", 1000),
			testMessage("msg-group-2", "head", "engine", 2000),
		}

		err := store.InsertMessagesGroup(ctx, groupID, msgs)
		require.NoError(t, err, "InsertMessagesGroup should persist a valid group")

		got, err := store.GetMessages(ctx, groupID)
		require.NoError(t, err, "GetMessages should load group messages")
		require.Len(t, got, 2, "GetMessages should return every message in the group")
		assert.Equal(t, []string{"msg-group-1", "msg-group-2"}, messageIDs(got), "GetMessages should order messages by sent time")
		assert.Equal(t, groupID, got[0].GroupID, "GetMessages should include the group id on the first message")
		assert.Equal(t, groupID, got[1].GroupID, "GetMessages should include the group id on the second message")
	})

	t.Run("Conversation Messages With Pagination", func(t *testing.T) {
		db := database.WithTestDB(t)
		store := message.New(db)
		insertMessages(t, ctx, store,
			testMessage("msg-conv-1", "engine", "head", 1000),
			testMessage("msg-other", "infra", "head", 1500),
			testMessage("msg-conv-2", "head", "engine", 2000),
			testMessage("msg-conv-3", "engine", "head", 3000),
		)

		got, err := store.GetConversationMessages(ctx, "head", "engine", message.Page{Offset: 1, Limit: 2})
		require.NoError(t, err, "GetConversationMessages should load conversation messages")
		assert.Equal(t, []string{"msg-conv-2", "msg-conv-3"}, messageIDs(got), "GetConversationMessages should page ordered conversation messages")
		assert.Subset(t, messageIDs(got), []string{"msg-conv-2", "msg-conv-3"}, "GetConversationMessages result should contain the expected page ids")
	})

	t.Run("Validation Errors", func(t *testing.T) {
		db := database.WithTestDB(t)
		store := message.New(db)

		oversized := testMessage("msg-too-large", "engine", "head", 1000)
		oversized.Prompt = strings.Repeat("x", message.MaxPromptBytes+1)
		err := store.InsertMessage(ctx, oversized)
		require.Error(t, err, "InsertMessage should reject prompts over the maximum size")

		msgs := make([]*message.Message, message.MaxGroupMessages+1)
		for index := range msgs {
			msgs[index] = testMessage(fmt.Sprintf("msg-large-group-%d", index), "engine", "head", int64(index))
		}
		err = store.InsertMessagesGroup(ctx, "group-too-large", msgs)
		require.Error(t, err, "InsertMessagesGroup should reject groups over the maximum size")
	})

	t.Run("Missing Message Errors", func(t *testing.T) {
		db := database.WithTestDB(t)
		store := message.New(db)
		msg := testMessage("missing", "engine", "head", 1000)

		_, err := store.GetMessage(ctx, msg.ID)
		require.ErrorIs(t, err, sql.ErrNoRows, "GetMessage should return sql.ErrNoRows for missing messages")

		err = store.UpdateMessage(ctx, msg)
		require.Error(t, err, "UpdateMessage should reject missing messages")
		assert.Contains(t, err.Error(), "not found", "UpdateMessage error should explain the missing message")
	})
}

func testMessage(id, to, by string, sentTime int64) *message.Message {
	return &message.Message{
		ID:          id,
		To:          to,
		From:        by,
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: message.RequestTypeQuery,
		ShouldReply: true,
		Prompt:      "test prompt",
		Refs:        "{}",
		Status:      message.StatusInQueue,
		SentTime:    sentTime,
	}
}

func insertMessages(t *testing.T, ctx context.Context, store message.MessageStore, msgs ...*message.Message) {
	t.Helper()
	for _, msg := range msgs {
		require.NoError(t, store.InsertMessage(ctx, msg), "InsertMessage should seed conversation test data")
	}
}

func messageIDs(msgs []*message.Message) []string {
	ids := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		ids = append(ids, msg.ID)
	}
	return ids
}

func assertMessageEqual(t *testing.T, expected, got *message.Message) {
	t.Helper()
	assert.Equal(t, expected.ID, got.ID, "Message ID should match")
	assert.Equal(t, expected.To, got.To, "Message recipient should match")
	assert.Equal(t, expected.From, got.From, "Message sender should match")
	assert.Equal(t, expected.FromSpec, got.FromSpec, "Message sender spec should match")
	assert.Equal(t, expected.ToSpec, got.ToSpec, "Message recipient spec should match")
	assert.Equal(t, expected.RequestType, got.RequestType, "Message request type should match")
	assert.Equal(t, expected.IsResponse, got.IsResponse, "Message response flag should match")
	assert.Equal(t, expected.ShouldReply, got.ShouldReply, "Message reply flag should match")
	assert.Equal(t, expected.RespondedTo, got.RespondedTo, "Message response target should match")
	assert.Equal(t, expected.Prompt, got.Prompt, "Message prompt should match")
	assert.Equal(t, expected.Refs, got.Refs, "Message refs should match")
	assert.Equal(t, expected.Workspace, got.Workspace, "Message workspace should match")
	assert.Equal(t, expected.Status, got.Status, "Message status should match")
	assert.Equal(t, expected.Retries, got.Retries, "Message retry count should match")
	assert.Equal(t, expected.DeliveryTime, got.DeliveryTime, "Message delivery time should match")
	assert.Equal(t, expected.SentTime, got.SentTime, "Message sent time should match")
	assert.Equal(t, expected.GroupID, got.GroupID, "Message group id should match")
}
