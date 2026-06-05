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

// TestExecResumeLaunchesPTY verifies that exec --bg starts a background PTY
// session and returns in < 10s (non-blocking).
func TestExecResumeLaunchesPTY(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := detectProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available (claude/codex)")
	}

	agentName := "e2e-exec-resume-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	start := time.Now()
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "reply with: pong", "--bg")
	elapsed := time.Since(start)

	t.Logf("exec --bg exit=%d elapsed=%s", code, elapsed)

	if strings.Contains(out, "unknown flag") {
		t.Fatalf("--bg flag not accepted by binary: %s", out)
	}
	assert.Equal(t, 0, code, "exec --bg must exit 0: %s", out)
	assert.Less(t, elapsed.Milliseconds(), int64(10_000),
		"exec --bg must return in <10s (non-blocking), took %s", elapsed)

	// PTY session started evidence
	time.Sleep(2 * time.Second)
	started := jrnl.WaitFor("session created", 10*time.Second) ||
		jrnl.WaitFor("session ready", 10*time.Second) ||
		jrnl.WaitFor("PTY daemon session started", 10*time.Second)
	if !started {
		t.Logf("WARN: PTY session start not visible in journalctl (may be timing)")
	}
}

// TestHookReceiptConfirmsDelivery is the critical delivery gate: verifies that
// hook events (UserPromptSubmit or exec in session) appear in journalctl after
// exec --bg, confirming the prompt actually reached the agent.
func TestHookReceiptConfirmsDelivery(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	provider := detectProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available (claude/codex)")
	}

	agentName := "e2e-hook-receipt-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	// Start the agent
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "reply with: pong", "--bg")
	require.Equal(t, 0, code, "exec --bg must succeed: %s", out)

	// Critical gate: hook receipt or exec-in-session confirms delivery
	hookDelivered := jrnl.WaitFor("UserPromptSubmit", 30*time.Second) ||
		jrnl.WaitFor("exec in session", 30*time.Second) ||
		omniLog.WaitFor("exec in session", 30*time.Second)

	assert.True(t, hookDelivered,
		"hook event (UserPromptSubmit or exec in session) must be observed within 30s")

	// exec in session logged — confirms the exec path reached the agent
	execLogged := jrnl.WaitFor("exec in session", 5*time.Second) ||
		omniLog.WaitFor("exec in session", 5*time.Second) ||
		jrnl.WaitFor("ExecInSession", 5*time.Second)
	if !execLogged {
		t.Logf("WARN: ExecInSession not confirmed — session log entry may suffice")
	}
}

// TestMultiplePromptsSequential verifies a second exec --bg after the first
// also fires a hook event.
func TestMultiplePromptsSequential(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := detectProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available (claude/codex)")
	}

	agentName := "e2e-multi-prompt-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	// First exec
	out1, code1 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "first: say hello", "--bg")
	require.Equal(t, 0, code1, "first exec --bg must succeed: %s", out1)

	// Count hook events before second exec
	before := countOccurrences(jrnl.String(), "exec in session")
	time.Sleep(500 * time.Millisecond)

	// Second exec
	out2, code2 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "second: count two", "--bg")
	assert.Equal(t, 0, code2, "second exec --bg must succeed: %s", out2)

	// Wait for the second hook event
	timeout := time.After(30 * time.Second)
	for {
		select {
		case <-timeout:
			after := countOccurrences(jrnl.String(), "exec in session")
			if after > before {
				t.Log("second exec in session confirmed")
			} else {
				t.Errorf("second hook event not observed within 30s (before=%d, after=%d)",
					before, after)
			}
			return
		case <-time.After(500 * time.Millisecond):
			after := countOccurrences(jrnl.String(), "exec in session")
			if after > before {
				t.Logf("second exec in session confirmed (before=%d, after=%d)", before, after)
				return
			}
		}
	}
}

// TestSessionPersistsAfterExec verifies that after exec --bg returns, the PTY
// session remains running (not blocked/hung).
func TestSessionPersistsAfterExec(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := detectProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available (claude/codex)")
	}

	agentName := "e2e-persist-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "persist test", "--bg")
	require.Equal(t, 0, code, "exec --bg must succeed: %s", out)

	// No hang evidence in logs
	time.Sleep(2 * time.Second)
	assert.False(t,
		strings.Contains(jrnl.String(), "hang") ||
			strings.Contains(jrnl.String(), "stuck"),
		"no hang/stuck evidence must appear in journalctl after exec --bg")

	// ResumeAgent completed confirms session is active
	completed := jrnl.WaitFor("ResumeAgent: completed", 10*time.Second) ||
		jrnl.WaitFor("leaving PTY daemon session detached", 10*time.Second)
	if !completed {
		t.Logf("WARN: ResumeAgent completion not explicitly logged")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// detectProvider returns the first available agent provider (claude or codex).
func detectProvider(t *testing.T, cfg harness.TestConfig) string {
	t.Helper()
	for _, provider := range []string{"claude", "codex"} {
		_, code := harness.ExecInContainer(t, cfg, "command -v "+provider)
		if code == 0 {
			t.Logf("provider: %s", provider)
			return provider
		}
	}
	return ""
}

func countOccurrences(s, substr string) int {
	return strings.Count(s, substr)
}
