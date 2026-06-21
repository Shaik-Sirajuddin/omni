//go:build e2e

package memlayout_test

// UPG — Upgrade relocation scenarios
//
// These tests verify that `omni agent upgrade` physically moves the agent
// directory to the correct v3 layout location, updates the store, and is
// idempotent on re-run.
//
// Run from tests/e2e:
//
//	E2E_CONTAINER=omni-omni_main-ubuntu-1 go test -tags e2e -count=1 ./memlayout/...
//
// Container e2e is deferred and will be approved separately; these files are
// written here so they are ready for in-container execution.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UPG-01: `omni agent upgrade` relocates a v1 agent from agents/<name>
// to the v3 location <name>/ inside the memory root.
func TestUPG01UpgradeRelocatesV1AgentToV3(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	ctx := context.Background()

	agentName := "upgv1agent"

	// Scaffold a v1 agent via `omni agent create` (it will use workspace version)
	// We force v1 by writing version.yaml before the create.
	writeFile(t, cfg, memDir+"/version.yaml", "default_version: v1\n")

	// Create a v1 agent using the CLI.
	out, code := harness.RunOmniAllowFail(t, cfg, "agent", "create", agentName, "--workspace", cfg.Workspace, "--provider", "claude")
	t.Logf("create v1 agent: %s", out)
	require.Equal(t, 0, code, "agent create (v1) failed: %s", out)

	// Verify it was created at the v1 location: memory/agents/<name>/
	v1Dir := fmt.Sprintf("%s/agents/%s", memDir, agentName)
	checkCode, _, checkErr := cfg.Exec.RunCommand(ctx, []string{"test", "-d", v1Dir})
	require.NoError(t, checkErr)
	assert.Equal(t, 0, checkCode, "v1 agent dir must exist at agents/%s", agentName)

	// Now upgrade to v3.
	upgradeOut, upgradeCode := harness.RunOmniAllowFail(t, cfg, "agent", "upgrade", "--id", agentName, "--version", "v3", "--workspace", cfg.Workspace)
	t.Logf("upgrade to v3: %s", upgradeOut)
	require.Equal(t, 0, upgradeCode, "agent upgrade to v3 failed: %s", upgradeOut)

	// v3 dir must exist at memory/<name>/
	v3Dir := fmt.Sprintf("%s/%s", memDir, agentName)
	v3Code, _, v3Err := cfg.Exec.RunCommand(ctx, []string{"test", "-d", v3Dir})
	require.NoError(t, v3Err)
	assert.Equal(t, 0, v3Code, "v3 agent dir must exist at memory/%s after upgrade", agentName)

	// Old v1 dir must be gone.
	v1Code, _, v1Err := cfg.Exec.RunCommand(ctx, []string{"test", "-d", v1Dir})
	require.NoError(t, v1Err)
	assert.NotEqual(t, 0, v1Code, "old v1 agent dir must not exist after upgrade")

	// metadata.yaml must report version_code: 3.
	metaCode, metaOut, metaErr := cfg.Exec.RunCommand(ctx, []string{"grep", "version_code", v3Dir + "/metadata.yaml"})
	require.NoError(t, metaErr)
	require.Equal(t, 0, metaCode, "metadata.yaml must be readable")
	assert.True(t, strings.Contains(string(metaOut), "3"), "version_code must be 3 in metadata.yaml, got: %s", metaOut)
}

// UPG-02: `omni agent upgrade` is idempotent — running it twice on a v3
// agent must succeed (no error) and not duplicate or corrupt the dir.
func TestUPG02UpgradeIdempotentOnV3Agent(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	ctx := context.Background()

	agentName := "upgv3idem"

	// Create a v3 agent (default version).
	out, code := harness.RunOmniAllowFail(t, cfg, "agent", "create", agentName, "--workspace", cfg.Workspace, "--provider", "claude")
	t.Logf("create v3 agent: %s", out)
	require.Equal(t, 0, code, "agent create failed: %s", out)

	// First upgrade (already at v3 — should be a no-op, exit 0).
	out1, code1 := harness.RunOmniAllowFail(t, cfg, "agent", "upgrade", "--id", agentName, "--version", "v3", "--workspace", cfg.Workspace)
	t.Logf("first upgrade (idempotent): %s", out1)
	assert.Equal(t, 0, code1, "first idempotent upgrade must succeed: %s", out1)

	// Second upgrade — still a no-op.
	out2, code2 := harness.RunOmniAllowFail(t, cfg, "agent", "upgrade", "--id", agentName, "--version", "v3", "--workspace", cfg.Workspace)
	t.Logf("second upgrade (idempotent): %s", out2)
	assert.Equal(t, 0, code2, "second idempotent upgrade must succeed: %s", out2)

	// Agent dir must still exist at v3 location.
	v3Dir := fmt.Sprintf("%s/%s", memDir, agentName)
	checkCode, _, checkErr := cfg.Exec.RunCommand(ctx, []string{"test", "-d", v3Dir})
	require.NoError(t, checkErr)
	assert.Equal(t, 0, checkCode, "v3 agent dir must still exist after idempotent upgrades")
}

// UPG-03: After upgrade, the store reflects the new memory_dir so that
// subsequent `omni agent resume` resolves the correct location.
func TestUPG03UpgradePersistsNewMemoryDirInStore(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	agentName := "upgstore"

	// Create under v1.
	writeFile(t, cfg, memDir+"/version.yaml", "default_version: v1\n")
	out, code := harness.RunOmniAllowFail(t, cfg, "agent", "create", agentName, "--workspace", cfg.Workspace, "--provider", "claude")
	require.Equal(t, 0, code, "agent create (v1): %s", out)

	// Upgrade to v3.
	upOut, upCode := harness.RunOmniAllowFail(t, cfg, "agent", "upgrade", "--id", agentName, "--version", "v3", "--workspace", cfg.Workspace)
	require.Equal(t, 0, upCode, "upgrade to v3: %s", upOut)

	// `omni agent list` output should reference the v3 path.
	listOut, listCode := harness.RunOmniAllowFail(t, cfg, "agent", "list", "--workspace", cfg.Workspace)
	t.Logf("agent list after upgrade:\n%s", listOut)
	assert.Equal(t, 0, listCode, "agent list failed: %s", listOut)
	// The v3 path contains memory/<name>, not memory/agents/<name>.
	expectedPath := fmt.Sprintf("memory/%s", agentName)
	legacyPath := fmt.Sprintf("memory/agents/%s", agentName)
	assert.True(t, strings.Contains(listOut, expectedPath) || !strings.Contains(listOut, legacyPath),
		"agent list should show v3 memory path %q (not legacy %q): %s", expectedPath, legacyPath, listOut)
}
