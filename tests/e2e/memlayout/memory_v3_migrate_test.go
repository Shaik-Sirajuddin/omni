//go:build e2e

package memlayout_test

// MIG — Migration scenarios (8 tests)
//
// ASSUMPTION resolutions:
//   - migrate.sh --agent <name> runs via `bash '<memDir>/tools/migrate.sh' --agent <name>`.
//   - /tmp is noexec in container → always invoke via `bash`.
//   - get_agent_version: reads from .memory.lock if present; else reads metadata.yaml from
//     either memory/agents/<name>/ or memory/<name>/.
//   - write_lock uses `mv "$tmp" "$LOCK_FILE"` (atomic rename) — safe for concurrency.
//   - MIG-08: team/tasks/* moved to collab/tasks/*; team/ dir is NOT removed by the script
//     (only tasks are moved; the team/ dir itself remains but is empty or nearly so).
//   - MIG-03 idempotent: log emits "already at v<x> — nothing to do" (from migrate_agent).
//   - MIG-07 v1 data/: v1_to_v2 moves entry/data/ → data/, then v2_to_v3 moves data/ → knowledge/data/.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MIG-01 v1 → v3 full migration via migrate.sh
func TestMIG01V1ToV3Migration(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV1Agent(t, cfg, memDir, "alpha-mig")

	migrateOut, migrateCode := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --agent alpha-mig", memDir))
	t.Logf("migrate alpha-mig output:\n%s", migrateOut)
	require.Equal(t, 0, migrateCode, "migrate v1→v3 must exit 0: %s", migrateOut)

	// Agent relocated to flat path.
	agentDir := memDir + "/alpha-mig"
	assertDirExists(t, cfg, agentDir)

	// v3 required dirs present.
	for _, sub := range []string{"instructions", "skills", "knowledge", "knowledge/com",
		"gen", "gen/plans", "gen/state"} {
		assertDirExists(t, cfg, agentDir+"/"+sub)
	}

	// Files moved correctly.
	assertFileExists(t, cfg, agentDir+"/instructions/x.yaml")   // entry/instructions/x.yaml
	assertFileExists(t, cfg, agentDir+"/gen/plans/z.md")        // generated/z.md → gen/plans/
	assertFileExists(t, cfg, agentDir+"/gen/state/s.md")        // state/s.md → gen/state/
	assertFileExists(t, cfg, agentDir+"/tasks/y.yaml")          // entry/tasks/y.yaml flattened

	// Circuit rule: memory.md in every required dir.
	for _, dir := range []string{"", "/instructions", "/skills", "/knowledge",
		"/knowledge/com", "/gen"} {
		assertFileExists(t, cfg, agentDir+dir+"/memory.md")
	}

	// metadata.yaml version_code 3.
	assertFileContains(t, cfg, agentDir+"/metadata.yaml", "version_code: 3")

	// entry/ and generated/ removed.
	assertNotExists(t, cfg, agentDir+"/entry")
	assertNotExists(t, cfg, agentDir+"/generated")

	// .memory.lock written.
	assertFileExists(t, cfg, memDir+"/.memory.lock")
	lockOut, _ := harness.ExecInContainer(t, cfg, fmt.Sprintf("cat '%s/.memory.lock'", memDir))
	assert.Contains(t, lockOut, "alpha-mig", ".memory.lock must mention alpha-mig: %s", lockOut)
	assert.Contains(t, lockOut, "3", ".memory.lock must record version 3: %s", lockOut)

	// Post-validate.
	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "alpha-mig", "--memory-root", memDir)
	t.Logf("validate alpha-mig after migrate:\n%s", valOut)
	assert.Equal(t, 0, valCode, "migrated agent must validate clean: %s", valOut)
}

// MIG-02 v2 → v3 full migration via migrate.sh
func TestMIG02V2ToV3Migration(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV2Agent(t, cfg, memDir, "beta-mig")

	migrateOut, migrateCode := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --agent beta-mig", memDir))
	t.Logf("migrate beta-mig output:\n%s", migrateOut)
	require.Equal(t, 0, migrateCode, "migrate v2→v3 must exit 0: %s", migrateOut)

	// Agent relocated to flat path.
	agentDir := memDir + "/beta-mig"
	assertDirExists(t, cfg, agentDir)

	// Files moved correctly.
	assertFileExists(t, cfg, agentDir+"/gen/plans/plan.md")       // gen/plan.md → gen/plans/
	assertFileExists(t, cfg, agentDir+"/gen/state/progress.md")   // state/progress.md → gen/state/
	assertFileExists(t, cfg, agentDir+"/instructions/instr.yaml") // instructions preserved
	assertFileExists(t, cfg, agentDir+"/tasks/task.yaml")         // tasks preserved

	// state/ drained (moved to gen/state/).
	assertNotExists(t, cfg, agentDir+"/state")

	// v3 dirs added.
	assertDirExists(t, cfg, agentDir+"/skills")
	assertDirExists(t, cfg, agentDir+"/knowledge/com")

	// memory.md in required dirs.
	for _, dir := range []string{"", "/instructions", "/skills", "/knowledge",
		"/knowledge/com", "/gen", "/gen/plans", "/gen/state"} {
		assertFileExists(t, cfg, agentDir+dir+"/memory.md")
	}

	assertFileContains(t, cfg, agentDir+"/metadata.yaml", "version_code: 3")
	assertFileExists(t, cfg, memDir+"/.memory.lock")

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "beta-mig", "--memory-root", memDir)
	t.Logf("validate beta-mig after migrate:\n%s", valOut)
	assert.Equal(t, 0, valCode, "migrated v2 agent must validate clean: %s", valOut)
}

// MIG-03 Idempotent re-run on already-v3 agent
func TestMIG03IdempotentReRun(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV1Agent(t, cfg, memDir, "alpha-mig")

	// First migration.
	out1, code1 := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --agent alpha-mig", memDir))
	require.Equal(t, 0, code1, "first migrate must exit 0: %s", out1)

	// Second run on already-v3 agent.
	out2, code2 := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --agent alpha-mig", memDir))
	t.Logf("2nd migrate output:\n%s", out2)
	assert.Equal(t, 0, code2, "idempotent re-run must exit 0: %s", out2)
	// Script logs "already at v<x> — nothing to do".
	assert.True(t,
		strings.Contains(out2, "nothing to do") || strings.Contains(out2, "already"),
		"re-run must log 'nothing to do': %s", out2)

	// Still validates.
	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "alpha-mig", "--memory-root", memDir)
	assert.Equal(t, 0, valCode, "validate must still pass after idempotent re-run: %s", valOut)
}

// MIG-04 Dry-run produces zero filesystem changes
func TestMIG04DryRunNoChanges(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV1Agent(t, cfg, memDir, "gamma-mig")

	// Snapshot: flat gamma-mig/ must NOT exist before.
	assertNotExists(t, cfg, memDir+"/gamma-mig")

	dryOut, dryCode := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --agent gamma-mig --dry-run", memDir))
	t.Logf("dry-run output:\n%s", dryOut)
	require.Equal(t, 0, dryCode, "dry-run must exit 0: %s", dryOut)

	// dry-run log present.
	assert.Contains(t, dryOut, "dry-run", "dry-run output must contain dry-run markers: %s", dryOut)

	// No actual move: agent still at agents/gamma-mig/.
	assertDirExists(t, cfg, memDir+"/agents/gamma-mig")
	// entry/ still present.
	assertDirExists(t, cfg, memDir+"/agents/gamma-mig/entry")
	// Flat dir NOT created.
	assertNotExists(t, cfg, memDir+"/gamma-mig")
	// .memory.lock NOT written.
	assertNotExists(t, cfg, memDir+"/.memory.lock")
}

// MIG-05 Single-agent migration leaves other agents untouched
func TestMIG05SingleAgentMigrationIsolation(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV1Agent(t, cfg, memDir, "delta-mig")   // will be migrated
	makeV1Agent(t, cfg, memDir, "epsilon-mig") // must stay untouched

	// Sentinel in epsilon-mig to detect corruption.
	writeFile(t, cfg, memDir+"/agents/epsilon-mig/entry/instructions/sentinel.yaml",
		"sentinel: true\n")

	migrateOut, migrateCode := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --agent delta-mig", memDir))
	t.Logf("migrate delta-mig:\n%s", migrateOut)
	require.Equal(t, 0, migrateCode, "migrate delta-mig must exit 0: %s", migrateOut)

	// delta-mig migrated to flat.
	assertDirExists(t, cfg, memDir+"/delta-mig")

	// epsilon-mig still at agents/.
	assertDirExists(t, cfg, memDir+"/agents/epsilon-mig")
	assertDirExists(t, cfg, memDir+"/agents/epsilon-mig/entry")
	assertFileExists(t, cfg, memDir+"/agents/epsilon-mig/entry/instructions/sentinel.yaml")

	// .memory.lock written; epsilon-mig at version 1.
	assertFileExists(t, cfg, memDir+"/.memory.lock")
	lockOut, _ := harness.ExecInContainer(t, cfg, fmt.Sprintf("cat '%s/.memory.lock'", memDir))
	assert.Contains(t, lockOut, "delta-mig", "lock must contain delta-mig: %s", lockOut)
	// epsilon-mig should appear in lock at version 1 (get_agent_version from metadata.yaml).
	assert.Contains(t, lockOut, "epsilon-mig", "lock must contain epsilon-mig: %s", lockOut)
}

// MIG-06 .memory.lock updated correctly after migration
func TestMIG06LockFileAfterMigration(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV1Agent(t, cfg, memDir, "zeta-mig")
	makeV1Agent(t, cfg, memDir, "eta-mig")

	migrateOut, migrateCode := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --agent zeta-mig", memDir))
	t.Logf("migrate zeta-mig:\n%s", migrateOut)
	require.Equal(t, 0, migrateCode, "migrate zeta-mig must exit 0: %s", migrateOut)

	lockOut, lockCode := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("cat '%s/.memory.lock'", memDir))
	require.Equal(t, 0, lockCode, "cat .memory.lock: %s", lockOut)
	t.Logf(".memory.lock:\n%s", lockOut)

	assert.Contains(t, lockOut, "layout_version: 3", "lock must have layout_version: 3: %s", lockOut)
	assert.Contains(t, lockOut, "lock_version: 1", "lock must have lock_version: 1: %s", lockOut)
	assert.Contains(t, lockOut, "migrated_at:", "lock must have migrated_at timestamp: %s", lockOut)
	assert.Contains(t, lockOut, "zeta-mig", "lock must mention zeta-mig: %s", lockOut)
	assert.Contains(t, lockOut, "eta-mig", "lock must mention eta-mig: %s", lockOut)
	// team_version is written when full migration runs; single-agent run still writes it.
	assert.Contains(t, lockOut, "team_version:", "lock must have team_version: %s", lockOut)
}

// MIG-07 data/ in v1 agent folded into knowledge/data/ after migration
func TestMIG07DataFoldedIntoKnowledgeData(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// Create v1 agent with data content in entry/data/.
	makeV1Agent(t, cfg, memDir, "theta-mig")
	// Add entry/data/ref.yaml — v1_to_v2 will move entry/data/ → data/
	// then v2_to_v3 will move data/ → knowledge/data/.
	out, code := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("mkdir -p '%s/agents/theta-mig/entry/data'", memDir))
	require.Equal(t, 0, code, "mkdir entry/data: %s", out)
	writeFile(t, cfg, memDir+"/agents/theta-mig/entry/data/ref.yaml", "ref: some-data\n")

	migrateOut, migrateCode := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --agent theta-mig", memDir))
	t.Logf("migrate theta-mig:\n%s", migrateOut)
	require.Equal(t, 0, migrateCode, "migrate theta-mig must exit 0: %s", migrateOut)

	agentDir := memDir + "/theta-mig"
	// ref.yaml moved to knowledge/data/.
	assertFileExists(t, cfg, agentDir+"/knowledge/data/ref.yaml")
	// knowledge/data/memory.md seeded.
	assertFileExists(t, cfg, agentDir+"/knowledge/data/memory.md")
	// Original data/ at agent root gone.
	assertNotExists(t, cfg, agentDir+"/data")

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "theta-mig", "--memory-root", memDir)
	t.Logf("validate theta-mig:\n%s", valOut)
	assert.Equal(t, 0, valCode, "theta-mig must validate clean after data fold: %s", valOut)
}

// MIG-08 team/ → collab/ migration (workspace-level, full run without --agent)
//
// ASSUMPTION deviation: team/ dir is NOT removed by migrate_team_to_collab; only
// team/tasks/* is moved to collab/tasks/*. The team/ parent remains (possibly empty).
func TestMIG08TeamToCollabMigration(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// Create team/tasks/ with content to be moved.
	out, code := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("mkdir -p '%s/team/tasks/iota-agent'", memDir))
	require.Equal(t, 0, code, "mkdir team/tasks: %s", out)
	writeFile(t, cfg, memDir+"/team/tasks/iota-agent/handoff.yaml",
		"task: handoff\nagent: iota-agent\n")

	// At least one agent to trigger write_lock.
	makeV1Agent(t, cfg, memDir, "iota-v1")

	// Full migration (no --agent flag).
	migrateOut, migrateCode := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh'", memDir))
	t.Logf("full migrate output:\n%s", migrateOut)
	require.Equal(t, 0, migrateCode, "full migration must exit 0: %s", migrateOut)

	// collab/ created.
	assertDirExists(t, cfg, memDir+"/collab")
	assertFileExists(t, cfg, memDir+"/collab/memory.md")

	// team/tasks/iota-agent/ moved to collab/tasks/iota-agent/.
	assertFileExists(t, cfg, memDir+"/collab/tasks/iota-agent/handoff.yaml")

	// .memory.lock has team_version: 3.
	lockOut, _ := harness.ExecInContainer(t, cfg, fmt.Sprintf("cat '%s/.memory.lock'", memDir))
	assert.Contains(t, lockOut, "team_version: 3", ".memory.lock must have team_version: 3: %s", lockOut)
}
