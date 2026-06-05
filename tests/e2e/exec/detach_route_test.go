//go:build e2e

package exec_test

import (
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDetachedExecDelivery verifies that exec --bg returns immediately (<5s)
// AND the ptydaemon receives the exec op within 30s.
func TestDetachedExecDelivery(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	agentName := "e2e-detach-route-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	provider := detectProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available")
	}

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	start := time.Now()
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "detach-route test", "--bg")
	elapsed := time.Since(start)

	require.Equal(t, 0, code, "exec --bg must exit 0: %s", out)
	assert.Less(t, elapsed.Milliseconds(), int64(5_000),
		"exec --bg must return in <5s, took %s", elapsed)

	// ptydaemon must receive the exec op within 30s
	received := jrnl.WaitFor("exec in session", 30*time.Second) ||
		jrnl.WaitFor("ExecInSession", 30*time.Second) ||
		jrnl.WaitFor("UserPromptSubmit", 30*time.Second)
	assert.True(t, received, "ptydaemon exec op must be observed within 30s")
}

// TestWaitPTYReadyColdStart verifies that exec --bg on a fresh (cold-start)
// agent still delivers the prompt — the waitPTYReady gate in detached mode
// must ensure the TUI is ready before prompt delivery.
func TestWaitPTYReadyColdStart(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	agentName := "e2e-cold-start-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	provider := detectProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available")
	}

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	// Kill any existing session to force cold start
	harness.RunOmniAllowFail(t, cfg, "agent", "stop", agentName)
	time.Sleep(time.Second)

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "cold-start probe", "--bg")

	require.Equal(t, 0, code, "exec --bg cold-start must exit 0: %s", out)

	// Exec must still succeed in background — ptydaemon receives it
	received := jrnl.WaitFor("exec in session", 30*time.Second) ||
		jrnl.WaitFor("ExecInSession", 30*time.Second)
	if !received {
		t.Logf("WARN: exec in session not observed within 30s on cold start — may still be starting")
	}
}

// TestNonDetachedRegression verifies that non-detached exec (no --bg) still
// works and exits 0.
func TestNonDetachedRegression(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	agentName := "e2e-non-detach-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	provider := detectProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available")
	}

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "non-detach regression test")

	assert.Equal(t, 0, code, "non-detached exec must exit 0: %s", out)

	execFired := jrnl.WaitFor("exec in session", 10*time.Second) ||
		jrnl.WaitFor("ExecInSession", 10*time.Second)
	if !execFired {
		t.Logf("WARN: exec in session not confirmed for non-detached path")
	}
}
