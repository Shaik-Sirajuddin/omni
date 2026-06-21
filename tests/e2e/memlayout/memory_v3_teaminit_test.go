//go:build e2e

package memlayout_test

// TI — Team Init scenarios (4 tests)
//
// ASSUMPTION resolutions:
//   - CLI: `omni team init` (Use: "init" under `team` group). No --workspace flag;
//     uses os.Getwd() which is set to cfg.Workspace by the harness's WithWorkDir.
//   - Only v1 and v3 agent/memory templates exist in the embedded FS (no v2 template).
//     TI-03 (pin to v2) therefore fails with a non-zero exit; test asserts actual behavior.
//   - seedMemoryRoot writes metadata.yaml + copies memory workspace template.
//     writeVersionConfig writes version.yaml pinning the resolved version.
//   - copyTemplateTree always overwrites template files; non-template user files untouched.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TI-01 Default team init seeds v3 and writes version.yaml
func TestTI01TeamInitDefaultV3(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// team init uses os.Getwd() = cfg.Workspace (set by harness WithWorkDir).
	out, code := harness.RunOmniAllowFail(t, cfg, "team", "init")
	t.Logf("team init output:\n%s", out)
	require.Equal(t, 0, code, "team init must exit 0: %s", out)

	// version.yaml must pin v3.
	versionOut, _ := harness.ExecInContainer(t, cfg, fmt.Sprintf("cat '%s/version.yaml'", memDir))
	assert.Contains(t, versionOut, "default_version: v3",
		"version.yaml must contain 'default_version: v3', got: %s", versionOut)

	// metadata.yaml must record version_code: 3.
	metaOut, _ := harness.ExecInContainer(t, cfg, fmt.Sprintf("cat '%s/metadata.yaml'", memDir))
	assert.Contains(t, metaOut, "version_code: 3", "metadata.yaml must have version_code: 3, got: %s", metaOut)
	assert.Contains(t, metaOut, "version: v3", "metadata.yaml must have version: v3, got: %s", metaOut)

	// v3 workspace: a guide agent is seeded at memory/guide/ (ensureWorkspaceAndGuideAgent).
	assertDirExists(t, cfg, memDir+"/guide")
	assertFileExists(t, cfg, memDir+"/guide/instructions/memory.md")
	assertFileContains(t, cfg, memDir+"/guide/metadata.yaml", "version_code: 3")

	// v3 collab tasks dir for guide is created (not a memory.md — no workspace template collab/).
	assertDirExists(t, cfg, memDir+"/collab/tasks/guide")

	// v3 workspace: team/ must NOT exist.
	assertNotExists(t, cfg, memDir+"/team")

	// Post-validate: pass memDir as positional arg (tree-level validation).
	// team init also creates a guide agent; validate checks the whole tree.
	valOut, valCode := harness.RunOmniAllowFail(t, cfg, "memory", "validate", memDir)
	t.Logf("validate output:\n%s", valOut)
	assert.Equal(t, 0, valCode, "validate must exit 0 after v3 team init: %s", valOut)
}

// TI-02 Team init with version.yaml pinned to v1 seeds v1 workspace
func TestTI02TeamInitPinnedV1(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// Pre-write version.yaml pinning v1.
	writeFile(t, cfg, memDir+"/version.yaml", "default_version: v1\n")

	out, code := harness.RunOmniAllowFail(t, cfg, "team", "init")
	t.Logf("team init (v1) output:\n%s", out)
	require.Equal(t, 0, code, "team init must exit 0 with v1 pin: %s", out)

	// metadata.yaml must record version_code: 1.
	metaOut, _ := harness.ExecInContainer(t, cfg, fmt.Sprintf("cat '%s/metadata.yaml'", memDir))
	assert.Contains(t, metaOut, "version_code: 1", "metadata.yaml must have version_code: 1, got: %s", metaOut)

	// version.yaml preserved at v1.
	versionOut, _ := harness.ExecInContainer(t, cfg, fmt.Sprintf("cat '%s/version.yaml'", memDir))
	assert.Contains(t, versionOut, "default_version: v1",
		"version.yaml must preserve v1 pin, got: %s", versionOut)
}

// TI-03 Team init with version.yaml pinned to v2 — no v2 memory template exists
//
// ASSUMPTION deviation: there is no v2 workspace template (templates/memory/v1/v2/ absent).
// seedMemoryRoot for v2 calls copyTemplateTree("templates/memory/v1/v2", ...) which
// fails because that embedded path does not exist. So team init exits non-zero.
// Test asserts the actual behavior rather than the plan's optimistic expectation.
func TestTI03TeamInitPinnedV2NoTemplate(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// Pre-write version.yaml pinning v2.
	writeFile(t, cfg, memDir+"/version.yaml", "default_version: v2\n")

	out, code := harness.RunOmniAllowFail(t, cfg, "team", "init")
	t.Logf("team init (v2) output:\n%s", out)

	// No v2 memory template → non-zero exit (embedded FS walk fails for unknown path).
	// This is the actual behavior; the plan assumed a v2 template exists.
	assert.NotEqual(t, 0, code,
		"team init with v2 pin must fail: no v2 memory template in embedded FS (output: %s)", out)
	// No panic: output must be non-empty with an error message.
	assert.NotEmpty(t, strings.TrimSpace(out), "must emit error message on failure")
}

// TI-04 Team init idempotent re-run (existing v3 workspace)
func TestTI04TeamInitIdempotent(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// First run — seeds v3.
	out1, code1 := harness.RunOmniAllowFail(t, cfg, "team", "init")
	t.Logf("team init (1st) output:\n%s", out1)
	require.Equal(t, 0, code1, "first team init must exit 0: %s", out1)

	// Write a sentinel user file that should NOT be overwritten (not a template file).
	writeFile(t, cfg, memDir+"/user_sentinel.md", "# sentinel\n")

	// Second run — must be non-destructive.
	out2, code2 := harness.RunOmniAllowFail(t, cfg, "team", "init")
	t.Logf("team init (2nd) output:\n%s", out2)
	assert.Equal(t, 0, code2, "second team init must exit 0: %s", out2)

	// version.yaml still pins v3.
	versionOut, _ := harness.ExecInContainer(t, cfg, fmt.Sprintf("cat '%s/version.yaml'", memDir))
	assert.Contains(t, versionOut, "default_version: v3",
		"version.yaml must still be v3 after re-init: %s", versionOut)

	// metadata.yaml still version_code: 3.
	metaOut, _ := harness.ExecInContainer(t, cfg, fmt.Sprintf("cat '%s/metadata.yaml'", memDir))
	assert.Contains(t, metaOut, "version_code: 3",
		"metadata.yaml must still be v3 after re-init: %s", metaOut)

	// Sentinel user file untouched (not in template → not overwritten).
	assertFileExists(t, cfg, memDir+"/user_sentinel.md")

	// validate still passes — pass memDir as positional arg for tree-level validation.
	valOut, valCode := harness.RunOmniAllowFail(t, cfg, "memory", "validate", memDir)
	t.Logf("validate after re-init:\n%s", valOut)
	assert.Equal(t, 0, valCode, "validate must pass after idempotent re-init: %s", valOut)
}
