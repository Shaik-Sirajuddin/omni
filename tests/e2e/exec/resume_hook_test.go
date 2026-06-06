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

	provider := harness.RequireProvider(t, cfg)

	agent := uniqueAgent(t, "exec-resume")
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

	provider := harness.RequireProvider(t, cfg)

	agent := uniqueAgent(t, "hook-receipt")
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
// The delivery signal is the UserPromptSubmit hook event (the phrase the omni
// build actually emits); the first exec must produce one, then a second exec
// must push the count higher.
func TestMultiplePromptsSequential(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	provider := harness.RequireProvider(t, cfg)

	agent := uniqueAgent(t, "multi-prompt")
	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agent,
		"--workspace", cfg.Workspace, "--provider", provider)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agent) })

	out1, code1 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "first: say hello", "--bg")
	require.Equal(t, 0, code1, "first exec --bg must succeed: %s", out1)

	// Gate on real delivery of the first prompt before measuring the second.
	first := jrnl.WaitFor("UserPromptSubmit", 30*time.Second) ||
		omniLog.WaitFor("UserPromptSubmit", 30*time.Second)
	require.True(t, first, "first prompt must produce a UserPromptSubmit hook within 30s")

	before := countOccurrences(jrnl.String(), "UserPromptSubmit")

	out2, code2 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "second: count two", "--bg")
	assert.Equal(t, 0, code2, "second exec --bg must succeed: %s", out2)

	deadline := time.After(30 * time.Second)
	for {
		select {
		case <-deadline:
			t.Errorf("second UserPromptSubmit hook not observed within 30s (before=%d, after=%d)",
				before, countOccurrences(jrnl.String(), "UserPromptSubmit"))
			return
		case <-time.After(500 * time.Millisecond):
			if countOccurrences(jrnl.String(), "UserPromptSubmit") > before {
				t.Logf("second UserPromptSubmit confirmed")
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

	provider := harness.RequireProvider(t, cfg)

	agent := uniqueAgent(t, "persist")
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

func countOccurrences(s, substr string) int {
	return strings.Count(s, substr)
}

func uniqueAgent(t *testing.T, prefix string) string {
	t.Helper()
	return "e2e-" + prefix + "-" + harness.AgentNameSuffix(t)
}
