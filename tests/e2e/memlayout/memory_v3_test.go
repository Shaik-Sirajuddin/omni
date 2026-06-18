//go:build e2e

package memlayout_test

// Memory Layout v3 pipeline integration tests.
//
// These tests drive the omni binary and migrate.sh inside the running container
// against a synthetic temp workspace — they NEVER mutate the live memory tree.
//
// Run from tests/e2e:
//   E2E_CONTAINER=omni-omni_main-ubuntu-1 go test -tags e2e -count=1 ./memory/...

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── constants ─────────────────────────────────────────────────────────────────

// repoRoot is where the repo is baked inside the container image.
const repoRoot = "/build"

// migrateSh is the migrate.sh path inside the container (from the baked repo).
// Tests copy it into the temp workspace so it operates on the right MEMORY_DIR.
const migrateSh = repoRoot + "/memory/tools/migrate.sh"

// memoryContracts are the dirs that migrate.sh and omni memory validate need.
// These are baked into the image at /build/memory/; we copy them into the temp
// memory workspace so the tool operates on an isolated tree.
var memoryContracts = []string{"v1", "v2", "v3", "templates"}

// ── helpers ───────────────────────────────────────────────────────────────────

// memTestConfig creates an isolated memory workspace under the per-test /tmp dir.
// Returns (cfg, memDir) where memDir is the memory root inside the container.
//
// Layout inside the container:
//
//	/tmp/e2e-<name>/          ← cfg.Workspace (omni DB workspace)
//	  memory/                 ← memDir (synthetic memory root)
//	    v1/  v2/  v3/  templates/   (copied from /build/memory/)
//	    agents/               (v1 agents staged here before migration)
//	    tools/migrate.sh      (copied so it derives the correct MEMORY_DIR)
func memTestConfig(t *testing.T) (harness.TestConfig, string) {
	t.Helper()
	cfg := harness.NewConfig(t)
	memDir := cfg.Workspace + "/memory"

	ctx := context.Background()

	// mkdir -p for memDir and tools subdir.
	code, _, err := cfg.Exec.RunCommand(ctx, []string{"mkdir", "-p", memDir + "/tools", memDir + "/agents"})
	require.NoError(t, err, "create memory dir")
	require.Equal(t, 0, code, "mkdir memory dir")

	// Copy contract dirs from baked image into the temp memory workspace.
	for _, sub := range memoryContracts {
		src := repoRoot + "/memory/" + sub
		dst := memDir + "/" + sub
		code, out, err := cfg.Exec.RunCommand(ctx, []string{"cp", "-r", src, dst})
		require.NoError(t, err, "cp %s", sub)
		require.Equal(t, 0, code, "cp %s: %s", sub, string(out))
	}

	// Copy migrate.sh into temp tree so MEMORY_DIR=$(dirname $0)/.. resolves correctly.
	code, out, err := cfg.Exec.RunCommand(ctx, []string{"cp", migrateSh, memDir + "/tools/migrate.sh"})
	require.NoError(t, err, "cp migrate.sh")
	require.Equal(t, 0, code, "cp migrate.sh: %s", string(out))

	// Note: /tmp is noexec in the container; all migrate.sh invocations use
	// `bash /path/migrate.sh` rather than executing the script directly.

	return cfg, memDir
}

// makeV1Agent creates a synthetic v1 agent directory under memDir/agents/<name>/
// matching the v1 contract: entry/instructions/, entry/tasks/, generated/, state/.
func makeV1Agent(t *testing.T, cfg harness.TestConfig, memDir, name string) {
	t.Helper()
	ctx := context.Background()
	agentDir := fmt.Sprintf("%s/agents/%s", memDir, name)

	dirs := []string{
		agentDir + "/entry/instructions",
		agentDir + "/entry/tasks",
		agentDir + "/generated",
		agentDir + "/state",
	}
	for _, d := range dirs {
		code, out, err := cfg.Exec.RunCommand(ctx, []string{"mkdir", "-p", d})
		require.NoError(t, err, "mkdir %s", d)
		require.Equal(t, 0, code, "mkdir %s: %s", d, string(out))
	}

	// Instruction file
	writeFile(t, cfg, agentDir+"/entry/instructions/x.yaml",
		"instruction: hello\ndetail: world\n")
	// Task file
	writeFile(t, cfg, agentDir+"/entry/tasks/y.yaml",
		"task: do-something\nstatus: pending\n")
	// Generated plan doc (will move to gen/plans/)
	writeFile(t, cfg, agentDir+"/generated/z.md",
		"# Plan doc for "+name+"\n")
	// State doc (will move to gen/state/)
	writeFile(t, cfg, agentDir+"/state/s.md",
		"# State for "+name+"\n")
	// metadata.yaml with version_code 1
	writeFile(t, cfg, agentDir+"/metadata.yaml",
		"version_code: 1\nversion: v1\n")
}

// writeFile writes content to path inside the container via sh -c.
func writeFile(t *testing.T, cfg harness.TestConfig, path, content string) {
	t.Helper()
	// Use printf to avoid echo interpretation of escape sequences.
	// Escape single-quotes in content so the printf argument is safe.
	escaped := strings.ReplaceAll(content, "'", "'\\''")
	cmd := fmt.Sprintf("printf '%%s' '%s' > '%s'", escaped, path)
	out, code := harness.ExecInContainer(t, cfg, cmd)
	require.Equal(t, 0, code, "writeFile %s: %s", path, out)
}

// assertFileExists checks that a path exists inside the container.
func assertFileExists(t *testing.T, cfg harness.TestConfig, path string) {
	t.Helper()
	_, code := harness.ExecInContainer(t, cfg, fmt.Sprintf("test -f '%s'", path))
	assert.Equal(t, 0, code, "expected file to exist: %s", path)
}

// assertDirExists checks that a directory exists inside the container.
func assertDirExists(t *testing.T, cfg harness.TestConfig, path string) {
	t.Helper()
	_, code := harness.ExecInContainer(t, cfg, fmt.Sprintf("test -d '%s'", path))
	assert.Equal(t, 0, code, "expected directory to exist: %s", path)
}

// assertNotExists checks that a path does NOT exist inside the container.
func assertNotExists(t *testing.T, cfg harness.TestConfig, path string) {
	t.Helper()
	_, code := harness.ExecInContainer(t, cfg, fmt.Sprintf("test -e '%s'", path))
	assert.NotEqual(t, 0, code, "expected path to NOT exist: %s", path)
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestMigrateV1ToV3Layout verifies that migrate.sh --agent <name> correctly
// transforms a synthetic v1 agent directory into the v3 layout:
//   - entry/instructions/*.yaml   → instructions/*.yaml  (flattened)
//   - entry/tasks/*.yaml          → tasks/*.yaml          (flattened)
//   - generated/*.md              → gen/plans/*.md
//   - state/*.md                  → gen/state/*.md
//   - memory.md present in every required circuit dir
//   - metadata.yaml version_code = 3
//   - entry/ and generated/ dirs removed
//   - no double-nesting (instructions/instructions, tasks/tasks)
func TestMigrateV1ToV3Layout(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV1Agent(t, cfg, memDir, "alpha-v1")

	// Run migrate.sh --agent alpha-v1.
	// The script derives MEMORY_DIR from its own path: $(dirname $0)/..
	// We placed it at memDir/tools/migrate.sh so MEMORY_DIR = memDir.
	// /tmp is mounted noexec in the container, so invoke via `bash` explicitly.
	migrateOut, migrateCode := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --agent alpha-v1", memDir))
	t.Logf("migrate.sh output:\n%s", migrateOut)
	require.Equal(t, 0, migrateCode, "migrate.sh --agent alpha-v1 must exit 0: %s", migrateOut)

	// After v2→v3, the agent is relocated from agents/alpha-v1 to alpha-v1 (flat).
	agentDir := memDir + "/alpha-v1"

	// Required v3 directories.
	for _, subdir := range []string{
		"instructions",
		"skills",
		"knowledge",
		"knowledge/com",
		"gen",
		"gen/plans",
		"gen/state",
	} {
		assertDirExists(t, cfg, agentDir+"/"+subdir)
	}

	// Circuit rule: memory.md must exist in every required dir.
	for _, dir := range []string{
		agentDir,
		agentDir + "/instructions",
		agentDir + "/skills",
		agentDir + "/knowledge",
		agentDir + "/knowledge/com",
		agentDir + "/gen",
	} {
		assertFileExists(t, cfg, dir+"/memory.md")
	}

	// Source files must be preserved at their v3 locations.
	assertFileExists(t, cfg, agentDir+"/instructions/x.yaml")   // entry/instructions/x.yaml flattened
	assertFileExists(t, cfg, agentDir+"/gen/plans/z.md")        // generated/z.md → gen/plans/z.md
	assertFileExists(t, cfg, agentDir+"/gen/state/s.md")        // state/s.md → gen/state/s.md
	assertFileExists(t, cfg, agentDir+"/tasks/y.yaml")          // entry/tasks/y.yaml flattened

	// metadata.yaml must reflect version_code 3.
	metaOut, _ := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("grep 'version_code' '%s/metadata.yaml'", agentDir))
	assert.Contains(t, metaOut, "3", "metadata.yaml version_code must be 3, got: %s", metaOut)

	// entry/ and generated/ must be gone.
	assertNotExists(t, cfg, agentDir+"/entry")
	assertNotExists(t, cfg, agentDir+"/generated")

	// No double-nesting.
	assertNotExists(t, cfg, agentDir+"/instructions/instructions")
	assertNotExists(t, cfg, agentDir+"/tasks/tasks")
}

// TestValidateAfterMigration runs `omni memory validate --agent alpha-v1
// --memory-root <memDir>` after migration and asserts exit 0 (no error violations).
func TestValidateAfterMigration(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV1Agent(t, cfg, memDir, "alpha-v1")

	// Migrate first.
	// /tmp is mounted noexec in the container; invoke via `bash` explicitly.
	migrateOut, migrateCode := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --agent alpha-v1", memDir))
	t.Logf("migrate.sh output:\n%s", migrateOut)
	require.Equal(t, 0, migrateCode, "migrate.sh must succeed before validate: %s", migrateOut)

	// Validate.
	validateOut, validateCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate",
		"--agent", "alpha-v1",
		"--memory-root", memDir,
	)
	t.Logf("memory validate output:\n%s", validateOut)
	assert.Equal(t, 0, validateCode,
		"'omni memory validate --agent alpha-v1' must exit 0 after migration:\n%s", validateOut)
}

// TestSysInstructsV3Path verifies `omni agent get <name> sys-instructs` returns
// a path ending in /<name>/instructions/memory.md for a v3 migrated agent.
func TestSysInstructsV3Path(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV1Agent(t, cfg, memDir, "alpha-v1")

	// Migrate.
	// /tmp is mounted noexec in the container; invoke via `bash` explicitly.
	migrateOut, migrateCode := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --agent alpha-v1", memDir))
	t.Logf("migrate.sh output:\n%s", migrateOut)
	require.Equal(t, 0, migrateCode, "migrate.sh must succeed before sys-instructs: %s", migrateOut)

	// sys-instructs uses the workspace's memory dir. The omni binary discovers
	// the memory root from the workspace; we set OMNI_WORKSPACE to our temp dir
	// which already has the memory/ subdir with the migrated agent.
	// Use --workspace explicitly for reliability.
	sysOut, sysCode := harness.RunOmniAllowFail(t, cfg,
		"agent", "get", "alpha-v1", "sys-instructs",
		"--workspace", cfg.Workspace,
	)
	sysPath := strings.TrimSpace(sysOut)
	t.Logf("sys-instructs output: %q (exit=%d)", sysPath, sysCode)

	require.Equal(t, 0, sysCode,
		"'omni agent get alpha-v1 sys-instructs' must exit 0: %s", sysOut)
	assert.True(t,
		strings.HasSuffix(sysPath, "/alpha-v1/instructions/memory.md"),
		"sys-instructs must return path ending in /alpha-v1/instructions/memory.md, got: %q", sysPath)
}

// TestSysInstructsV1Coexistence verifies that a v1 agent NOT migrated still
// returns the v1 entry/instructions path from sys-instructs, confirming that
// v1 and v3 agents can coexist in the same workspace.
func TestSysInstructsV1Coexistence(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// Create two agents: alpha-v1 (will be migrated) and beta-v1 (stays v1).
	makeV1Agent(t, cfg, memDir, "alpha-v1")
	makeV1Agent(t, cfg, memDir, "beta-v1")

	// Migrate only alpha-v1.
	// /tmp is mounted noexec in the container; invoke via `bash` explicitly.
	migrateOut, migrateCode := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --agent alpha-v1", memDir))
	t.Logf("migrate.sh (alpha-v1) output:\n%s", migrateOut)
	require.Equal(t, 0, migrateCode, "migrate.sh --agent alpha-v1 must succeed: %s", migrateOut)

	// alpha-v1: must return v3 path.
	alphaOut, alphaCode := harness.RunOmniAllowFail(t, cfg,
		"agent", "get", "alpha-v1", "sys-instructs",
		"--workspace", cfg.Workspace,
	)
	alphaPath := strings.TrimSpace(alphaOut)
	t.Logf("alpha-v1 sys-instructs: %q (exit=%d)", alphaPath, alphaCode)

	require.Equal(t, 0, alphaCode,
		"alpha-v1 sys-instructs must exit 0: %s", alphaOut)
	assert.True(t,
		strings.HasSuffix(alphaPath, "/alpha-v1/instructions/memory.md"),
		"alpha-v1 (migrated) must return v3 path, got: %q", alphaPath)

	// beta-v1: must return v1 entry/instructions path (not migrated).
	betaOut, betaCode := harness.RunOmniAllowFail(t, cfg,
		"agent", "get", "beta-v1", "sys-instructs",
		"--workspace", cfg.Workspace,
	)
	betaPath := strings.TrimSpace(betaOut)
	t.Logf("beta-v1 sys-instructs: %q (exit=%d)", betaPath, betaCode)

	require.Equal(t, 0, betaCode,
		"beta-v1 sys-instructs must exit 0: %s", betaOut)
	assert.True(t,
		strings.HasSuffix(betaPath, "/agents/beta-v1/entry/instructions"),
		"beta-v1 (not migrated, v1) must return entry/instructions path, got: %q", betaPath)
}
