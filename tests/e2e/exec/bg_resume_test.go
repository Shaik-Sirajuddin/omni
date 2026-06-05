//go:build e2e

package exec_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const bgNonBlockingMs = 10_000

// initBgAgent creates a fresh agent in the service workspace and registers
// cleanup. Returns the unique agent name.
func initBgAgent(t *testing.T, cfg harness.TestConfig) string {
	t.Helper()
	name := "e2e-bg-" + harness.AgentNameSuffix(t)
	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", name,
		"--workspace", cfg.Workspace, "--provider", "codex")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, name) })
	return name
}

// TestBgFlagAccepted verifies --bg is a known flag and --resume is rejected.
func TestBgFlagAccepted(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)

	// --resume must be unknown
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", "any-agent", "--prompt", "test", "--resume")
	assert.NotEqual(t, 0, code, "--resume flag should be rejected")
	assert.True(t,
		strings.Contains(out, "unknown flag") || strings.Contains(out, "unknown shorthand"),
		"--resume error must say 'unknown flag', got: %s", out)

	// --bg must appear in exec --help
	helpOut, _ := harness.RunOmniAllowFail(t, cfg, "agent", "exec", "--help")
	assert.True(t,
		strings.Contains(helpOut, "--bg") || strings.Contains(helpOut, "-b,"),
		"--bg flag must be present in exec --help")
}

// TestExecBgNonBlocking verifies exec --bg returns in < bgNonBlockingMs.
func TestExecBgNonBlocking(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	agent := initBgAgent(t, cfg)

	start := time.Now()
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "reply with one word: pong", "--bg")
	elapsed := time.Since(start)

	t.Logf("exec --bg exit=%d elapsed=%s", code, elapsed)

	if strings.Contains(out, "unknown flag") || strings.Contains(out, "flag provided") {
		t.Fatalf("--bg flag not accepted: %s", out)
	}
	assert.Equal(t, 0, code, "exec --bg must exit 0: %s", out)
	assert.Less(t, elapsed.Milliseconds(), int64(bgNonBlockingMs),
		"exec --bg must return in <%dms (non-blocking), took %s", bgNonBlockingMs, elapsed)

	started := jrnl.WaitFor("session created", 20*time.Second) ||
		jrnl.WaitFor("session ready", 20*time.Second) ||
		jrnl.WaitFor("PTY daemon session started", 20*time.Second)
	if !started {
		t.Logf("WARN: PTY session start not visible in journalctl within 20s")
	}
}

// TestExecBgOnActivePTY verifies a second exec --bg on an active session
// also returns immediately.
func TestExecBgOnActivePTY(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	agent := initBgAgent(t, cfg)

	// First call starts the session
	harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "first prompt", "--bg")
	time.Sleep(2 * time.Second)

	// Second call on active session must also be non-blocking
	start := time.Now()
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "second prompt on active session", "--bg")
	elapsed := time.Since(start)

	t.Logf("exec --bg (2nd) exit=%d elapsed=%s", code, elapsed)

	assert.Equal(t, 0, code, "second exec --bg on active session must exit 0: %s", out)
	assert.Less(t, elapsed.Milliseconds(), int64(bgNonBlockingMs),
		"second exec --bg must return in <%dms (non-blocking), took %s", bgNonBlockingMs, elapsed)
}

// TestExecBgAgentNotFound verifies exec --bg on a nonexistent agent exits non-zero.
func TestExecBgAgentNotFound(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", "nonexistent-agent-e2e-bg-"+harness.AgentNameSuffix(t), "--prompt", "test", "--bg")

	assert.NotEqual(t, 0, code, "exec --bg on nonexistent agent must exit non-zero")
	t.Logf("nonexistent agent output: %s", out)
}

// TestBgShortFlag verifies -b short flag is accepted identically to --bg.
func TestBgShortFlag(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)

	agent := initBgAgent(t, cfg)

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "short flag test", "-b")

	assert.False(t,
		strings.Contains(out, "unknown flag") || strings.Contains(out, "unknown shorthand"),
		"-b short flag must not produce 'unknown flag': %s", out)
	assert.Equal(t, 0, code, "-b must exit 0: %s", out)
}

// TestResumeNonExistent verifies resume on a nonexistent agent exits non-zero.
func TestResumeNonExistent(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "resume", "nonexistent-agent-e2e-"+harness.AgentNameSuffix(t),
		"--workspace", cfg.Workspace)

	assert.NotEqual(t, 0, code, "resume nonexistent agent must exit non-zero")
	t.Logf("resume nonexistent: exit=%d output=%s", code, out)
}

// TestPromptDelivered verifies exec --bg delivers the prompt — hook event
// or exec-in-session appears in logs within 30s.
func TestPromptDelivered(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	agent := initBgAgent(t, cfg)

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "respond with exactly: pong-probe", "--bg")
	require.Equal(t, 0, code, "exec --bg must exit 0: %s", out)

	delivered := jrnl.WaitFor("ExecInSession", 30*time.Second) ||
		jrnl.WaitFor("exec in session", 30*time.Second) ||
		omniLog.WaitFor("exec in session", 30*time.Second) ||
		jrnl.WaitFor("UserPromptSubmit", 30*time.Second)

	assert.True(t, delivered, "prompt delivery evidence must appear in logs within 30s")
}

// TestSequentialBgCalls verifies two sequential exec --bg calls both return
// quickly (combined < 15s).
func TestSequentialBgCalls(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	agent := initBgAgent(t, cfg)

	start := time.Now()
	out1, code1 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "first: hello", "--bg")
	mid := time.Since(start)

	out2, code2 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "second: world", "--bg")
	total := time.Since(start)

	t.Logf("call1 exit=%d elapsed=%s, call2 exit=%d total=%s",
		code1, mid, code2, total)

	assert.Equal(t, 0, code1, "first sequential exec --bg must exit 0: %s", out1)
	assert.Equal(t, 0, code2, "second sequential exec --bg must exit 0: %s", out2)
	assert.Less(t, total.Milliseconds(), int64(15_000),
		"both sequential calls must complete in <15s, took %s", total)
}
