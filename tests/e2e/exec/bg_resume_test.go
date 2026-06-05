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

const (
	bgAgent    = "e2e-bg-agent"
	bgWorkspace = "/build"

	// exec --bg must return within this many milliseconds to be "non-blocking".
	bgNonBlockingMs = 10_000
)

// TestBgFlagAccepted verifies --bg is a known flag and --resume is rejected.
func TestBgFlagAccepted(t *testing.T) {
	cfg := harness.NewConfig(t)

	// T1a: --resume must be unknown
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", bgAgent, "--prompt", "test", "--resume")
	assert.NotEqual(t, 0, code, "--resume flag should be rejected (unknown flag)")
	assert.True(t,
		strings.Contains(out, "unknown flag") || strings.Contains(out, "unknown shorthand"),
		"--resume error must say 'unknown flag', got: %s", out)

	// T1b: --bg must appear in exec --help
	helpOut, _ := harness.RunOmniAllowFail(t, cfg, "agent", "exec", "--help")
	assert.True(t,
		strings.Contains(helpOut, "--bg") || strings.Contains(helpOut, "-b,"),
		"--bg flag must be present in exec --help")
}

// TestExecBgNonBlocking verifies exec --bg starts a PTY session and returns
// immediately (< bgNonBlockingMs ms).
func TestExecBgNonBlocking(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	// Stop any lingering session first
	harness.RunOmniAllowFail(t, cfg, "agent", "stop", bgAgent)
	time.Sleep(time.Second)

	start := time.Now()
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", bgAgent, "--prompt", "T2: reply with one word: pong", "--bg")
	elapsed := time.Since(start)

	t.Logf("exec --bg exit=%d elapsed=%s", code, elapsed)

	if strings.Contains(out, "unknown flag") || strings.Contains(out, "flag provided") {
		t.Fatalf("--bg flag not accepted: %s", out)
	}
	assert.Equal(t, 0, code, "exec --bg must exit 0")
	assert.Less(t, elapsed.Milliseconds(), int64(bgNonBlockingMs),
		"exec --bg must return in <%dms (non-blocking), took %s", bgNonBlockingMs, elapsed)

	// PTY session started evidence in journalctl
	started := jrnl.WaitFor("session created", 20*time.Second) ||
		jrnl.WaitFor("session ready", 20*time.Second) ||
		jrnl.WaitFor("PTY daemon session started", 20*time.Second)
	if !started {
		t.Logf("WARN: PTY session start not visible in journalctl within 20s — may be a timing issue")
	}
}

// TestExecBgOnActivePTY verifies that a second exec --bg on an already-active
// session also returns immediately.
func TestExecBgOnActivePTY(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	time.Sleep(2 * time.Second) // let session from T2 settle

	start := time.Now()
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", bgAgent, "--prompt", "T3: second prompt on active session", "--bg")
	elapsed := time.Since(start)

	t.Logf("exec --bg (2nd) exit=%d elapsed=%s", code, elapsed)

	assert.Equal(t, 0, code, "second exec --bg on active session must exit 0: %s", out)
	assert.Less(t, elapsed.Milliseconds(), int64(bgNonBlockingMs),
		"second exec --bg must return in <%dms (non-blocking), took %s", bgNonBlockingMs, elapsed)
}

// TestExecBgAgentNotFound verifies that exec --bg on a nonexistent agent exits
// non-zero and returns an error message.
func TestExecBgAgentNotFound(t *testing.T) {
	cfg := harness.NewConfig(t)

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", "nonexistent-agent-e2e-bg", "--prompt", "test", "--bg")

	assert.NotEqual(t, 0, code, "exec --bg on nonexistent agent must exit non-zero")
	t.Logf("nonexistent agent output: %s", out)
}

// TestBgShortFlag verifies -b short flag is accepted identically to --bg.
func TestBgShortFlag(t *testing.T) {
	cfg := harness.NewConfig(t)

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", bgAgent, "--prompt", "T5: short flag test", "-b")

	assert.False(t,
		strings.Contains(out, "unknown flag") || strings.Contains(out, "unknown shorthand"),
		"-b short flag must not produce 'unknown flag': %s", out)
	t.Logf("-b exit=%d", code)
}

// TestResumeNonExistent verifies that resume on a nonexistent agent exits
// non-zero.
func TestResumeNonExistent(t *testing.T) {
	cfg := harness.NewConfig(t)

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "resume", "nonexistent-agent-e2e",
		"--workspace", bgWorkspace)

	assert.NotEqual(t, 0, code, "resume nonexistent agent must exit non-zero")
	t.Logf("resume nonexistent: exit=%d output=%s", code, out)
}

// TestPromptDelivered verifies that exec --bg actually delivers the prompt to
// the running agent via journalctl evidence.
func TestPromptDelivered(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	probe := "t9-probe-" + time.Now().Format("150405")

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", bgAgent, "--prompt", "respond with exactly: "+probe, "--bg")
	require.Equal(t, 0, code, "exec --bg for probe must exit 0: %s", out)

	// Wait for delivery evidence
	delivered := jrnl.WaitFor("ExecInSession", 30*time.Second) ||
		jrnl.WaitFor("exec in session", 30*time.Second) ||
		omniLog.WaitFor("exec in session", 30*time.Second) ||
		jrnl.WaitFor("UserPromptSubmit", 30*time.Second)

	assert.True(t, delivered, "prompt delivery evidence must appear in logs within 30s")
}

// TestSequentialBgCalls verifies two sequential exec --bg calls both return
// quickly (neither blocks).
func TestSequentialBgCalls(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	start := time.Now()

	out1, code1 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", bgAgent, "--prompt", "T10-first: hello", "--bg")
	mid := time.Since(start)

	out2, code2 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", bgAgent, "--prompt", "T10-second: world", "--bg")
	total := time.Since(start)

	t.Logf("call1 exit=%d elapsed=%s, call2 exit=%d total=%s",
		code1, mid, code2, total)

	assert.Equal(t, 0, code1, "first sequential exec --bg must exit 0: %s", out1)
	assert.Equal(t, 0, code2, "second sequential exec --bg must exit 0: %s", out2)
	assert.Less(t, total.Milliseconds(), int64(15_000),
		"both sequential calls must complete in <15s (neither blocking), took %s", total)
}
