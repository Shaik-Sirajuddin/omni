//go:build integration

package engine_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/engine"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── noopCLI ─────────────────────────────────────────────────────────────────

type noopCLI struct{}

func (c *noopCLI) ExecInSession(_ context.Context, _, _, _, _ string) error { return nil }
func (c *noopCLI) GetPromptState(_ context.Context, _ string) (string, error) {
	return "", nil
}

// ─── spyTaskDeliveryStore ─────────────────────────────────────────────────────

type spyTaskDeliveryStore struct {
	mu        sync.Mutex
	Starts    []spyStart
	Completes []spyComplete
}

type spyStart struct{ TaskID, AgentID, CreatorAgentID, LastMessageID string }
type spyComplete struct{ TaskID, AgentID string }

func newSpyTaskDeliveryStore() *spyTaskDeliveryStore {
	return &spyTaskDeliveryStore{}
}

func (s *spyTaskDeliveryStore) StartDelivery(_ context.Context, taskID, agentID, creatorAgentID, lastMessageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Starts = append(s.Starts, spyStart{taskID, agentID, creatorAgentID, lastMessageID})
	return nil
}

func (s *spyTaskDeliveryStore) CompleteDelivery(_ context.Context, taskID, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Completes = append(s.Completes, spyComplete{taskID, agentID})
	return nil
}

func (s *spyTaskDeliveryStore) GetInProgress(_ context.Context, _ string) ([]session.TaskDelivery, error) {
	return nil, nil
}

func (s *spyTaskDeliveryStore) starts() []spyStart {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]spyStart, len(s.Starts))
	copy(out, s.Starts)
	return out
}

func (s *spyTaskDeliveryStore) completes() []spyComplete {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]spyComplete, len(s.Completes))
	copy(out, s.Completes)
	return out
}

// ─── TestT1_TaskMuxPriority ───────────────────────────────────────────────────

func TestT1_TaskMuxPriority(t *testing.T) {
	ctx := context.Background()

	// T1: when TaskMux is set, messages matching that task_id are returned first
	// regardless of sent_time ordering.
	t.Run("TaskMux Messages Sorted Before Non-Task Messages", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t1", "ag-t1", "/ws", "team")

		// Other execute (earlier sent_time, no task_id) would normally be picked first.
		other := taskMsg("other-exec", "ag-t1", "sender", message.RequestTypeExecute, "", "", 100)
		// T1 execute (later sent_time, task_id=T1).
		t1exec := taskMsg("t1-exec", "ag-t1", "sender", message.RequestTypeExecute, "task-t1", "creator-1", 200)
		insertEngineMessages(t, ctx, msgStore, other, t1exec)

		// Without TaskMux: other-exec is picked (earlier sent_time).
		picked, err := engine.PickNextMessagesForTest(proc, "ag-t1")
		require.NoError(t, err)
		require.Len(t, picked, 1)
		assert.Equal(t, "other-exec", picked[0].ID, "without TaskMux the earliest message wins")

		// Mark other-exec delivered so it is no longer in_queue.
		other.Status = message.StatusDelivered
		require.NoError(t, msgStore.UpdateMessage(ctx, other))

		// Set TaskMux to task-t1.
		engine.SetTaskMuxForTest(proc, "ag-t1", &engine.TaskKey{TaskID: "task-t1", CreatorAgentID: "creator-1"})

		// Insert a new other-exec2 (earlier sent_time, different task).
		other2 := taskMsg("other-exec2", "ag-t1", "sender", message.RequestTypeExecute, "", "", 50)
		insertEngineMessages(t, ctx, msgStore, other2)

		// With TaskMux: t1-exec should be first despite later sent_time.
		picked2, err := engine.PickNextMessagesForTest(proc, "ag-t1")
		require.NoError(t, err)
		require.NotEmpty(t, picked2, "TaskMux must not return empty")
		assert.Equal(t, "t1-exec", picked2[0].ID, "with TaskMux, task-t1 message must sort first")
	})
}

// ─── TestT2_QueryBypass ───────────────────────────────────────────────────────

func TestT2_QueryBypass(t *testing.T) {
	ctx := context.Background()

	// T2: bypass mode picks only in_queue queries matching the given task_id.
	t.Run("Bypass Returns Queries For Matching Task Only", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t2", "ag-t2", "/ws", "team")

		q1 := taskMsg("q-task1", "ag-t2", "sender", message.RequestType("query"), "task-1", "creator-1", 100)
		q2 := taskMsg("q-task2", "ag-t2", "sender", message.RequestType("query"), "task-2", "creator-1", 101)
		qNone := taskMsg("q-notask", "ag-t2", "sender", message.RequestType("query"), "", "", 102)
		insertEngineMessages(t, ctx, msgStore, q1, q2, qNone)

		// Bypass for task-1: only q-task1 returned.
		picked, err := engine.PickNextMessagesWithBypassForTest(proc, "ag-t2", engine.TaskKey{TaskID: "task-1", CreatorAgentID: "creator-1"})
		require.NoError(t, err)
		require.Len(t, picked, 1, "bypass must return only queries for the specified task")
		assert.Equal(t, "q-task1", picked[0].ID)

		// Bypass for task-2: only q-task2 returned.
		picked2, err := engine.PickNextMessagesWithBypassForTest(proc, "ag-t2", engine.TaskKey{TaskID: "task-2", CreatorAgentID: "creator-1"})
		require.NoError(t, err)
		require.Len(t, picked2, 1)
		assert.Equal(t, "q-task2", picked2[0].ID)
	})

	t.Run("Empty TaskKey Falls Through To Normal Mode", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t2b", "ag-t2b", "/ws", "team")

		q := taskMsg("q-1", "ag-t2b", "sender", message.RequestType("query"), "task-x", "creator", 100)
		insertEngineMessages(t, ctx, msgStore, q)

		// Empty TaskKey is zero — pickNextMessages treats it as no bypass and runs normal mode.
		// Normal mode returns whatever is pending (the query).
		picked, err := engine.PickNextMessagesWithBypassForTest(proc, "ag-t2b", engine.TaskKey{})
		require.NoError(t, err)
		assert.Len(t, picked, 1, "empty TaskKey is not bypass mode — normal pick returns the pending query")
	})

	t.Run("Bypass Returns Nothing When No Matching Task Queries", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t2c", "ag-t2c", "/ws", "team")

		// Query with different task_id.
		q := taskMsg("q-other", "ag-t2c", "sender", message.RequestType("query"), "task-other", "creator", 100)
		insertEngineMessages(t, ctx, msgStore, q)

		picked, err := engine.PickNextMessagesWithBypassForTest(proc, "ag-t2c", engine.TaskKey{TaskID: "task-target", CreatorAgentID: "creator"})
		require.NoError(t, err)
		assert.Empty(t, picked, "bypass must not return queries with a different task_id")
	})

	t.Run("Bypass Ignores Execute Messages", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t2d", "ag-t2d", "/ws", "team")

		// Only an execute with matching task — no queries.
		exec := taskMsg("exec-bypass", "ag-t2d", "sender", message.RequestTypeExecute, "task-by", "creator", 100)
		insertEngineMessages(t, ctx, msgStore, exec)

		picked, err := engine.PickNextMessagesWithBypassForTest(proc, "ag-t2d", engine.TaskKey{TaskID: "task-by", CreatorAgentID: "creator"})
		require.NoError(t, err)
		assert.Empty(t, picked, "bypass mode must not pick execute messages")
	})
}

// ─── TestT3_ExecuteGuard ─────────────────────────────────────────────────────

func TestT3_ExecuteGuard(t *testing.T) {
	ctx := context.Background()

	// T3: when first pending is execute, standalone queries are not picked.
	// With bypass set, queries of the matching task ARE returned.
	t.Run("Standalone Queries Not Picked When Execute Is First", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t3", "ag-t3", "/ws", "team")

		// Execute is first (lowest sent_time), standalone query has no task_id.
		exec := taskMsg("exec-t3", "ag-t3", "sender", message.RequestTypeExecute, "task-t3", "creator-t3", 100)
		queryDiff := taskMsg("q-diff", "ag-t3", "sender", message.RequestType("query"), "task-other", "creator-other", 101)
		insertEngineMessages(t, ctx, msgStore, exec, queryDiff)

		picked, err := engine.PickNextMessagesForTest(proc, "ag-t3")
		require.NoError(t, err)
		require.Len(t, picked, 1, "only the execute should be returned; standalone query excluded")
		assert.Equal(t, "exec-t3", picked[0].ID)
	})

	t.Run("Co-Task Query Bundled With Execute In Normal Mode", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t3b", "ag-t3b", "/ws", "team")

		exec := taskMsg("exec-t3b", "ag-t3b", "sender", message.RequestTypeExecute, "task-t3b", "creator", 100)
		coQuery := taskMsg("q-co", "ag-t3b", "sender", message.RequestType("query"), "task-t3b", "creator", 101)
		insertEngineMessages(t, ctx, msgStore, exec, coQuery)

		picked, err := engine.PickNextMessagesForTest(proc, "ag-t3b")
		require.NoError(t, err)
		require.Len(t, picked, 2, "co-task query must be bundled with execute")
		assert.Equal(t, "exec-t3b", picked[0].ID)
		assert.Equal(t, "q-co", picked[1].ID)
	})

	t.Run("Bypass Returns Co-Task Queries Independently", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t3c", "ag-t3c", "/ws", "team")

		exec := taskMsg("exec-t3c", "ag-t3c", "sender", message.RequestTypeExecute, "task-t3c", "creator", 100)
		coQuery := taskMsg("q-bypass", "ag-t3c", "sender", message.RequestType("query"), "task-t3c", "creator", 101)
		insertEngineMessages(t, ctx, msgStore, exec, coQuery)

		picked, err := engine.PickNextMessagesWithBypassForTest(proc, "ag-t3c",
			engine.TaskKey{TaskID: "task-t3c", CreatorAgentID: "creator"})
		require.NoError(t, err)
		require.Len(t, picked, 1, "bypass must return only the query, not the execute")
		assert.Equal(t, "q-bypass", picked[0].ID)
	})
}

// ─── TestT4_PauseResumeTask ───────────────────────────────────────────────────

func TestT4_PauseResumeTask(t *testing.T) {
	ctx := context.Background()

	t.Run("PauseTask Causes pickNextMessages To Return Nil", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t4", "ag-t4", "/ws", "team")

		msg := taskMsg("exec-t4", "ag-t4", "sender", message.RequestTypeExecute, "task-t4", "creator-t4", 100)
		insertEngineMessages(t, ctx, msgStore, msg)

		proc.PauseTask("ag-t4", "task-t4", "creator-t4")

		picked, err := engine.PickNextMessagesForTest(proc, "ag-t4")
		require.NoError(t, err)
		assert.Empty(t, picked, "paused task must not be picked")
	})

	t.Run("ResumeTask Allows Messages To Be Picked Again", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t4r", "ag-t4r", "/ws", "team")

		msg := taskMsg("exec-t4r", "ag-t4r", "sender", message.RequestTypeExecute, "task-t4r", "creator-t4r", 100)
		insertEngineMessages(t, ctx, msgStore, msg)

		proc.PauseTask("ag-t4r", "task-t4r", "creator-t4r")
		picked, err := engine.PickNextMessagesForTest(proc, "ag-t4r")
		require.NoError(t, err)
		assert.Empty(t, picked, "paused task must not be picked after PauseTask")

		proc.ResumeTask(ctx, "ag-t4r", "task-t4r", "creator-t4r")
		picked2, err := engine.PickNextMessagesForTest(proc, "ag-t4r")
		require.NoError(t, err)
		require.Len(t, picked2, 1, "resumed task must be picked after ResumeTask")
		assert.Equal(t, "exec-t4r", picked2[0].ID)
	})

	t.Run("Pausing One Task Does Not Block Another Task", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t4x", "ag-t4x", "/ws", "team")

		paused := taskMsg("exec-paused", "ag-t4x", "sender", message.RequestTypeExecute, "task-paused", "creator", 200)
		active := taskMsg("exec-active", "ag-t4x", "sender", message.RequestTypeExecute, "task-active", "creator", 100)
		insertEngineMessages(t, ctx, msgStore, paused, active)

		proc.PauseTask("ag-t4x", "task-paused", "creator")

		// active has lower sent_time so it comes first; task-paused is paused.
		picked, err := engine.PickNextMessagesForTest(proc, "ag-t4x")
		require.NoError(t, err)
		require.Len(t, picked, 1)
		assert.Equal(t, "exec-active", picked[0].ID, "active task must be picked even though another task is paused")
	})

	t.Run("Messages Without TaskID Are Not Affected By PauseTask", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := engine.New(msgStore, engine.WithTestBinary(&noopCLI{}))
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t4n", "ag-t4n", "/ws", "team")

		noTask := taskMsg("exec-notask", "ag-t4n", "sender", message.RequestTypeExecute, "", "", 100)
		insertEngineMessages(t, ctx, msgStore, noTask)

		proc.PauseTask("ag-t4n", "some-task", "creator")

		picked, err := engine.PickNextMessagesForTest(proc, "ag-t4n")
		require.NoError(t, err)
		require.Len(t, picked, 1, "messages without task_id must not be affected by PauseTask")
		assert.Equal(t, "exec-notask", picked[0].ID)
	})
}

// ─── TestT5_TaskDeliveryCheckpoints ──────────────────────────────────────────

func TestT5_TaskDeliveryCheckpoints(t *testing.T) {
	ctx := context.Background()

	// T5: StartDelivery called when execute with task_id is picked and queued.
	t.Run("StartDelivery Called When Execute Picked", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		cli := newBlockingOmniCLI()
		spy := newSpyTaskDeliveryStore()

		proc := engine.New(msgStore,
			engine.WithTestBinary(cli),
			engine.WithTaskDeliveryStore(spy),
		)
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t5s", "ag-t5s", "/ws", "team")

		msg := taskMsg("exec-t5s", "ag-t5s", "sender", message.RequestTypeExecute, "task-t5", "creator-t5", 100)
		insertEngineMessages(t, ctx, msgStore, msg)

		proc.MessageArrived(ctx, "sender", "ag-t5s")
		e1 := cli.waitForExec(t)

		// StartDelivery must have been called before ExecInSession blocks.
		starts := spy.starts()
		require.Len(t, starts, 1, "StartDelivery must be called when execute is picked")
		assert.Equal(t, "task-t5", starts[0].TaskID)
		assert.Equal(t, "ag-t5s", starts[0].AgentID)
		assert.Equal(t, "exec-t5s", starts[0].LastMessageID)

		close(e1.relCh)
	})

	// T5: StartDelivery NOT called for execute without task_id.
	t.Run("StartDelivery Not Called For Execute Without TaskID", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		cli := newBlockingOmniCLI()
		spy := newSpyTaskDeliveryStore()

		proc := engine.New(msgStore,
			engine.WithTestBinary(cli),
			engine.WithTaskDeliveryStore(spy),
		)
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t5n", "ag-t5n", "/ws", "team")

		msg := taskMsg("exec-t5n", "ag-t5n", "sender", message.RequestTypeExecute, "", "", 100)
		insertEngineMessages(t, ctx, msgStore, msg)

		proc.MessageArrived(ctx, "sender", "ag-t5n")
		e1 := cli.waitForExec(t)

		assert.Empty(t, spy.starts(), "StartDelivery must not be called for execute with empty task_id")

		close(e1.relCh)
	})

	// T5: CompleteDelivery called when markDelivered fires for an execute with task_id.
	t.Run("CompleteDelivery Called When Execute Delivered Via Hook", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		cli := newBlockingOmniCLI()
		spy := newSpyTaskDeliveryStore()

		proc := engine.New(msgStore,
			engine.WithTestBinary(cli),
			engine.WithTaskDeliveryStore(spy),
		)
		engine.StartForTest(proc, ctx)
		engine.RegisterAgentForTest(proc, "ag-t5c", "ag-t5c", "/ws", "team")
		engine.SetSessionForTest(proc, "ag-t5c", "sess-t5c")

		msg := taskMsg("exec-t5c", "ag-t5c", "sender", message.RequestTypeExecute, "task-t5c", "creator-t5c", 100)
		insertEngineMessages(t, ctx, msgStore, msg)

		proc.MessageArrived(ctx, "sender", "ag-t5c")
		e1 := cli.waitForExec(t)

		// StartDelivery must have been called.
		require.Len(t, spy.starts(), 1, "StartDelivery must be called before ExecInSession")

		// Simulate hook sequence: PrePrompt moves message to processing, tool invoked, Stop marks delivered.
		yamlPrompt := fmt.Sprintf("warm_up: true\nmessages:\n  - message_id: exec-t5c\n    prompt: task\ninstruction: Execute.\n")
		proc.OnUserPromptSubmit(ctx, "ag-t5c", "sess-t5c", yamlPrompt)
		proc.OnPostToolUse("ag-t5c", "sess-t5c", "send_response", nil)
		proc.OnStop(ctx, "ag-t5c", "sess-t5c")

		// CompleteDelivery must have been called inside markDelivered.
		time.Sleep(5 * time.Millisecond) // let any async work settle
		completes := spy.completes()
		require.Len(t, completes, 1, "CompleteDelivery must be called when execute is marked delivered")
		assert.Equal(t, "task-t5c", completes[0].TaskID)
		assert.Equal(t, "ag-t5c", completes[0].AgentID)

		close(e1.relCh)
	})
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// taskMsg creates an in_queue message with task_id and creator_agent_id set.
func taskMsg(id, to, from string, rt message.RequestType, taskID, creatorAgentID string, sentTime int64) *message.Message {
	return &message.Message{
		ID:             id,
		To:             to,
		From:           from,
		FromSpec:       message.SpecOmni,
		ToSpec:         message.SpecOmni,
		RequestType:    rt,
		ShouldReply:    false,
		Prompt:         "test",
		Refs:           "{}",
		Status:         message.StatusInQueue,
		TaskID:         taskID,
		CreatorAgentID: creatorAgentID,
		SentTime:       sentTime,
	}
}
