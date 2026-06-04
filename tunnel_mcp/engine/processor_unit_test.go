//go:build unit

package engine

import (
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// ─── TestBuildWarmUpPrompt ────────────────────────────────────────────────────

func TestBuildWarmUpPrompt(t *testing.T) {
	t.Run("nil returns empty", func(t *testing.T) {
		assert.Empty(t, buildWarmUpPrompt(nil))
	})

	t.Run("empty slice returns empty", func(t *testing.T) {
		assert.Empty(t, buildWarmUpPrompt([]*message.Message{}))
	})

	t.Run("warm_up field absent — removed from payload", func(t *testing.T) {
		msgs := []*message.Message{{ID: "m1", RequestType: reqTypeExecute, Prompt: "do task", Refs: "{}"}}
		assert.NotContains(t, buildWarmUpPrompt(msgs), "warm_up:")
	})

	t.Run("instruction is last field after messages", func(t *testing.T) {
		msgs := []*message.Message{{ID: "m1", RequestType: reqTypeExecute, Prompt: "do task", Refs: "{}"}}
		out := buildWarmUpPrompt(msgs)
		msgIdx := strings.Index(out, "messages:")
		instrIdx := strings.Index(out, "instruction:")
		require.Greater(t, instrIdx, msgIdx, "instruction must be last — after messages section")
	})

	t.Run("homogeneous execute: no per-item request_type, execute instruction", func(t *testing.T) {
		msgs := []*message.Message{
			{ID: "e1", RequestType: reqTypeExecute, Prompt: "task 1", Refs: "{}"},
			{ID: "e2", RequestType: reqTypeExecute, Prompt: "task 2", Refs: "{}"},
		}
		out := buildWarmUpPrompt(msgs)
		assert.NotContains(t, out, "request_type:", "homogeneous batch must not set per-item request_type")
		assert.Contains(t, out, "send_response", "execute instruction must reference send_response tool")
	})

	t.Run("homogeneous query: query-specific instruction, no per-item request_type", func(t *testing.T) {
		msgs := []*message.Message{{ID: "q1", RequestType: reqTypeQuery, Prompt: "what?", Refs: "{}"}}
		out := buildWarmUpPrompt(msgs)
		assert.NotContains(t, out, "request_type:", "homogeneous query batch must not set per-item request_type")
		assert.Contains(t, out, "send_response", "query instruction must reference send_response tool")
	})

	t.Run("mixed batch: per-item request_type set, unified instruction", func(t *testing.T) {
		msgs := []*message.Message{
			{ID: "exec-m", RequestType: reqTypeExecute, Prompt: "task", Refs: "{}"},
			{ID: "query-m", RequestType: reqTypeQuery, Prompt: "question", Refs: "{}"},
		}
		out := buildWarmUpPrompt(msgs)
		assert.Contains(t, out, "request_type:", "mixed batch must set per-item request_type")
		assert.Contains(t, out, warmUpMixedInstruction, "mixed batch must use the unified mixed instruction")
	})

	t.Run("mixed batch: instruction is last after messages", func(t *testing.T) {
		msgs := []*message.Message{
			{ID: "exec-m2", RequestType: reqTypeExecute, Prompt: "task", Refs: "{}"},
			{ID: "query-m2", RequestType: reqTypeQuery, Prompt: "question", Refs: "{}"},
		}
		out := buildWarmUpPrompt(msgs)
		msgIdx := strings.Index(out, "messages:")
		instrIdx := strings.Index(out, "instruction:")
		require.Greater(t, instrIdx, -1, "instruction must be present in mixed batch")
		assert.Greater(t, instrIdx, msgIdx, "instruction must come after messages section in mixed batch")
	})
}

// ─── TestBuildActivePrompt ────────────────────────────────────────────────────

func TestBuildActivePrompt(t *testing.T) {
	t.Run("nil returns empty", func(t *testing.T) {
		assert.Empty(t, buildActivePrompt(nil, "sender"))
	})

	t.Run("warm_up field absent", func(t *testing.T) {
		msgs := []*message.Message{{ID: "m1", RequestType: reqTypeExecute, Prompt: "do task"}}
		assert.NotContains(t, buildActivePrompt(msgs, "sender-x"), "warm_up:", "active prompt must not contain warm_up field")
	})

	t.Run("instruction is last field after messages", func(t *testing.T) {
		msgs := []*message.Message{{ID: "m1", RequestType: reqTypeExecute, Prompt: "do task"}}
		out := buildActivePrompt(msgs, "sender-x")
		msgIdx := strings.Index(out, "messages:")
		instrIdx := strings.Index(out, "instruction:")
		require.Greater(t, instrIdx, -1, "active prompt must contain instruction")
		assert.Greater(t, instrIdx, msgIdx, "instruction must come after messages section")
	})

	t.Run("sender name embedded in instruction", func(t *testing.T) {
		msgs := []*message.Message{{ID: "m1", RequestType: reqTypeExecute, Prompt: "do task"}}
		assert.Contains(t, buildActivePrompt(msgs, "my-sender"), "my-sender", "active prompt instruction must embed sender name")
	})

	t.Run("empty sender defaults to 'sender'", func(t *testing.T) {
		msgs := []*message.Message{{ID: "m1", RequestType: reqTypeExecute, Prompt: "do task"}}
		assert.Contains(t, buildActivePrompt(msgs, ""), "sender", "empty sender name should default to 'sender'")
	})

	t.Run("refs omitted to reduce tokens", func(t *testing.T) {
		msgs := []*message.Message{{ID: "m1", RequestType: reqTypeExecute, Prompt: "task", Refs: `{"author":"test"}`}}
		assert.NotContains(t, buildActivePrompt(msgs, "s"), "refs:", "active prompt must omit refs to reduce token usage")
	})

	t.Run("homogeneous: no per-item request_type", func(t *testing.T) {
		msgs := []*message.Message{
			{ID: "q1", RequestType: reqTypeQuery, Prompt: "q1"},
			{ID: "q2", RequestType: reqTypeQuery, Prompt: "q2"},
		}
		assert.NotContains(t, buildActivePrompt(msgs, "s"), "request_type:", "homogeneous active batch must not set per-item request_type")
	})

	t.Run("mixed batch: per-item request_type set", func(t *testing.T) {
		msgs := []*message.Message{
			{ID: "exec-a", RequestType: reqTypeExecute, Prompt: "task"},
			{ID: "query-a", RequestType: reqTypeQuery, Prompt: "question"},
		}
		assert.Contains(t, buildActivePrompt(msgs, "some-sender"), "request_type:", "mixed active batch must set per-item request_type")
	})

	t.Run("mixed batch: instruction is last", func(t *testing.T) {
		msgs := []*message.Message{
			{ID: "exec-b", RequestType: reqTypeExecute, Prompt: "task"},
			{ID: "query-b", RequestType: reqTypeQuery, Prompt: "question"},
		}
		out := buildActivePrompt(msgs, "s")
		msgIdx := strings.Index(out, "messages:")
		instrIdx := strings.Index(out, "instruction:")
		assert.Greater(t, instrIdx, msgIdx, "instruction must come after messages in mixed active batch")
	})
}
