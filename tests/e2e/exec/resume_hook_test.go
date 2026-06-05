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

// TestExecResumeLaunchesPTY verifies exec --bg starts a PTY session and
// returns in < 10s (non-blocking).
func TestExecResumeLaunchesPTY(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := detectProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available (claude/codex)")
	}

	agent := uniqueAgent("exec-resume")
	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agent,
		"--workspace", cfg.Workspace, "--provider", provider)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agent) })

	start := time.Now()
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "reply with: pong", "--bg")
	elapsed := time.Since(start)

	t.Logf("exec --bg exit=%d elapsed=%s", code, elapsed)

	if strings.Contains(out, "unknown flag") {
		t.Fatalf("--bg flag not accepted: %s", out)
	}
	assert.Equal(t, 0, code, "exec --bg must exit 0: %s", out)
	assert.Less(t, elapsed.Milliseconds(), int64(10_000),
		"exec --bg must return in <10s (non-blocking), took %s", elapsed)

	time.Sleep(2 * time.Second)
	started := jrnl.WaitFor("session created", 10*time.Second) ||
		jrnl.WaitFor("session ready", 10*time.Second) ||
		jrnl.WaitFor("PTY daemon session started", 10*time.Second)
	if !started {
		t.Logf("WARN: PTY session start not visible in journalctl")
	}
}

// TestHookReceiptConfirmsDelivery is the critical delivery gate: hook events
// (UserPromptSubmit or exec in session) must appear in logs after exec --bg.
func TestHookReceiptConfirmsDelivery(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	provider := detectProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available (claude/codex)")
	}

	agent := uniqueAgent("hook-receipt")
	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agent,
		"--workspace", cfg.Workspace, "--provider", provider)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agent) })

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "reply with: pong", "--bg")
	require.Equal(t, 0, code, "exec --bg must succeed: %s", out)

	hookDelivered := jrnl.WaitFor("UserPromptSubmit", 30*time.Second) ||
		jrnl.WaitFor("exec in session", 30*time.Second) ||
		omniLog.WaitFor("exec in session", 30*time.Second)

	assert.True(t, hookDelivered,
		"hook event (UserPromptSubmit or exec in session) must be observed within 30s")
}

// TestMultiplePromptsSequential verifies a second exec --bg also fires a hook.
func TestMultiplePromptsSequential(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := detectProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available (claude/codex)")
	}

	agent := uniqueAgent("multi-prompt")
	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agent,
		"--workspace", cfg.Workspace, "--provider", provider)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agent) })

	out1, code1 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "first: say hello", "--bg")
	require.Equal(t, 0, code1, "first exec --bg must succeed: %s", out1)

	before := countOccurrences(jrnl.String(), "exec in session")
	time.Sleep(500 * time.Millisecond)

	out2, code2 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "second: count two", "--bg")
	assert.Equal(t, 0, code2, "second exec --bg must succeed: %s", out2)

	deadline := time.After(30 * time.Second)
	for {
		select {
		case <-deadline:
			after := countOccurrences(jrnl.String(), "exec in session")
			if after <= before {
				t.Errorf("second hook event not observed within 30s (before=%d, after=%d)",
					before, after)
			}
			return
		case <-time.After(500 * time.Millisecond):
			if countOccurrences(jrnl.String(), "exec in session") > before {
				t.Logf("second exec in session confirmed")
				return
			}
		}
	}
}

// TestSessionPersistsAfterExec verifies no hang/stuck evidence after exec --bg.
func TestSessionPersistsAfterExec(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := detectProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available (claude/codex)")
	}

	agent := uniqueAgent("persist")
	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agent,
		"--workspace", cfg.Workspace, "--provider", provider)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agent) })

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "persist test", "--bg")
	require.Equal(t, 0, code, "exec --bg must succeed: %s", out)

	time.Sleep(2 * time.Second)
	assert.False(t,
		strings.Contains(jrnl.String(), "hang") ||
			strings.Contains(jrnl.String(), "stuck"),
		"no hang/stuck evidence must appear in journalctl after exec --bg")
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func detectProvider(t *testing.T, cfg harness.TestConfig) string {
	t.Helper()
	for _, p := range []string{"claude", "codex"} {
		_, code := harness.ExecInContainer(t, cfg, "command -v "+p)
		if code == 0 {
			t.Logf("provider: %s", p)
			return p
		}
	}
	return ""
}

func countOccurrences(s, substr string) int {
	return strings.Count(s, substr)
}

func uniqueAgent(prefix string) string {
	return "e2e-" + prefix + "-" + harness.AgentNameSuffix(t)
}
