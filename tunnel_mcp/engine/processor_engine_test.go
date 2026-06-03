//go:build integration

package engine_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/engine"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── promptCapturingCLI ───────────────────────────────────────────────────────

// promptCapturingCLI records the prompt passed to each ExecInSession call and
// blocks until the test closes the per-call release channel, just like blockingOmniCLI.
type promptCapturingCLI struct {
	mu      sync.Mutex
	execs   []string
	prompts []string
	execCh  chan execEntry
}

func newPromptCapturingCLI() *promptCapturingCLI {
	return &promptCapturingCLI{execCh: make(chan execEntry, 10)}
}

func (c *promptCapturingCLI) ExecInSession(_ context.Context, agentID, _, _, prompt string) error {
	rel := make(chan struct{})
	c.mu.Lock()
	c.execs = append(c.execs, agentID)
	c.prompts = append(c.prompts, prompt)
	c.mu.Unlock()
	c.execCh <- execEntry{agentID: agentID, relCh: rel}
	<-rel
	return nil
}

func (c *promptCapturingCLI) GetPromptState(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (c *promptCapturingCLI) waitForExec(t *testing.T) execEntry {
	t.Helper()
	select {
	case e := <-c.execCh:
		return e
	case <-time.After(2 * time.Second):
		require.FailNow(t, "ExecInSession not called within timeout")
		return execEntry{}
	}
}

func (c *promptCapturingCLI) capturedPrompts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.prompts))
	copy(out, c.prompts)
	return out
}

// ─── fakePromptSessionStore ───────────────────────────────────────────────────

type fakePromptSessionStore struct {
	mu        sync.Mutex
	delivered map[string]bool
}

func newFakePromptSessionStore() *fakePromptSessionStore {
	return &fakePromptSessionStore{delivered: make(map[string]bool)}
}

func (s *fakePromptSessionStore) IsDelivered(_ context.Context, sessionID, promptID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivered[sessionID+":"+promptID]
}

func (s *fakePromptSessionStore) MarkDelivered(_ context.Context, sessionID, promptID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delivered[sessionID+":"+promptID] = true
	return nil
}

func (s *fakePromptSessionStore) Cleanup(_ context.Context, _ int64) error { return nil }

// ─── TestWarmupSentinel ───────────────────────────────────────────────────────

func TestWarmupSentinel(t *testing.T) {
	// First delivery to an agent with an active session uses warm_up: true.
	// Second delivery to the same session uses the lean active prompt (no warm_up field).
	t.Run("First Delivery Warm Up Second Delivery Active", func(t *testing.T) {
		ctx := context.Background()
		msgStore := message.WithTestDB(t)
		cli := newPromptCapturingCLI()
		sessionStore := newFakePromptSessionStore()

		proc := engine.New(msgStore,
			engine.WithTestBinary(cli),
			engine.WithPromptSessionStore(sessionStore),
		)
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-ws", "ag-ws", "/ws", "team")
		engine.SetSessionForTest(proc, "ag-ws", "sess-ws")

		msg1 := testEngineMessage("msg-ws-1", "ag-ws", "head", 100, false)
		insertEngineMessages(t, ctx, msgStore, msg1)

		proc.MessageArrived(ctx, "head", "ag-ws")
		e1 := cli.waitForExec(t)

		prompts1 := cli.capturedPrompts()
		require.Len(t, prompts1, 1)
		assert.Contains(t, prompts1[0], "warm_up: true", "first delivery to a session must be a warm-up prompt")
		close(e1.relCh)

		time.Sleep(10 * time.Millisecond)

		msg2 := testEngineMessage("msg-ws-2", "ag-ws", "head", 200, false)
		insertEngineMessages(t, ctx, msgStore, msg2)

		proc.AgentCallback(ctx, engine.AgentCallbackRequest{
			Source:  engine.MessageRef{MessageID: msg1.ID},
			AgentID: "ag-ws",
		}, false)

		e2 := cli.waitForExec(t)
		prompts2 := cli.capturedPrompts()
		require.Len(t, prompts2, 2)
		assert.NotContains(t, prompts2[1], "warm_up:", "second delivery to same session must use active prompt, not warm-up")
		close(e2.relCh)
	})

	// Without a session ID the warmup sentinel cannot be checked, so every delivery is a warm-up.
	t.Run("No Session ID Always Uses Warmup", func(t *testing.T) {
		ctx := context.Background()
		msgStore := message.WithTestDB(t)
		cli := newPromptCapturingCLI()

		proc := engine.New(msgStore,
			engine.WithTestBinary(cli),
			engine.WithPromptSessionStore(newFakePromptSessionStore()),
		)
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-nosess", "ag-nosess", "/ws", "team")
		// No SetSessionForTest — sessionID remains "".

		msg := testEngineMessage("msg-nosess-1", "ag-nosess", "head", 100, false)
		insertEngineMessages(t, ctx, msgStore, msg)

		proc.MessageArrived(ctx, "head", "ag-nosess")
		e1 := cli.waitForExec(t)

		prompts := cli.capturedPrompts()
		require.Len(t, prompts, 1)
		assert.Contains(t, prompts[0], "warm_up: true", "no session ID must always produce a warm-up prompt")
		close(e1.relCh)
	})

	// A sentinel marked for session A must not suppress the warm-up for session B on the same agent.
	t.Run("Different Session ID Does Not Inherit Sentinel", func(t *testing.T) {
		ctx := context.Background()
		msgStore := message.WithTestDB(t)
		cli := newPromptCapturingCLI()
		sessionStore := newFakePromptSessionStore()

		proc := engine.New(msgStore,
			engine.WithTestBinary(cli),
			engine.WithPromptSessionStore(sessionStore),
		)
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-newses", "ag-newses", "/ws", "team")

		// Pre-seed sentinel for an old session; agent now has a different session.
		_ = sessionStore.MarkDelivered(ctx, "old-session", "warmup_done")
		engine.SetSessionForTest(proc, "ag-newses", "new-session")

		msg := testEngineMessage("msg-newses-1", "ag-newses", "head", 100, false)
		insertEngineMessages(t, ctx, msgStore, msg)

		proc.MessageArrived(ctx, "head", "ag-newses")
		e1 := cli.waitForExec(t)

		prompts := cli.capturedPrompts()
		require.Len(t, prompts, 1)
		assert.Contains(t, prompts[0], "warm_up: true", "new session must receive warm-up regardless of other sessions")
		close(e1.relCh)
	})
}

// ─── TestPickNextMessages_MixedBatching ───────────────────────────────────────

func TestPickNextMessages_MixedBatching(t *testing.T) {
	ctx := context.Background()

	// Execute with a co-group query: both share the same group_id and should be bundled.
	t.Run("Execute And CoGroup Query Are Bundled", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(newBlockingOmniCLI()))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-mix", "ag-mix", "/ws", "team")

		exec := newGroupMessage("exec-1", "ag-mix", "sender", message.RequestTypeExecute, "group-abc", 100)
		coQuery := newGroupMessage("coquery-1", "ag-mix", "sender", message.RequestType("query"), "group-abc", 101)
		otherQuery := newGroupMessage("other-q", "ag-mix", "sender", message.RequestType("query"), "group-xyz", 102)
		insertEngineMessages(t, ctx, msgStore, exec, coQuery, otherQuery)

		picked, err := engine.PickNextMessagesForTest(proc, "ag-mix")
		require.NoError(t, err)
		require.Len(t, picked, 2, "execute and co-group query should be bundled into one delivery")
		assert.Equal(t, "exec-1", picked[0].ID, "execute must be first in the batch")
		assert.Equal(t, "coquery-1", picked[1].ID, "co-group query must follow the execute")
	})

	// Execute without a group_id should pick only itself.
	t.Run("Execute Without GroupID Picks Only Itself", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(newBlockingOmniCLI()))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-nogrp", "ag-nogrp", "/ws", "team")

		exec := newGroupMessage("exec-ng", "ag-nogrp", "sender", message.RequestTypeExecute, "", 100)
		query := newGroupMessage("q-ng", "ag-nogrp", "sender", message.RequestType("query"), "", 101)
		insertEngineMessages(t, ctx, msgStore, exec, query)

		picked, err := engine.PickNextMessagesForTest(proc, "ag-nogrp")
		require.NoError(t, err)
		require.Len(t, picked, 1, "execute without group_id must not bundle the following query")
		assert.Equal(t, "exec-ng", picked[0].ID)
	})

	// A query from a different group_id must not be bundled with the execute.
	t.Run("Different Group Query Is Not Bundled", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(newBlockingOmniCLI()))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-diffgrp", "ag-diffgrp", "/ws", "team")

		exec := newGroupMessage("exec-dg", "ag-diffgrp", "sender", message.RequestTypeExecute, "group-1", 100)
		wrongGrpQ := newGroupMessage("q-dg", "ag-diffgrp", "sender", message.RequestType("query"), "group-2", 101)
		insertEngineMessages(t, ctx, msgStore, exec, wrongGrpQ)

		picked, err := engine.PickNextMessagesForTest(proc, "ag-diffgrp")
		require.NoError(t, err)
		require.Len(t, picked, 1, "query from a different group_id must not be bundled with the execute")
		assert.Equal(t, "exec-dg", picked[0].ID)
	})

	// Another execute in the same group must not be bundled — only query/instant are co-grouped.
	t.Run("Co-Group Execute Is Not Bundled", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(newBlockingOmniCLI()))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-2exec", "ag-2exec", "/ws", "team")

		exec1 := newGroupMessage("exec-a", "ag-2exec", "sender", message.RequestTypeExecute, "group-e", 100)
		exec2 := newGroupMessage("exec-b", "ag-2exec", "sender", message.RequestTypeExecute, "group-e", 101)
		insertEngineMessages(t, ctx, msgStore, exec1, exec2)

		picked, err := engine.PickNextMessagesForTest(proc, "ag-2exec")
		require.NoError(t, err)
		require.Len(t, picked, 1, "a second execute in the same group must not be bundled")
		assert.Equal(t, "exec-a", picked[0].ID)
	})

	// Query batching still batches up to 5 from the same sender, stopping before an execute.
	t.Run("Query Batch From Same Sender Stops Before Execute", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(newBlockingOmniCLI()))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-qbatch", "ag-qbatch", "/ws", "team")

		for i, id := range []string{"q-1", "q-2", "q-3"} {
			msg := newGroupMessage(id, "ag-qbatch", "sender-a", message.RequestType("query"), "", int64(i*100))
			insertEngineMessages(t, ctx, msgStore, msg)
		}
		execMsg := newGroupMessage("exec-q", "ag-qbatch", "sender-a", message.RequestTypeExecute, "", 300)
		insertEngineMessages(t, ctx, msgStore, execMsg)

		picked, err := engine.PickNextMessagesForTest(proc, "ag-qbatch")
		require.NoError(t, err)
		assert.Len(t, picked, 3, "three queries from same sender should be batched; execute must be excluded")
		for _, p := range picked {
			assert.Equal(t, message.RequestType("query"), p.RequestType, "batch should contain only queries")
		}
	})
}

// ─── TestOnStopRecall ─────────────────────────────────────────────────────────

func TestOnStopRecall(t *testing.T) {
	// When mandatory tool was not invoked and retries remain, OnStop must return a recall
	// string and leave messages in StatusProcessing (agent continues the same session).
	t.Run("Messages Stay Processing On Recall", func(t *testing.T) {
		ctx := context.Background()
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(newBlockingOmniCLI()))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-recall", "ag-recall", "/ws", "team")
		engine.SetSessionForTest(proc, "ag-recall", "sess-recall")

		msg := processingMessage("msg-recall-1", "ag-recall", "sender", message.RequestTypeExecute, 1)
		require.NoError(t, msgStore.InsertMessage(ctx, msg))

		recall := proc.OnStop(ctx, "ag-recall", "sess-recall")

		require.NotNil(t, recall, "OnStop must return a recall string when mandatory tool was not invoked")
		assert.Contains(t, *recall, "msg-recall-1", "recall must embed the message_id")
		assert.Contains(t, *recall, "send_response", "recall must reference the send_response tool")

		got, err := msgStore.GetMessage(ctx, "msg-recall-1")
		require.NoError(t, err)
		assert.Equal(t, message.StatusProcessing, got.Status,
			"message must remain StatusProcessing during recall — agent continues the same session")
	})

	// A batch recall must include all message IDs and mention send_response_batch.
	t.Run("Batch Recall Includes All IDs And send_response_batch", func(t *testing.T) {
		ctx := context.Background()
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(newBlockingOmniCLI()))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-brecall", "ag-brecall", "/ws", "team")
		engine.SetSessionForTest(proc, "ag-brecall", "sess-brecall")

		msg1 := processingMessage("batch-r-1", "ag-brecall", "sender", message.RequestType("query"), 1)
		msg2 := processingMessage("batch-r-2", "ag-brecall", "sender", message.RequestType("query"), 1)
		require.NoError(t, msgStore.InsertMessage(ctx, msg1))
		require.NoError(t, msgStore.InsertMessage(ctx, msg2))

		recall := proc.OnStop(ctx, "ag-brecall", "sess-brecall")

		require.NotNil(t, recall)
		assert.Contains(t, *recall, "batch-r-1", "batch recall must include first message_id")
		assert.Contains(t, *recall, "batch-r-2", "batch recall must include second message_id")
		assert.Contains(t, *recall, "send_response_batch", "batch recall must offer send_response_batch")
	})

	// After max retries are exhausted, OnStop must deliver the message and return nil.
	t.Run("Messages Delivered After Max Retries Exceeded", func(t *testing.T) {
		ctx := context.Background()
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(newBlockingOmniCLI()))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-maxr", "ag-maxr", "/ws", "team")
		engine.SetSessionForTest(proc, "ag-maxr", "sess-maxr")

		// retries=4 exceeds maxMandatoryToolRetries (3).
		msg := processingMessage("msg-maxr-1", "ag-maxr", "sender", message.RequestTypeExecute, 4)
		require.NoError(t, msgStore.InsertMessage(ctx, msg))

		recall := proc.OnStop(ctx, "ag-maxr", "sess-maxr")

		require.Nil(t, recall, "OnStop must not return a recall string after max retries are exhausted")

		got, err := msgStore.GetMessage(ctx, "msg-maxr-1")
		require.NoError(t, err)
		assert.Equal(t, message.StatusDelivered, got.Status, "message must be delivered once max retries are exceeded")
		assert.NotNil(t, got.DeliveryTime, "delivered message must have a DeliveryTime set")
	})

	// No processing messages: OnStop returns nil without error.
	t.Run("No Processing Messages Returns Nil", func(t *testing.T) {
		ctx := context.Background()
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(newBlockingOmniCLI()))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-empty", "ag-empty", "/ws", "team")
		engine.SetSessionForTest(proc, "ag-empty", "sess-empty")

		recall := proc.OnStop(ctx, "ag-empty", "sess-empty")

		require.Nil(t, recall, "OnStop with no processing messages must return nil")
	})
}

// ─── TestUserPromptSubmit_StaleOrphanSweep ────────────────────────────────────

func TestUserPromptSubmit_StaleOrphanSweep(t *testing.T) {
	// A new UserPromptSubmit (PrePrompt hook) must mark stale processing messages as failed.
	t.Run("Orphan Processing Messages Swept On New Prompt", func(t *testing.T) {
		ctx := context.Background()
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(newBlockingOmniCLI()))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-sweep", "ag-sweep", "/ws", "team")

		// Orphan: processing message left behind by a crashed prior session.
		orphan := processingMessage("orphan-1", "ag-sweep", "sender", message.RequestTypeExecute, 1)
		require.NoError(t, msgStore.InsertMessage(ctx, orphan))

		// New session submits a non-engine prompt.
		proc.OnUserPromptSubmit(ctx, "ag-sweep", "sess-new", "hello world")

		got, err := msgStore.GetMessage(ctx, "orphan-1")
		require.NoError(t, err)
		assert.Equal(t, message.StatusFailed, got.Status,
			"orphan processing message must be swept to StatusFailed when a new prompt submission fires")
	})

	// The Stop hook (recall path) fires OnStop, not OnUserPromptSubmit.
	// It must NOT sweep current processing messages.
	t.Run("Stop Hook Recall Does Not Sweep Processing Messages", func(t *testing.T) {
		ctx := context.Background()
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(newBlockingOmniCLI()))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-nostop", "ag-nostop", "/ws", "team")
		engine.SetSessionForTest(proc, "ag-nostop", "sess-nostop")

		// Active processing message from the current delivery.
		inProgress := processingMessage("inprog-1", "ag-nostop", "sender", message.RequestTypeExecute, 1)
		require.NoError(t, msgStore.InsertMessage(ctx, inProgress))

		// Stop hook fires (recall path) — must NOT sweep inprog-1.
		recall := proc.OnStop(ctx, "ag-nostop", "sess-nostop")

		require.NotNil(t, recall, "stop without mandatory tool must recall")
		got, err := msgStore.GetMessage(ctx, "inprog-1")
		require.NoError(t, err)
		assert.Equal(t, message.StatusProcessing, got.Status,
			"Stop hook (recall path) must not sweep current processing messages to StatusFailed")
	})

	// UserPromptSubmit with an engine YAML payload should advance listed messages to processing.
	t.Run("UserPromptSubmit With Engine YAML Advances Messages To Processing", func(t *testing.T) {
		ctx := context.Background()
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(newBlockingOmniCLI()))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-yaml", "ag-yaml", "/ws", "team")

		msg := &message.Message{
			ID:          "msg-yaml-1",
			To:          "ag-yaml",
			From:        "sender",
			FromSpec:    message.SpecOmni,
			ToSpec:      message.SpecOmni,
			RequestType: message.RequestTypeExecute,
			Status:      message.StatusInQueue,
			SentTime:    100,
			Prompt:      "do task",
			Refs:        "{}",
		}
		require.NoError(t, msgStore.InsertMessage(ctx, msg))

		// Simulate what executeLoop sends as the hook prompt.
		yamlPrompt := "warm_up: true\nmessages:\n  - message_id: msg-yaml-1\n    prompt: do task\ninstruction: Execute.\n"
		proc.OnUserPromptSubmit(ctx, "ag-yaml", "sess-yaml", yamlPrompt)

		got, err := msgStore.GetMessage(ctx, "msg-yaml-1")
		require.NoError(t, err)
		assert.Equal(t, message.StatusProcessing, got.Status,
			"OnUserPromptSubmit with engine YAML must advance listed messages to StatusProcessing")
	})
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// processingMessage creates a message in StatusProcessing for hook-path tests.
func processingMessage(id, to, from string, rt message.RequestType, retries int) *message.Message {
	return &message.Message{
		ID:          id,
		To:          to,
		From:        from,
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: rt,
		ShouldReply: false,
		Prompt:      "test prompt",
		Refs:        "{}",
		Status:      message.StatusProcessing,
		Retries:     retries,
		SentTime:    100,
	}
}

// newGroupMessage creates an InQueue message with a specific group_id and request type.
func newGroupMessage(id, to, from string, rt message.RequestType, groupID string, sentTime int64) *message.Message {
	return &message.Message{
		ID:          id,
		To:          to,
		From:        from,
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: rt,
		ShouldReply: false,
		Prompt:      "test",
		Refs:        "{}",
		Status:      message.StatusInQueue,
		GroupID:     groupID,
		SentTime:    sentTime,
	}
}
