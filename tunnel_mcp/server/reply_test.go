//go:build unit

package server

import (
	"context"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplyService(t *testing.T) {
	ctx := context.Background()

	t.Run("MCP Origin Inserts Reversed Response", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		var arrivals []arrival
		msg := &message.Message{
			ID:          "msg-mcp",
			To:          "agent-1",
			From:        "mcp-client",
			FromSpec:    message.SpecOmni,
			ToSpec:      message.SpecOmni,
			RequestType: message.RequestTypeQuery,
			ShouldReply: true,
		}
		reply := newReplyService(msgStore, func(_ context.Context, from, to string) {
			arrivals = append(arrivals, arrival{from: from, to: to})
		})

		err := reply.SendReply(ctx, msg, "agent-1", "agent-one")

		require.NoError(t, err, "MCP reply should insert response without error")
		msgs, listErr := msgStore.GetConversationMessages(ctx, "agent-1", "mcp-client", message.Page{Limit: 10})
		require.NoError(t, listErr, "Inserted MCP reply should be queryable")
		require.Len(t, msgs, 1, "Inserted MCP reply conversation should include one response")
		got := msgs[0]
		assert.Equal(t, "mcp-client", got.To, "Inserted MCP reply should target original MCP sender")
		assert.Equal(t, "agent-1", got.From, "Inserted MCP reply should come from replying agent")
		assert.Equal(t, message.SpecOmni, got.FromSpec, "Inserted MCP reply sender spec should be agent")
		assert.Equal(t, message.SpecOmni, got.ToSpec, "Inserted MCP reply target spec should be agent")
		assert.True(t, got.IsResponse, "Inserted MCP reply should be marked as response")
		assert.False(t, got.ShouldReply, "Inserted MCP reply should not request another reply")
		assert.Equal(t, "msg-mcp", got.RespondedTo, "Inserted MCP reply should reference source message")
		assert.Equal(t, msg.Prompt, got.Prompt, "Inserted MCP reply prompt should forward original message content")
		assert.JSONEq(t, `{"author":"tunnel-mcp","author_agent_id":"agent-1","author_agent_name":"agent-one","reply_to_message_id":"msg-mcp","original_sender":"mcp-client"}`, got.Refs, "Inserted MCP reply refs should include reply metadata with resolved agent name")
		assert.Equal(t, []arrival{{from: "agent-1", to: "mcp-client"}}, arrivals, "Inserted MCP reply should notify engine")
	})

	t.Run("Should Reply False Is Noop", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		reply := newReplyService(msgStore, nil)

		err := reply.SendReply(ctx, &message.Message{ID: "msg-noop", FromSpec: message.SpecOmni}, "agent-1", "agent-one")

		require.NoError(t, err, "Non-reply message should not fail")
		msgs, listErr := msgStore.GetConversationMessages(ctx, "agent-1", "mcp-client", message.Page{Limit: 10})
		require.NoError(t, listErr, "Non-reply message query should not fail")
		assert.Empty(t, msgs, "Non-reply message should not insert response")
	})

	t.Run("Agent Origin Inserts Response And Notifies Engine", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		var arrivals []arrival
		reply := newReplyService(msgStore, func(_ context.Context, from, to string) {
			arrivals = append(arrivals, arrival{from: from, to: to})
		})
		msg := &message.Message{
			ID:          "msg-agent",
			To:          "reply-agent",
			From:        "requesting-agent",
			FromSpec:    message.SpecOmni,
			ToSpec:      message.SpecOmni,
			RequestType: message.RequestTypeQuery,
			ShouldReply: true,
		}

		err := reply.SendReply(ctx, msg, "reply-agent", "reply-agent-name")

		require.NoError(t, err, "Agent reply should insert response without error")
		msgs, listErr := msgStore.GetConversationMessages(ctx, "reply-agent", "requesting-agent", message.Page{Limit: 10})
		require.NoError(t, listErr, "Inserted reply should be queryable")
		require.Len(t, msgs, 1, "Inserted reply conversation should include one response")
		got := msgs[0]
		assert.Equal(t, "requesting-agent", got.To, "Inserted reply should target requesting agent")
		assert.Equal(t, "reply-agent", got.From, "Inserted reply should come from replying agent")
		assert.Equal(t, message.SpecOmni, got.FromSpec, "Inserted reply sender spec should be agent")
		assert.Equal(t, message.SpecOmni, got.ToSpec, "Inserted reply target spec should be agent")
		assert.True(t, got.IsResponse, "Inserted reply should be marked as response")
		assert.False(t, got.ShouldReply, "Inserted reply should not request another reply")
		assert.Equal(t, "msg-agent", got.RespondedTo, "Inserted reply should reference source message")
		assert.Equal(t, msg.Prompt, got.Prompt, "Inserted reply prompt should forward original message content")
		assert.Equal(t, message.StatusInQueue, got.Status, "Inserted reply should be queued")
		assert.Equal(t, []arrival{{from: "reply-agent", to: "requesting-agent"}}, arrivals, "Inserted reply should notify engine")
	})
}
