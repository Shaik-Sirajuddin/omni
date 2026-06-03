//go:build unit

package engine

import (
	"testing"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
)

// ─── TestIsMixedBatch ─────────────────────────────────────────────────────────

func TestIsMixedBatch(t *testing.T) {
	t.Run("nil is false", func(t *testing.T) {
		assert.False(t, isMixedBatch(nil))
	})

	t.Run("empty is false", func(t *testing.T) {
		assert.False(t, isMixedBatch([]*message.Message{}))
	})

	t.Run("single execute is false", func(t *testing.T) {
		assert.False(t, isMixedBatch([]*message.Message{
			{RequestType: reqTypeExecute},
		}))
	})

	t.Run("two queries are false", func(t *testing.T) {
		assert.False(t, isMixedBatch([]*message.Message{
			{RequestType: reqTypeQuery},
			{RequestType: reqTypeQuery},
		}))
	})

	t.Run("three instants are false", func(t *testing.T) {
		assert.False(t, isMixedBatch([]*message.Message{
			{RequestType: reqTypeInstant},
			{RequestType: reqTypeInstant},
			{RequestType: reqTypeInstant},
		}))
	})

	t.Run("execute and query is true", func(t *testing.T) {
		assert.True(t, isMixedBatch([]*message.Message{
			{RequestType: reqTypeExecute},
			{RequestType: reqTypeQuery},
		}))
	})

	t.Run("execute and instant is true", func(t *testing.T) {
		assert.True(t, isMixedBatch([]*message.Message{
			{RequestType: reqTypeExecute},
			{RequestType: reqTypeInstant},
		}))
	})

	t.Run("query and instant is true", func(t *testing.T) {
		assert.True(t, isMixedBatch([]*message.Message{
			{RequestType: reqTypeQuery},
			{RequestType: reqTypeInstant},
		}))
	})

	t.Run("only first element differs triggers true", func(t *testing.T) {
		assert.True(t, isMixedBatch([]*message.Message{
			{RequestType: reqTypeExecute},
			{RequestType: reqTypeQuery},
			{RequestType: reqTypeQuery},
		}))
	})
}

// ─── TestBuildRecallPrompt ────────────────────────────────────────────────────

func TestBuildRecallPrompt(t *testing.T) {
	t.Run("nil returns non-empty fallback", func(t *testing.T) {
		assert.NotEmpty(t, buildRecallPrompt(nil))
	})

	t.Run("empty slice returns non-empty fallback", func(t *testing.T) {
		assert.NotEmpty(t, buildRecallPrompt([]*message.Message{}))
	})

	t.Run("single message embeds message_id", func(t *testing.T) {
		msgs := []*message.Message{{ID: "msg-solo"}}
		recall := buildRecallPrompt(msgs)
		assert.Contains(t, recall, "msg-solo", "single recall must embed the message_id")
	})

	t.Run("single message references send_response", func(t *testing.T) {
		msgs := []*message.Message{{ID: "msg-solo"}}
		recall := buildRecallPrompt(msgs)
		assert.Contains(t, recall, "send_response", "single recall must reference send_response tool")
	})

	t.Run("single message also mentions send_response_batch for single-item array", func(t *testing.T) {
		msgs := []*message.Message{{ID: "msg-solo"}}
		recall := buildRecallPrompt(msgs)
		assert.Contains(t, recall, "send_response_batch", "single recall must mention send_response_batch")
	})

	t.Run("batch includes all message_ids", func(t *testing.T) {
		msgs := []*message.Message{{ID: "msg-a"}, {ID: "msg-b"}, {ID: "msg-c"}}
		recall := buildRecallPrompt(msgs)
		assert.Contains(t, recall, "msg-a", "batch recall must include first id")
		assert.Contains(t, recall, "msg-b", "batch recall must include second id")
		assert.Contains(t, recall, "msg-c", "batch recall must include third id")
	})

	t.Run("batch references send_response_batch", func(t *testing.T) {
		msgs := []*message.Message{{ID: "msg-a"}, {ID: "msg-b"}}
		recall := buildRecallPrompt(msgs)
		assert.Contains(t, recall, "send_response_batch", "batch recall must reference send_response_batch")
	})

	t.Run("recall discourages send_message and does not use it as a positive instruction", func(t *testing.T) {
		for _, msgs := range [][]*message.Message{
			{{ID: "msg-x"}},
			{{ID: "msg-x"}, {ID: "msg-y"}},
		} {
			recall := buildRecallPrompt(msgs)
			// The recall may reference send_message in a "Do not use" context — what it must
			// NOT do is recommend it as the action to take.
			assert.NotContains(t, recall, "Call `send_message`", "recall must not instruct the agent to call send_message")
		}
	})
}
