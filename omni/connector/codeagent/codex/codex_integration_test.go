//go:build integration

package codex

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/omni/connector/codeagent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireCodex skips the test if the real codex binary is not installed or
// the user is not authenticated. Returns the resolved binary path.
func requireCodex(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex binary not found in PATH — skipping integration test")
	}
	if out, authErr := exec.Command(path, "login", "status").CombinedOutput(); authErr != nil {
		t.Skipf("codex not authenticated (%s) — skipping integration test", strings.TrimSpace(string(out)))
	}
	return path
}

// newRealAgent builds a codexAgent backed by the installed codex binary.
// workDir is a git-initialised temp directory so codex treats it as trusted.
func newRealAgent(t *testing.T, model string) *codexAgent {
	t.Helper()
	binPath := requireCodex(t)
	workDir := t.TempDir()
	// Codex requires a git repo (trusted directory) to run exec commands.
	cmd := exec.Command("git", "init", workDir)
	require.NoError(t, cmd.Run(), "newRealAgent should be able to git init the workDir")
	return &codexAgent{binPath: binPath, workDir: workDir, model: model}
}

// ============================================================
// TestCreate
// ============================================================

func TestCreate(t *testing.T) {
	t.Run("BootstrapsRealSessionID", func(t *testing.T) {
		t.Log("Verifying Create obtains a real codex session ID via bootstrap")
		agent := newRealAgent(t, DefaultModel)

		result, err := agent.Create(codeagent.CreateSessionParams{
			ID:    "caller-uuid",
			Name:  "test-agent",
			Model: DefaultModel,
		})

		require.NoError(t, err, "Create should succeed with a reachable authenticated codex binary")
		require.NotNil(t, result, "Create should return a non-nil result")
		assert.NotEmpty(t, result.ID, "Create should return a non-empty session ID from codex bootstrap")
		assert.NotEqual(t, "caller-uuid", result.ID,
			"Create should return the real codex session ID, not the caller-supplied UUID")
		assert.Equal(t, "test-agent", result.Name, "Create should preserve the agent name")

		t.Logf("bootstrapped session ID: %s", result.ID)
	})

	t.Run("SessionIDIsValidUUID", func(t *testing.T) {
		t.Log("Verifying the bootstrapped session ID looks like a codex session UUID")
		agent := newRealAgent(t, DefaultModel)

		result, err := agent.Create(codeagent.CreateSessionParams{Name: "uuid-check"})

		require.NoError(t, err, "Create should succeed")
		require.NotNil(t, result, "Create should return a result")
		// Codex session IDs are UUIDs: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
		parts := strings.Split(result.ID, "-")
		assert.Len(t, parts, 5, "bootstrapped session ID should be a UUID with 5 dash-separated parts")
		t.Logf("session ID: %s", result.ID)
	})

	t.Run("AuthFailureReturnsError", func(t *testing.T) {
		t.Log("Verifying Create returns an error when pointed at a binary that fails auth")
		requireCodex(t) // still need codex installed for --version to pass

		// Point at a temp dir with a stub that passes --version but fails auth.
		binDir := t.TempDir()
		script := strings.Join([]string{
			"#!/usr/bin/env bash",
			`if [[ "${1:-}" == "--version" ]]; then printf 'codex 0.128.0-stub\n'; exit 0; fi`,
			`exit 1`,
		}, "\n")
		stubPath := binDir + "/codex-auth-fail"
		require.NoError(t, writeExecutable(t, stubPath, script))

		agent := &codexAgent{binPath: stubPath, workDir: t.TempDir(), model: DefaultModel}
		_, err := agent.Create(codeagent.CreateSessionParams{ID: "should-fail"})

		require.Error(t, err, "Create should fail when auth returns non-zero")
		assert.Contains(t, err.Error(), "not authenticated",
			"Create error should mention authentication failure")
	})
}

// ============================================================
// TestResume
// ============================================================

func TestResume(t *testing.T) {
	t.Run("ResumesBootstrappedSession", func(t *testing.T) {
		t.Log("Verifying Resume can attach to the session ID returned by Create")
		agent := newRealAgent(t, DefaultModel)

		createResult, err := agent.Create(codeagent.CreateSessionParams{
			ID:    "pre-resume-id",
			Name:  "resume-test",
			Model: DefaultModel,
		})
		require.NoError(t, err, "Create should succeed before Resume")
		require.NotEmpty(t, createResult.ID, "Create should return a session ID")
		t.Logf("session to resume: %s", createResult.ID)

		stdout := &bytes.Buffer{}
		prevIn, prevOut, prevErr := interactiveStdin, interactiveStdout, interactiveStderr
		interactiveStdin = strings.NewReader("/exit\nexit\nquit\n")
		interactiveStdout = stdout
		interactiveStderr = stdout
		t.Cleanup(func() {
			interactiveStdin = prevIn
			interactiveStdout = prevOut
			interactiveStderr = prevErr
		})

		result, err := agent.Resume(codeagent.ResumeSessionParams{ID: createResult.ID})

		require.NoError(t, err, "Resume should succeed with a valid session ID from Create")
		require.NotNil(t, result, "Resume should return a non-nil result")
		assert.Equal(t, createResult.ID, result.SessionID,
			"Resume should return the same session ID that was passed in")
		assert.NotEmpty(t, result.ProcessID, "Resume should return a non-empty process ID")
	})

	// t.Run("FallsBackToNewSessionWhenIDNotFound" is skipped while the Resume
	// new-session fallback is commented out in commands.go)
}

// ============================================================
// TestSessionLifecycle
// ============================================================

func TestSessionLifecycle(t *testing.T) {
	t.Run("CreateThenResume", func(t *testing.T) {
		t.Log("Verifying full Create → Resume lifecycle against the real codex binary")
		agent := newRealAgent(t, DefaultModel)

		var ca codeagent.CodeAgent = agent

		createResult, err := ca.Create(codeagent.CreateSessionParams{
			ID:    "lifecycle-caller-id",
			Name:  "lifecycle-test",
			Model: DefaultModel,
		})
		require.NoError(t, err, "Create should succeed")
		require.NotNil(t, createResult, "Create should return a result")
		assert.NotEmpty(t, createResult.ID, "Create should return a bootstrapped session ID")
		t.Logf("lifecycle session ID: %s", createResult.ID)

		stdout := &bytes.Buffer{}
		prevIn, prevOut, prevErr := interactiveStdin, interactiveStdout, interactiveStderr
		interactiveStdin = strings.NewReader("/exit\nexit\nquit\n")
		interactiveStdout = stdout
		interactiveStderr = stdout
		t.Cleanup(func() {
			interactiveStdin = prevIn
			interactiveStdout = prevOut
			interactiveStderr = prevErr
		})

		resumeResult, err := ca.Resume(codeagent.ResumeSessionParams{ID: createResult.ID})
		require.NoError(t, err, "Resume should succeed with the session ID returned by Create")
		require.NotNil(t, resumeResult, "Resume should return a result")
		assert.Equal(t, createResult.ID, resumeResult.SessionID,
			"Resume session ID should match the ID returned by Create")
		assert.NotEmpty(t, resumeResult.ProcessID, "Resume should return a non-empty process ID")
	})
}

// ============================================================
// Helpers
// ============================================================

func writeExecutable(t *testing.T, path, script string) error {
	t.Helper()
	return os.WriteFile(path, []byte(script), 0o755)
}
