//go:build unit

package engine

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── TestEngineState_TaskMux ──────────────────────────────────────────────────

func TestEngineState_TaskMux(t *testing.T) {
	t.Run("GetTaskMux returns nil for unknown agent", func(t *testing.T) {
		s := newEngineState()
		assert.Nil(t, s.GetTaskMux("unknown"))
	})

	t.Run("SetTaskMux and GetTaskMux round-trip", func(t *testing.T) {
		s := newEngineState()
		key := &TaskKey{TaskID: "t1", CreatorAgentID: "creator-1"}
		s.SetTaskMux("ag1", key)
		got := s.GetTaskMux("ag1")
		require.NotNil(t, got)
		assert.Equal(t, "t1", got.TaskID)
		assert.Equal(t, "creator-1", got.CreatorAgentID)
	})

	t.Run("SetTaskMux nil clears the entry", func(t *testing.T) {
		s := newEngineState()
		s.SetTaskMux("ag2", &TaskKey{TaskID: "t2"})
		s.SetTaskMux("ag2", nil)
		assert.Nil(t, s.GetTaskMux("ag2"))
	})

	t.Run("different agents have independent TaskMux", func(t *testing.T) {
		s := newEngineState()
		s.SetTaskMux("ag-a", &TaskKey{TaskID: "ta"})
		s.SetTaskMux("ag-b", &TaskKey{TaskID: "tb"})
		assert.Equal(t, "ta", s.GetTaskMux("ag-a").TaskID)
		assert.Equal(t, "tb", s.GetTaskMux("ag-b").TaskID)
	})

	t.Run("concurrent set/get does not corrupt state", func(t *testing.T) {
		s := newEngineState()
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.SetTaskMux("ag-c", &TaskKey{TaskID: "tc"})
				_ = s.GetTaskMux("ag-c")
			}()
		}
		wg.Wait()
		// No assertions needed — the race detector catches any races.
	})
}

// ─── TestEngineState_PauseResumeTask ──────────────────────────────────────────

func TestEngineState_PauseResumeTask(t *testing.T) {
	t.Run("IsTaskPaused returns false for unknown task", func(t *testing.T) {
		s := newEngineState()
		assert.False(t, s.IsTaskPaused("ag", "unknown-task"))
	})

	t.Run("PauseTask makes IsTaskPaused return true", func(t *testing.T) {
		s := newEngineState()
		s.PauseTask("ag1", "task-1")
		assert.True(t, s.IsTaskPaused("ag1", "task-1"))
	})

	t.Run("ResumeTask makes IsTaskPaused return false", func(t *testing.T) {
		s := newEngineState()
		s.PauseTask("ag1", "task-1")
		s.ResumeTask("ag1", "task-1")
		assert.False(t, s.IsTaskPaused("ag1", "task-1"))
	})

	t.Run("paused task does not affect another task on same agent", func(t *testing.T) {
		s := newEngineState()
		s.PauseTask("ag1", "task-paused")
		assert.False(t, s.IsTaskPaused("ag1", "task-other"),
			"unpaused task on same agent must not appear paused")
	})

	t.Run("paused task on one agent does not affect another agent", func(t *testing.T) {
		s := newEngineState()
		s.PauseTask("ag-a", "task-shared")
		assert.False(t, s.IsTaskPaused("ag-b", "task-shared"),
			"paused task must be scoped to the specific agent")
	})

	t.Run("ResumeTask on non-paused task is a no-op", func(t *testing.T) {
		s := newEngineState()
		s.ResumeTask("ag1", "task-never-paused")
		assert.False(t, s.IsTaskPaused("ag1", "task-never-paused"))
	})

	t.Run("concurrent pause/resume/ispaused does not corrupt state", func(t *testing.T) {
		s := newEngineState()
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(3)
			go func() { defer wg.Done(); s.PauseTask("ag-cc", "task-cc") }()
			go func() { defer wg.Done(); s.ResumeTask("ag-cc", "task-cc") }()
			go func() { defer wg.Done(); _ = s.IsTaskPaused("ag-cc", "task-cc") }()
		}
		wg.Wait()
	})
}
