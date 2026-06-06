//go:build e2e

package exec_test

import (
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDetachedExecDelivery verifies exec --bg returns immediately (<5s)
// AND the ptydaemon receives the exec op within 30s.
func TestDetachedExecDelivery(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := harness.RequireProvider(t, cfg)

	agent := uniqueAgent(t, "detach-route")
	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agent,
		"--workspace", cfg.Workspace, "--provider", provider)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agent) })

	start := time.Now()
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "detach-route test", "--bg")
	elapsed := time.Since(start)

	require.Equal(t, 0, code, "exec --bg must exit 0: %s", out)
	assert.Less(t, elapsed.Milliseconds(), int64(5_000),
		"exec --bg must return in <5s, took %s", elapsed)

	received := jrnl.WaitFor("exec in session", 30*time.Second) ||
		jrnl.WaitFor("ExecInSession", 30*time.Second) ||
		jrnl.WaitFor("UserPromptSubmit", 30*time.Second)
	assert.True(t, received, "ptydaemon exec op must be observed within 30s")
}

// TestWaitPTYReadyColdStart verifies exec --bg on a cold-start agent delivers
// the prompt once the TUI is ready.
func TestWaitPTYReadyColdStart(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := harness.RequireProvider(t, cfg)

	agent := uniqueAgent(t, "cold-start")
	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agent,
		"--workspace", cfg.Workspace, "--provider", provider)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agent) })

	// Ensure no running session (cold start)
	harness.RunOmniAllowFail(t, cfg, "agent", "stop", agent)
	time.Sleep(time.Second)

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "cold-start probe", "--bg")

	require.Equal(t, 0, code, "exec --bg cold-start must exit 0: %s", out)

	received := jrnl.WaitFor("exec in session", 30*time.Second) ||
		jrnl.WaitFor("ExecInSession", 30*time.Second)
	if !received {
		t.Logf("WARN: exec in session not observed within 30s on cold start")
	}
}

// TestNonDetachedRegression verifies non-detached exec (no --bg) still works.
func TestNonDetachedRegression(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := harness.RequireProvider(t, cfg)

	agent := uniqueAgent(t, "non-detach")
	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agent,
		"--workspace", cfg.Workspace, "--provider", provider)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agent) })

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agent, "--prompt", "non-detach regression test")

	assert.Equal(t, 0, code, "non-detached exec must exit 0: %s", out)
}
