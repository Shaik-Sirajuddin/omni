package claude

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/omni/connector/codeagent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installFakeClaudeBinary writes a bash stub that mimics the claude CLI.
// It logs each invocation to logPath and handles --version, auth status,
// and -r <id> (resume) so the connector's lifecycle methods can be tested
// without a real claude binary.
func installFakeClaudeBinary(t *testing.T, dir, logPath string) {
	t.Helper()
	script := strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		`printf '%s\n' "$*" >> "` + logPath + `"`,
		`if [[ "${1:-}" == "--version" ]]; then`,
		`  printf 'claude 1.0.0-test\n'`,
		`  exit 0`,
		`fi`,
		`if [[ "${1:-}" == "auth" && "${2:-}" == "status" ]]; then`,
		`  exit 0`,
		`fi`,
		`if [[ "${1:-}" == "-p" ]]; then`,
		`  printf '{"type":"result","result":"ok"}\n'`,
		`  exit 0`,
		`fi`,
		`if [[ "${1:-}" == "-r" ]]; then`,
		`  printf 'interactive-started\n'`,
		`  read -r -t 30 _ || true`,
		`  exit 0`,
		`fi`,
		`printf 'unexpected args: %s\n' "$*" >&2`,
		`exit 1`,
	}, "\n")
	path := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
}

func readLogLines(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read log: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// Models under test — gemini and codex are skipped per task instructions.
var claudeModels = []string{
	ModelOpus4,
	ModelSonnet4,
	ModelHaiku45,
}

func TestCreateSession(t *testing.T) {
	for _, model := range claudeModels {
		model := model
		t.Run(model, func(t *testing.T) {
			workDir := t.TempDir()
			binDir := t.TempDir()
			logPath := filepath.Join(t.TempDir(), "create.log")
			installFakeClaudeBinary(t, binDir, logPath)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			agent := &claudeAgent{ClaudeParser: &ClaudeParser{}, workDir: workDir, model: model}
			result, err := agent.Create(codeagent.CreateSessionParams{
				ID:    "test-session-" + model,
				Name:  "test-" + model,
				Model: model,
			})
			require.NoError(t, err, "Create should succeed with a reachable claude binary")
			require.NotNil(t, result)
			assert.Equal(t, "test-session-"+model, result.ID)
			assert.Equal(t, "test-"+model, result.Name)

			lines := readLogLines(t, logPath)
			require.NotEmpty(t, lines, "Create should invoke the claude binary")
			assert.Contains(t, lines[0], "--version", "Create should probe binary version first")
		})
	}
}

func TestCreateSessionGeneratesIDWhenEmpty(t *testing.T) {
	workDir := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "create-genid.log")
	installFakeClaudeBinary(t, binDir, logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	agent := &claudeAgent{ClaudeParser: &ClaudeParser{}, workDir: workDir, model: ModelSonnet4}
	result, err := agent.Create(codeagent.CreateSessionParams{Name: "gen-id-test"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.NotEmpty(t, result.ID, "Create should generate a session ID when none is provided")
}

func TestCreateSessionAuthFailure(t *testing.T) {
	workDir := t.TempDir()
	binDir := t.TempDir()

	// Write a stub that returns non-zero for auth status.
	script := strings.Join([]string{
		"#!/usr/bin/env bash",
		`if [[ "${1:-}" == "--version" ]]; then printf 'claude 1.0.0-test\n'; exit 0; fi`,
		`exit 1`,
	}, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	agent := &claudeAgent{ClaudeParser: &ClaudeParser{}, workDir: workDir, model: ModelSonnet4}
	_, err := agent.Create(codeagent.CreateSessionParams{ID: "auth-fail"})
	require.Error(t, err, "Create should fail when claude auth status returns non-zero")
	assert.Contains(t, err.Error(), "not authenticated")
}

func TestResumeSession(t *testing.T) {
	for _, model := range claudeModels {
		model := model
		t.Run(model, func(t *testing.T) {
			workDir := t.TempDir()
			binDir := t.TempDir()
			logPath := filepath.Join(t.TempDir(), "resume.log")
			installFakeClaudeBinary(t, binDir, logPath)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			// Inject test-friendly stdio so Resume does not open /dev/tty.
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			prevIn, prevOut, prevErr := interactiveStdin, interactiveStdout, interactiveStderr
			interactiveStdin = strings.NewReader("")
			interactiveStdout = stdout
			interactiveStderr = stderr
			t.Cleanup(func() {
				interactiveStdin = prevIn
				interactiveStdout = prevOut
				interactiveStderr = prevErr
			})

			agent := &claudeAgent{ClaudeParser: &ClaudeParser{}, workDir: workDir, model: model}
			sessionID := "resume-" + model

			result, err := agent.Resume(codeagent.ResumeSessionParams{ID: sessionID})
			require.NoError(t, err, "Resume should succeed with a reachable claude binary")
			require.NotNil(t, result)
			assert.Equal(t, sessionID, result.SessionID)
			assert.NotEmpty(t, result.ProcessID)

			require.Eventually(t, func() bool {
				return len(readLogLines(t, logPath)) > 0
			}, 3*time.Second, 50*time.Millisecond, "Resume should invoke the claude binary")

			lines := readLogLines(t, logPath)
			assert.Contains(t, lines[0], "-r", "Resume should pass -r flag to claude")
			assert.Contains(t, lines[0], sessionID, "Resume should pass the session ID to claude")

			require.Eventually(t, func() bool {
				return strings.Contains(stdout.String(), "interactive-started")
			}, 3*time.Second, 50*time.Millisecond, "Resume should attach stdout to the injected writer")
		})
	}
}

func TestResumeSessionForkFlag(t *testing.T) {
	workDir := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "fork.log")

	// Extended stub that handles --fork-session flag.
	script := strings.Join([]string{
		"#!/usr/bin/env bash",
		`printf '%s\n' "$*" >> "` + logPath + `"`,
		`printf 'interactive-started\n'`,
		`read -r -t 5 _ || true`,
		`exit 0`,
	}, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout := &bytes.Buffer{}
	prevIn, prevOut, prevErr := interactiveStdin, interactiveStdout, interactiveStderr
	interactiveStdin = strings.NewReader("")
	interactiveStdout = stdout
	interactiveStderr = stdout
	t.Cleanup(func() {
		interactiveStdin = prevIn
		interactiveStdout = prevOut
		interactiveStderr = prevErr
	})

	agent := &claudeAgent{ClaudeParser: &ClaudeParser{}, workDir: workDir, model: ModelSonnet4}
	_, err := agent.Resume(codeagent.ResumeSessionParams{ID: "fork-session", ForkSession: true})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return len(readLogLines(t, logPath)) > 0
	}, 3*time.Second, 50*time.Millisecond)

	lines := readLogLines(t, logPath)
	assert.Contains(t, lines[0], "--fork-session", "Resume with ForkSession should pass --fork-session flag")
}

func TestSessionLifecycle(t *testing.T) {
	workDir := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "lifecycle.log")
	installFakeClaudeBinary(t, binDir, logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout := &bytes.Buffer{}
	prevIn, prevOut, prevErr := interactiveStdin, interactiveStdout, interactiveStderr
	interactiveStdin = strings.NewReader("")
	interactiveStdout = stdout
	interactiveStderr = stdout
	t.Cleanup(func() {
		interactiveStdin = prevIn
		interactiveStdout = prevOut
		interactiveStderr = prevErr
	})

	agent := &claudeAgent{ClaudeParser: &ClaudeParser{}, workDir: workDir, model: ModelSonnet4}

	// Create → Resume in sequence using the generic codeagent.CodeAgent interface.
	var ca codeagent.CodeAgent = agent

	createResult, err := ca.Create(codeagent.CreateSessionParams{
		ID:    "lifecycle-001",
		Name:  "lifecycle-test",
		Model: ModelSonnet4,
	})
	require.NoError(t, err, "Create should succeed")
	require.Equal(t, "lifecycle-001", createResult.ID)

	resumeResult, err := ca.Resume(codeagent.ResumeSessionParams{ID: createResult.ID})
	require.NoError(t, err, "Resume should succeed after Create")
	require.Equal(t, "lifecycle-001", resumeResult.SessionID)
	assert.NotEmpty(t, resumeResult.ProcessID)
}
