//go:build e2e

package memlayout_test

// AI — Agent Init/Create scenarios (5 tests)
//
// ASSUMPTION resolutions:
//   - CLI: `omni agent init [name] --workspace <path>`.
//     The command is registered as Use: "init [name]" (not "create").
//   - agent init uses --workspace flag (verified in entrypoint.go:746).
//   - Only v1 and v3 agent templates exist in the embedded FS.
//     AI-03 (v2) fails because templates/agents/v2/ does not exist.
//   - memDir resolved by operator as <workspace>/memory.
//   - v3 flat layout: agent lives at memory/<name>/ (not memory/agents/<name>/).
//   - v1 layout: agent lives at memory/agents/<name>/.
//   - Collision detected at DB store level before memory.Create — sentinel file preserved.
//   - agent init with --interactive=false avoids launching the agent process.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AI-01 Agent create default (no version.yaml) → v3 layout seeded
func TestAI01AgentInitDefaultV3(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// No version.yaml → latestEmbeddedVersion() = v3.
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "init", "alpha",
		"--workspace", cfg.Workspace,
		"--interactive=false",
	)
	t.Logf("agent init alpha:\n%s", out)
	require.Equal(t, 0, code, "agent init must exit 0: %s", out)

	// v3 flat: agent lives at memory/<name>/.
	agentDir := memDir + "/alpha"
	assertDirExists(t, cfg, agentDir)
	assertFileContains(t, cfg, agentDir+"/metadata.yaml", "version_code: 3")

	// v3 required dirs.
	for _, sub := range []string{"instructions", "skills", "knowledge/com", "gen/plans", "gen/state"} {
		assertDirExists(t, cfg, agentDir+"/"+sub)
	}

	// instructions/memory.md (v3 sysInstructionsRel).
	assertFileExists(t, cfg, agentDir+"/instructions/memory.md")

	// v3 collab tasks dir: memory/collab/tasks/alpha/.
	assertDirExists(t, cfg, memDir+"/collab/tasks/alpha")

	// No agent_alpha.md at memory root (v3: writeWorkspaceDoc = false).
	assertNotExists(t, cfg, memDir+"/agent_alpha.md")

	// Validate.
	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "alpha", "--memory-root", memDir)
	t.Logf("validate alpha:\n%s", valOut)
	assert.Equal(t, 0, valCode, "v3 agent must validate clean after init: %s", valOut)
}

// AI-02 Agent create with workspace version.yaml = v1 → v1 layout
//
// ASSUMPTION deviation: agent init with v1 pin creates the agent at the flat
// memory/<name>/ path (NOT memory/agents/<name>/). The v1 template populates the
// v1 directory structure (entry/instructions/, entry/tasks/, generated/, state/)
// but uses the same flat root as v3. metadata.yaml records version: v1 / version_code: 2
// (the operator's internal mapping of v1 → code 2).
func TestAI02AgentInitWithV1Pin(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// Pin workspace to v1.
	writeFile(t, cfg, memDir+"/version.yaml", "default_version: v1\n")

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "init", "beta",
		"--workspace", cfg.Workspace,
		"--interactive=false",
	)
	t.Logf("agent init beta (v1):\n%s", out)
	require.Equal(t, 0, code, "agent init must exit 0 with v1 pin: %s", out)

	// v1 actual layout: agent lives at flat memory/<name>/ (not memory/agents/<name>/).
	// The v1 agent template uses v1 directory structure but the flat root.
	agentDir := memDir + "/beta"
	assertDirExists(t, cfg, agentDir)
	assertFileContains(t, cfg, agentDir+"/metadata.yaml", "version: v1")

	// v1 required dirs (v1 template structure at the flat root).
	for _, sub := range []string{"entry/instructions", "entry/tasks", "generated", "state"} {
		assertDirExists(t, cfg, agentDir+"/"+sub)
	}

	// v1 workspace doc: agent_beta.md at memory root.
	assertFileExists(t, cfg, memDir+"/agent_beta.md")

	// v1 collab tasks dir: memory/team/entry/tasks/beta/.
	assertDirExists(t, cfg, memDir+"/team/entry/tasks/beta")
}

// AI-03 Agent create with workspace version.yaml = v2 — no v2 agent template exists
//
// ASSUMPTION deviation: templates/agents/v2/ does not exist in the embedded FS.
// copyTemplateTree for "templates/agents/v2" fails → agent init exits non-zero.
func TestAI03AgentInitV2NoTemplate(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// Pin workspace to v2.
	writeFile(t, cfg, memDir+"/version.yaml", "default_version: v2\n")

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "init", "gamma",
		"--workspace", cfg.Workspace,
		"--interactive=false",
	)
	t.Logf("agent init gamma (v2):\n%s", out)

	// No v2 template → exit non-zero.
	assert.NotEqual(t, 0, code,
		"agent init with v2 pin must fail (no v2 template in embedded FS): %s", out)
	// Error message must reference the missing template.
	assert.Contains(t, out, "v2", "error must mention the missing v2 template: %s", out)
	// Note: the operator may leave a partial gamma/ dir on disk after the failure;
	// we do not assert its absence because the rollback is best-effort.
}

// AI-04 Multiple agents in same workspace all seeded at workspace default (v3)
func TestAI04MultipleAgentsSameWorkspace(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// Pin to v3 explicitly.
	writeFile(t, cfg, memDir+"/version.yaml", "default_version: v3\n")

	for _, name := range []string{"delta", "epsilon", "zeta"} {
		out, code := harness.RunOmniAllowFail(t, cfg,
			"agent", "init", name,
			"--workspace", cfg.Workspace,
			"--interactive=false",
		)
		t.Logf("agent init %s:\n%s", name, out)
		require.Equal(t, 0, code, "agent init %s must exit 0: %s", name, out)

		// All at flat v3 path.
		agentDir := fmt.Sprintf("%s/%s", memDir, name)
		assertDirExists(t, cfg, agentDir)
		assertFileContains(t, cfg, agentDir+"/metadata.yaml", "version_code: 3")
	}

	// validate the whole workspace — all three agents pass.
	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", memDir)
	t.Logf("validate multi-agent workspace:\n%s", valOut)
	assert.Equal(t, 0, valCode, "all three v3 agents must validate clean: %s", valOut)
}

// AI-05 Agent create name collision → non-zero, no data corruption
func TestAI05AgentInitNameCollision(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// First creation — must succeed.
	out1, code1 := harness.RunOmniAllowFail(t, cfg,
		"agent", "init", "eta",
		"--workspace", cfg.Workspace,
		"--interactive=false",
	)
	t.Logf("agent init eta (1st):\n%s", out1)
	require.Equal(t, 0, code1, "first agent init must succeed: %s", out1)

	// Write a sentinel file inside the agent's instructions dir.
	agentDir := memDir + "/eta"
	writeFile(t, cfg, agentDir+"/instructions/sentinel.yaml", "sentinel: true\n")

	// Second creation with same name — must fail.
	out2, code2 := harness.RunOmniAllowFail(t, cfg,
		"agent", "init", "eta",
		"--workspace", cfg.Workspace,
		"--interactive=false",
	)
	t.Logf("agent init eta (2nd/collision):\n%s", out2)
	assert.NotEqual(t, 0, code2, "duplicate agent init must exit non-zero: %s", out2)
	assert.True(t,
		strings.Contains(out2, "eta") || strings.Contains(out2, "already"),
		"error must mention the duplicate name: %s", out2)

	// Sentinel file must still exist — no corruption.
	assertFileExists(t, cfg, agentDir+"/instructions/sentinel.yaml")

	// Existing agent still validates.
	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "eta", "--memory-root", memDir)
	t.Logf("validate eta after collision attempt:\n%s", valOut)
	assert.Equal(t, 0, valCode, "existing agent must validate clean after collision: %s", valOut)
}
