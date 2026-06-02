package impl

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	operator "github.com/Shaik-Sirajuddin/memory/operator"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureReporter records every StatusReporter callback for later assertion.
// All methods are goroutine-safe. Since ResumeAgent is synchronous, assertions
// may read fields directly after the call returns (no additional sync needed).
type captureReporter struct {
	mu         sync.Mutex
	phases     []operator.InitPhase
	readyFired bool
	readyName  string
	readyModel string
	readySID   string
	errVal     error
}

func (c *captureReporter) PhaseUpdate(phase operator.InitPhase, _ string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.phases = append(c.phases, phase)
}

func (c *captureReporter) Ready(agentName, model, sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readyFired = true
	c.readyName = agentName
	c.readyModel = model
	c.readySID = sessionID
}

func (c *captureReporter) Error(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errVal = err
}

// snap returns a consistent copy of all fields without holding the lock to the
// caller's goroutine — used to avoid lock-order issues when asserting.
func (c *captureReporter) snap() (phases []operator.InitPhase, readyFired bool, readyName, readySID string, errVal error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	phases = append([]operator.InitPhase(nil), c.phases...)
	return phases, c.readyFired, c.readyName, c.readySID, c.errVal
}

// TestResumeAgent_StatusReporter tests all four StatusReporter e2e scenarios
// through the real DefaultOperator.ResumeAgent path with stub dependencies.
func TestResumeAgent_StatusReporter(t *testing.T) {
	// ── Test 1: Full happy path — phases arrive in order, Ready fires ─────────

	t.Run("HappyPath_PhasesAndReadyInOrder", func(t *testing.T) {
		t.Parallel()

		resumed := &stubCodeAgent{}
		op := newTestOperator(t, true)
		op.newCodeAgent = func(_ codeagent.Provider, _, _ string) (codeagent.CodeAgent, error) {
			return resumed, nil
		}

		workspace := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace: workspace, Name: "tui-happy",
		}))

		rep := &captureReporter{}
		err := op.ResumeAgent(operator.ResumeAgentParams{
			Workspace: workspace,
			Name:      "tui-happy",
			Status:    rep,
		})
		require.NoError(t, err, "ResumeAgent must succeed on happy path")

		phases, readyFired, readyName, readySID, errVal := rep.snap()

		require.Equal(t,
			[]operator.InitPhase{
				operator.InitPhaseResolving,
				operator.InitPhaseStarting,
				operator.InitPhaseWaiting,
			},
			phases,
			"phases must be emitted in Resolving → Starting → Waiting order",
		)
		assert.True(t, readyFired, "Ready must be called on success")
		assert.Equal(t, "tui-happy", readyName, "Ready must receive the agent name")
		assert.NotEmpty(t, readySID, "Ready must supply a non-empty sessionID")
		assert.Nil(t, errVal, "Error must not be called on success")
		require.Len(t, resumed.resumeCalls, 1, "underlying Resume must be invoked exactly once")
	})

	// ── Test 2: Binary not found — Error is called, Ready is not ─────────────

	t.Run("FailurePath_BinaryNotFound_ErrorCalled", func(t *testing.T) {
		t.Parallel()

		op := newTestOperator(t, true)
		op.newCodeAgent = func(_ codeagent.Provider, _, _ string) (codeagent.CodeAgent, error) {
			return nil, errors.New("exec: no such file or directory: claude")
		}

		workspace := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace: workspace, Name: "tui-fail",
		}))

		rep := &captureReporter{}
		err := op.ResumeAgent(operator.ResumeAgentParams{
			Workspace: workspace,
			Name:      "tui-fail",
			Status:    rep,
		})
		require.Error(t, err, "ResumeAgent must propagate the error to the caller")

		phases, readyFired, _, _, errVal := rep.snap()

		// Resolving is emitted just before createCodeAgent is attempted.
		require.NotEmpty(t, phases, "InitPhaseResolving must be emitted before the error")
		assert.Equal(t, operator.InitPhaseResolving, phases[0],
			"first phase must be Resolving even on failure")
		assert.NotNil(t, errVal, "Error() must be called with a non-nil error")
		assert.False(t, readyFired, "Ready must not be called when initialisation fails")
	})

	// ── Test 3: Nil StatusReporter — no panic (regression guard) ─────────────

	t.Run("NilStatusReporter_NoPanic", func(t *testing.T) {
		t.Parallel()

		resumed := &stubCodeAgent{}
		op := newTestOperator(t, true)
		op.newCodeAgent = func(_ codeagent.Provider, _, _ string) (codeagent.CodeAgent, error) {
			return resumed, nil
		}

		workspace := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace: workspace, Name: "tui-nil-status",
		}))

		assert.NotPanics(t, func() {
			err := op.ResumeAgent(operator.ResumeAgentParams{
				Workspace: workspace,
				Name:      "tui-nil-status",
				Status:    nil, // existing callers pass nil — must never panic
			})
			assert.NoError(t, err, "ResumeAgent with nil Status must still succeed")
		}, "nil StatusReporter must not cause a panic in any code path")
	})

	// ── Test 4: Detached mode — returns without blocking ─────────────────────

	t.Run("DetachedMode_NilStatus_NoBlock", func(t *testing.T) {
		t.Parallel()

		resumed := &stubCodeAgent{}
		op := newTestOperator(t, true)
		op.newCodeAgent = func(_ codeagent.Provider, _, _ string) (codeagent.CodeAgent, error) {
			return resumed, nil
		}

		workspace := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace: workspace, Name: "tui-detached",
		}))

		done := make(chan error, 1)
		go func() {
			done <- op.ResumeAgent(operator.ResumeAgentParams{
				Workspace: workspace,
				Name:      "tui-detached",
				Detached:  true,
				Status:    nil, // detached paths never provide a TUI reporter
			})
		}()

		select {
		case err := <-done:
			assert.NoError(t, err, "detached ResumeAgent must succeed")
		case <-time.After(5 * time.Second):
			t.Fatal("detached ResumeAgent with nil Status blocked for > 5 s — callback or lifecycle deadlock")
		}
	})
}
