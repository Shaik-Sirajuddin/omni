//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── team commands ─────────────────────────────────────────────────────────────

// TestOmniTeamInit verifies `omni team init` succeeds in an isolated workspace.
func TestOmniTeamInit(t *testing.T) {
	cfg := newConfig(t)
	_, logBuf := captureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)

	out := runOmni(t, cfg, "team", "init")

	assert.True(t,
		strings.Contains(out, "team initialized") || strings.Contains(out, "team reinitialized"),
		"expected 'team initialized' or 'team reinitialized', got: %s", out,
	)
	t.Logf("journalctl snapshot:\n%s", logBuf.String())
}

// TestOmniTeamList verifies `omni team list` returns at least one entry after init.
// team list has no --workspace flag; --output json is supported.
func TestOmniTeamList(t *testing.T) {
	cfg := newConfig(t)
	_, logBuf := captureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)

	runOmni(t, cfg, "team", "init")

	out := runOmni(t, cfg, "team", "list", "--output", "json")

	require.NotEmpty(t, out, "team list output must not be empty")
	assert.Contains(t, out, cfg.workspace, "team list JSON should reference the test workspace")
	t.Logf("journalctl snapshot:\n%s", logBuf.String())
}

// TestOmniTeamGet verifies `omni team get` returns workspace details.
// team get resolves by CWD when --id is omitted; --output json supported.
func TestOmniTeamGet(t *testing.T) {
	cfg := newConfig(t)
	_, logBuf := captureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)

	runOmni(t, cfg, "team", "init")

	out := runOmni(t, cfg, "team", "get", "--output", "json")

	require.NotEmpty(t, out)
	assert.Contains(t, out, cfg.workspace, "team get should reference the test workspace dir")
	t.Logf("journalctl snapshot:\n%s", logBuf.String())
}

// ─── agent commands ────────────────────────────────────────────────────────────

// TestOmniAgentList verifies `omni agent list` exits 0.
// agent list supports --workspace.
func TestOmniAgentList(t *testing.T) {
	cfg := newConfig(t)
	_, logBuf := captureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)

	out := runOmni(t, cfg, "agent", "list", "--output", "json", "--workspace", cfg.workspace)

	require.NotNil(t, out)
	t.Logf("agent list output: %s", out)
	t.Logf("journalctl snapshot:\n%s", logBuf.String())
}

// TestOmniAgentCreate verifies `omni agent init <name>` creates an agent visible in list.
// Teardown: omni agent delete via CLI — no docker restart needed.
func TestOmniAgentCreate(t *testing.T) {
	cfg := newConfig(t)
	_, logBuf := captureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)

	agentName := "e2e-agent-create-test"
	t.Cleanup(func() { teardownAgent(t, cfg, agentName) })

	runOmni(t, cfg, "team", "init")
	runOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.workspace,
		"--provider", "claude",
	)

	listOut := runOmni(t, cfg, "agent", "list", "--output", "json", "--workspace", cfg.workspace)
	assert.Contains(t, listOut, agentName, "created agent should appear in list")

	t.Logf("journalctl snapshot:\n%s", logBuf.String())
}

// TestOmniAgentDelete verifies `omni agent delete <name>` removes the agent.
// Create → delete → assert gone. Safety cleanup registered before creation.
func TestOmniAgentDelete(t *testing.T) {
	cfg := newConfig(t)
	_, logBuf := captureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)

	agentName := "e2e-agent-delete-test"
	t.Cleanup(func() { teardownAgent(t, cfg, agentName) })

	runOmni(t, cfg, "team", "init")
	runOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.workspace,
		"--provider", "claude",
	)

	listOut := runOmni(t, cfg, "agent", "list", "--output", "json", "--workspace", cfg.workspace)
	require.Contains(t, listOut, agentName, "agent must exist before delete")

	runOmni(t, cfg, "agent", "delete", agentName, "--workspace", cfg.workspace)

	listAfter := runOmni(t, cfg, "agent", "list", "--output", "json", "--workspace", cfg.workspace)
	assert.NotContains(t, listAfter, agentName, "deleted agent must not appear in list")

	t.Logf("journalctl snapshot:\n%s", logBuf.String())
}

// TestOmniAgentCreateDuplicateName verifies a duplicate create is handled gracefully.
// Checks server stays healthy after the attempt.
func TestOmniAgentCreateDuplicateName(t *testing.T) {
	cfg := newConfig(t)
	_, logBuf := captureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)

	agentName := "e2e-agent-dup-test"
	t.Cleanup(func() { teardownAgent(t, cfg, agentName) })

	runOmni(t, cfg, "team", "init")
	runOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.workspace,
		"--provider", "claude",
	)

	out, code := runOmniAllowFail(t, cfg,
		"agent", "init", agentName,
		"--workspace", cfg.workspace,
		"--provider", "claude",
	)
	t.Logf("duplicate create → exit=%d out=%s", code, out)

	// server must still be healthy regardless of exit code
	listOut := runOmni(t, cfg, "agent", "list", "--output", "json", "--workspace", cfg.workspace)
	assert.Contains(t, listOut, agentName)

	t.Logf("journalctl snapshot:\n%s", logBuf.String())
}
