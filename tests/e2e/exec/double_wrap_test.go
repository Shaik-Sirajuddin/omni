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

// TestDoubleWrapFixClaude verifies that the exec double-wrap fix works for Claude:
// a nested exec inside a running agent session does not cause duplicate PTY wraps.
func TestDoubleWrapFixClaude(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	out, code := harness.ExecInContainer(t, cfg, "command -v claude 2>/dev/null")
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Log("WARNING: claude binary not found in container — double-wrap test requires a real claude session")
		t.Skip("claude binary not available")
	}

	agentName := "e2e-double-wrap-claude-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", "claude", "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "resume", agentName,
		"--detach", "--workspace", cfg.Workspace)
	time.Sleep(5 * time.Second)

	// First exec — establishes an active session
	out1, code1 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "say: first", "--bg")
	require.Equal(t, 0, code1, "first exec must succeed: %s", out1)
	time.Sleep(2 * time.Second)

	// Second exec while session is active — must not double-wrap
	start := time.Now()
	out2, code2 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "say: second", "--bg")
	elapsed := time.Since(start)

	assert.Equal(t, 0, code2, "second exec on active session must exit 0: %s", out2)
	assert.Less(t, elapsed.Milliseconds(), int64(8_000),
		"second exec --bg must not block (double-wrap), took %s", elapsed)

	// No double-wrap evidence in logs
	log := jrnl.String() + omniLog.String()
	assert.False(t, strings.Contains(log, "double wrap") || strings.Contains(log, "already wrapped"),
		"no double-wrap error must appear in logs")

	harness.AssertNoExecSessionFailed(t, log)
}

// TestDoubleWrapFixCodex verifies the same double-wrap fix for Codex.
func TestDoubleWrapFixCodex(t *testing.T) {
	t.Parallel()
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	out, code := harness.ExecInContainer(t, cfg, "command -v codex 2>/dev/null")
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Log("WARNING: codex binary not found in container — double-wrap test requires a real codex session")
		t.Skip("codex binary not available")
	}

	agentName := "e2e-double-wrap-codex-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", "codex", "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "resume", agentName,
		"--detach", "--workspace", cfg.Workspace)
	time.Sleep(5 * time.Second)

	out1, code1 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "say: first-codex", "--bg")
	require.Equal(t, 0, code1, "first codex exec must succeed: %s", out1)
	time.Sleep(2 * time.Second)

	start := time.Now()
	out2, code2 := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "say: second-codex", "--bg")
	elapsed := time.Since(start)

	assert.Equal(t, 0, code2, "second codex exec must exit 0: %s", out2)
	assert.Less(t, elapsed.Milliseconds(), int64(8_000),
		"second codex exec --bg must not block, took %s", elapsed)

	log := jrnl.String() + omniLog.String()
	harness.AssertNoExecSessionFailed(t, log)
}
