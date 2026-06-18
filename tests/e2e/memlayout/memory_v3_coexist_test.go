//go:build e2e

package memlayout_test

// COEX — Coexistence scenarios (4 tests)
//
// Mixed workspaces where v1 legacy agents and v3 flat agents coexist.
// The validator handles this via resolveAgentByName which probes both
// memory/<name>/ (v3) and memory/agents/<name>/ (v1/v2).

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// COEX-01 Mixed workspace: v1 legacy + v3 flat — tree validate produces no spurious errors
func TestCOEX01MixedWorkspaceValidation(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// Create one v1 agent (under agents/).
	makeV1Agent(t, cfg, memDir, "legacy-one")
	// Create one v3 agent (flat at memDir/modern-one/).
	makeV3Agent(t, cfg, memDir, "modern-one")

	// Pass memDir as positional arg for tree-level validation.
	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", memDir)
	t.Logf("validate mixed workspace:\n%s", valOut)
	// Both agents must validate without errors.
	assert.Equal(t, 0, valCode, "mixed workspace must validate clean: %s", valOut)
}

// COEX-02 `--agent <legacy-v1>` resolves agents/ path correctly
func TestCOEX02LegacyAgentPathResolution(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	makeV1Agent(t, cfg, memDir, "legacy-two")

	// Validate by agent name — must resolve to agents/legacy-two/.
	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "legacy-two", "--memory-root", memDir)
	t.Logf("validate legacy-two by name:\n%s", valOut)
	assert.Equal(t, 0, valCode, "legacy v1 agent by name must validate clean: %s", valOut)
}

// COEX-03 sys-instructs returns correct path per version in mixed workspace
func TestCOEX03SysInstructsPerVersionInMixed(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// v1 legacy agent.
	makeV1Agent(t, cfg, memDir, "legacy-three")
	// v3 flat agent (manual scaffold — not via agent init, to avoid provider dependency).
	makeV3Agent(t, cfg, memDir, "modern-three")

	// Register both agents so the operator knows them.
	// Use agent init for v1 (with v1 pin so the operator registers the agent).
	// Actually, sys-instructs in the CLI uses ResolveAgentSysInstructionsPath which
	// probes disk directly without querying the DB — so no registration needed.
	// EXCEPTION: omni agent get <name> sys-instructs does NOT require the agent to be in DB;
	// it resolves from disk layout. Confirmed in entrypoint.go:289.

	// legacy-three: v1 → entry/instructions path.
	legacyOut, legacyCode := harness.RunOmniAllowFail(t, cfg,
		"agent", "get", "legacy-three", "sys-instructs",
		"--workspace", cfg.Workspace)
	legacyPath := strings.TrimSpace(legacyOut)
	t.Logf("legacy-three sys-instructs: %q (exit=%d)", legacyPath, legacyCode)
	require.Equal(t, 0, legacyCode, "legacy-three sys-instructs must exit 0: %s", legacyOut)
	assert.True(t,
		strings.HasSuffix(legacyPath, "/agents/legacy-three/entry/instructions"),
		"v1 sys-instructs must end with entry/instructions, got: %q", legacyPath)

	// modern-three: v3 → instructions/memory.md path.
	modernOut, modernCode := harness.RunOmniAllowFail(t, cfg,
		"agent", "get", "modern-three", "sys-instructs",
		"--workspace", cfg.Workspace)
	modernPath := strings.TrimSpace(modernOut)
	t.Logf("modern-three sys-instructs: %q (exit=%d)", modernPath, modernCode)
	require.Equal(t, 0, modernCode, "modern-three sys-instructs must exit 0: %s", modernOut)
	assert.True(t,
		strings.HasSuffix(modernPath, "/modern-three/instructions/memory.md"),
		"v3 sys-instructs must end with instructions/memory.md, got: %q", modernPath)
}

// COEX-04 Infra dirs (collab/, circuits/, templates/, tools/) skipped in tree validation
func TestCOEX04InfraDirsSkippedInValidation(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// One valid v3 agent.
	makeV3Agent(t, cfg, memDir, "infra-host")

	// Create infra dirs intentionally WITHOUT memory.md — to verify they are skipped.
	for _, d := range []string{"collab", "circuits", "templates", "tools"} {
		out, code := harness.ExecInContainer(t, cfg,
			fmt.Sprintf("mkdir -p '%s/%s'", memDir, d))
		require.Equal(t, 0, code, "mkdir %s: %s", d, out)
	}

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", memDir)
	t.Logf("validate with infra dirs:\n%s", valOut)
	// infraDirs are skipped — no violations for empty collab/, circuits/, etc.
	assert.Equal(t, 0, valCode, "infra dirs must not cause validation errors: %s", valOut)
}
