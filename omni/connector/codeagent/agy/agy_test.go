package agy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installFakeAgyBinary writes a bash stub that mimics the agy CLI.
// It logs each invocation to logPath and handles --version, auth status,
// and -r <id> (resume) so the connector's lifecycle methods can be tested
// without a real agy binary.
func installFakeAgyBinary(t *testing.T, dir, logPath string) {
	t.Helper()
	script := strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		`printf '%s\n' "$*" >> "` + logPath + `"`,
		`if [[ "${1:-}" == "--version" ]]; then`,
		`  printf 'agy 1.0.0-test\n'`,
		`  exit 0`,
		`fi`,
		`if [[ "${1:-}" == "auth" && "${2:-}" == "status" ]]; then`,
		`  exit 0`,
		`fi`,
		`if [[ "${1:-}" == "-p" ]]; then`,
		`  printf '{"type":"result","result":"ok"}\n'`,
		`  exit 0`,
		`fi`,
		`if [[ "${1:-}" == "--conversation" ]]; then`,
		`  printf 'interactive-started\n'`,
		`  read -r -t 30 _ || true`,
		`  exit 0`,
		`fi`,
		`printf 'unexpected args: %s\n' "$*" >&2`,
		`exit 1`,
	}, "\n")
	path := filepath.Join(dir, "agy")
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
var agyModels = []string{
	ModelGeminiPro,
	ModelGeminiFlash,
}

func TestCreateSession(t *testing.T) {
	for _, model := range agyModels {
		model := model
		t.Run(model, func(t *testing.T) {
			workDir := t.TempDir()
			binDir := t.TempDir()
			logPath := filepath.Join(t.TempDir(), "create.log")
			installFakeAgyBinary(t, binDir, logPath)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			agent := &agyAgent{AgyParser: &AgyParser{}, workDir: workDir, model: model, binPath: filepath.Join(binDir, "agy")}
			result, err := agent.Create(codeagent.CreateSessionParams{
				ID:    "test-session-" + model,
				Name:  "test-" + model,
				Model: model,
			})
			require.NoError(t, err, "Create should succeed with a reachable agy binary")
			require.NotNil(t, result)
			assert.Equal(t, "test-session-"+model, result.ID)
			assert.Equal(t, "test-"+model, result.Name)

			lines := readLogLines(t, logPath)
			require.NotEmpty(t, lines, "Create should invoke the agy binary")
			assert.Contains(t, lines[0], "--version", "Create should probe binary version first")
		})
	}
}

func TestCreateSessionGeneratesIDWhenEmpty(t *testing.T) {
	workDir := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "create-genid.log")
	installFakeAgyBinary(t, binDir, logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	agent := &agyAgent{AgyParser: &AgyParser{}, workDir: workDir, model: ModelGeminiPro, binPath: filepath.Join(binDir, "agy")}
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
		`if [[ "${1:-}" == "--version" ]]; then printf 'agy 1.0.0-test\n'; exit 0; fi`,
		`exit 1`,
	}, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "agy"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	agent := &agyAgent{AgyParser: &AgyParser{}, workDir: workDir, model: ModelGeminiPro, binPath: filepath.Join(binDir, "agy")}
	_, err := agent.Create(codeagent.CreateSessionParams{ID: "auth-fail"})
	require.Error(t, err, "Create should fail when agy auth status returns non-zero")
	assert.Contains(t, err.Error(), "not authenticated")
}

func TestResumeSession(t *testing.T) {
	for _, model := range agyModels {
		model := model
		t.Run(model, func(t *testing.T) {
			workDir := t.TempDir()
			binDir := t.TempDir()
			logPath := filepath.Join(t.TempDir(), "resume.log")
			installFakeAgyBinary(t, binDir, logPath)
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

			agent := &agyAgent{AgyParser: &AgyParser{}, workDir: workDir, model: model, binPath: filepath.Join(binDir, "agy")}
			sessionID := "resume-" + model

			result, err := agent.Resume(codeagent.ResumeSessionParams{ID: sessionID})
			require.NoError(t, err, "Resume should succeed with a reachable agy binary")
			require.NotNil(t, result)
			assert.Equal(t, sessionID, result.SessionID)
			assert.NotEmpty(t, result.ProcessID)

			require.Eventually(t, func() bool {
				return len(readLogLines(t, logPath)) > 0
			}, 3*time.Second, 50*time.Millisecond, "Resume should invoke the agy binary")

			lines := readLogLines(t, logPath)
			assert.Contains(t, lines[0], "--conversation", "Resume should pass --conversation flag to agy")
			assert.Contains(t, lines[0], sessionID, "Resume should pass the session ID to agy")

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
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "agy"), []byte(script), 0o755))
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

	agent := &agyAgent{AgyParser: &AgyParser{}, workDir: workDir, model: ModelGeminiPro, binPath: filepath.Join(binDir, "agy")}
	_, err := agent.Resume(codeagent.ResumeSessionParams{ID: "fork-session", ForkSession: true})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return len(readLogLines(t, logPath)) > 0
	}, 3*time.Second, 50*time.Millisecond)

	lines := readLogLines(t, logPath)
	assert.Contains(t, lines[0], "--continue", "Resume with ForkSession should pass --continue flag")
}

func TestSessionLifecycle(t *testing.T) {
	workDir := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "lifecycle.log")
	installFakeAgyBinary(t, binDir, logPath)
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

	agent := &agyAgent{AgyParser: &AgyParser{}, workDir: workDir, model: ModelGeminiPro, binPath: filepath.Join(binDir, "agy")}

	// Create → Resume in sequence using the generic codeagent.CodeAgent interface.
	var ca codeagent.CodeAgent = agent

	createResult, err := ca.Create(codeagent.CreateSessionParams{
		ID:    "lifecycle-001",
		Name:  "lifecycle-test",
		Model: ModelGeminiPro,
	})
	require.NoError(t, err, "Create should succeed")
	require.Equal(t, "lifecycle-001", createResult.ID)

	resumeResult, err := ca.Resume(codeagent.ResumeSessionParams{ID: createResult.ID})
	require.NoError(t, err, "Resume should succeed after Create")
	require.Equal(t, "lifecycle-001", resumeResult.SessionID)
	assert.NotEmpty(t, resumeResult.ProcessID)
}
