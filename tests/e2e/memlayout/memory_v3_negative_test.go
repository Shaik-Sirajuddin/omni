//go:build e2e

package memlayout_test

// NEG — Negative/Error scenarios (6 tests)
//
// ASSUMPTION resolutions:
//   - NEG-01: validate on empty dir → "no version contracts found" because
//     resolveMemoryRoot will find no v<x>/ subdirs and exit non-zero.
//   - NEG-02: version.yaml = v99; memory.Create calls layoutForVersion(99) → fallback v3 layout
//     (creates dirs) then copyTemplateTree("templates/agents/v99", ...) → FAILS because
//     the embedded FS has no v99 template. So agent init exits non-zero.
//   - NEG-03: metadata.yaml version_code: 99 → validateSingleAgent: available[99] is nil
//     (no v99 contract dir) → returns error "version v99 contract not found (available: v1, v2, v3)".
//   - NEG-04: --agent does-not-exist → resolveAgentByName returns false → error with both paths.
//   - NEG-05: no metadata.yaml → inferVersion from structure → v3 inferred (skills/ present) → exit 0.
//   - NEG-06: migrate.sh --unknown-flag → exit 1, stderr: "Unknown flag: --unknown-flag".

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NEG-01 Validate on empty dir (no v<x>/ contracts) → hard error, non-zero exit
func TestNEG01ValidateEmptyDir(t *testing.T) {
	t.Parallel()
	cfg, _ := memTestConfig(t)

	// Create a completely empty memory dir (no v1/v2/v3 contract dirs).
	emptyDir := cfg.Workspace + "/empty-mem"
	out, code := harness.ExecInContainer(t, cfg, fmt.Sprintf("mkdir -p '%s'", emptyDir))
	require.Equal(t, 0, code, "mkdir empty-mem: %s", out)

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", emptyDir)
	t.Logf("validate empty dir:\n%s", valOut)

	// Expect non-zero: no version contracts found.
	assert.NotEqual(t, 0, valCode, "validate on empty dir must exit non-zero: %s", valOut)
	// Error message must be non-empty (no silent crash).
	assert.NotEmpty(t, strings.TrimSpace(valOut), "error output must be non-empty")
}

// NEG-02 Bogus version (v99) in version.yaml — agent init exits non-zero
//
// layoutForVersion(99) falls back to v3 layout (creates dirs), but then
// copyTemplateTree("templates/agents/v99", ...) fails because v99 doesn't exist.
// REAL PRODUCT BEHAVIOR: exit non-zero with embedded FS error.
func TestNEG02BogusVersionInVersionYaml(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// Write version.yaml with bogus v99.
	writeFile(t, cfg, memDir+"/version.yaml", "default_version: v99\n")

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "init", "test-neg2",
		"--workspace", cfg.Workspace,
		"--interactive=false",
	)
	t.Logf("agent init with v99 (exit=%d):\n%s", code, out)

	// No v99 template → non-zero exit, non-empty error output, no panic.
	assert.NotEqual(t, 0, code,
		"agent init with v99 version must fail (no v99 template in embedded FS): %s", out)
	assert.NotEmpty(t, strings.TrimSpace(out), "error output must be non-empty")
}

// NEG-03 Validate with version_code: 99 in metadata.yaml → hard error
func TestNEG03UnknownVersionCodeInMetadata(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "neg-three")

	// Overwrite metadata.yaml with v99.
	writeFile(t, cfg, memDir+"/neg-three/metadata.yaml",
		"version_code: 99\nversion: v99\n")

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "neg-three", "--memory-root", memDir)
	t.Logf("validate neg-three (v99 metadata):\n%s", valOut)

	assert.NotEqual(t, 0, valCode, "v99 version_code must cause non-zero exit: %s", valOut)
	// Error message references the unknown version and available versions.
	assert.True(t,
		strings.Contains(valOut, "99") || strings.Contains(valOut, "contract not found"),
		"error must reference unknown version 99: %s", valOut)
}

// NEG-04 --agent for non-existent agent → clean non-zero, no panic
func TestNEG04NonExistentAgent(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "does-not-exist", "--memory-root", memDir)
	t.Logf("validate non-existent agent:\n%s", valOut)

	assert.NotEqual(t, 0, valCode, "non-existent agent must cause non-zero exit: %s", valOut)
	// Non-empty error output (no silent crash / no panic).
	assert.NotEmpty(t, strings.TrimSpace(valOut), "error output must be non-empty")
	// Error message should reference the agent name.
	assert.Contains(t, valOut, "does-not-exist",
		"error must mention the agent name: %s", valOut)
}

// NEG-05 Agent with no metadata.yaml → structural inference + exit 1 (metadata.yaml required)
//
// ASSUMPTION deviation: missing metadata.yaml produces an inference warning but also a
// v3 contract error (metadata.yaml is a required file). Actual exit code is 1.
// Same underlying behavior as VAL-11, explicitly categorized as a negative test.
func TestNEG05NoMetadataYamlGracefulWarning(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "neg-five")

	// Remove metadata.yaml.
	out, code := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("rm '%s/neg-five/metadata.yaml'", memDir))
	require.Equal(t, 0, code, "rm metadata.yaml: %s", out)

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "neg-five", "--memory-root", memDir)
	t.Logf("validate neg-five (no metadata.yaml):\n%s", valOut)

	// Actual behavior: inferred v3 from structure (warning) but metadata.yaml is required
	// by v3 contract → exit 1 (error). No silent crash.
	assert.NotEqual(t, 0, valCode, "missing metadata.yaml exits 1 (v3 contract error): %s", valOut)
	assert.NotEmpty(t, strings.TrimSpace(valOut), "must emit output (no silent crash)")
	// Warning about structural inference must be present.
	assert.Contains(t, valOut, "inferred",
		"output must mention structural inference warning: %s", valOut)
	// Error must reference the missing file.
	assert.Contains(t, valOut, "metadata.yaml",
		"error must mention the missing metadata.yaml: %s", valOut)
}

// NEG-06 migrate.sh unknown flag → exit 1, stderr contains "Unknown flag"
func TestNEG06MigrateUnknownFlag(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	// Use 2>&1 to capture stderr into the output buffer.
	out, code := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("bash '%s/tools/migrate.sh' --unknown-flag 2>&1", memDir))
	t.Logf("migrate --unknown-flag (exit=%d):\n%s", code, out)

	assert.Equal(t, 1, code, "migrate.sh --unknown-flag must exit 1: %s", out)
	assert.Contains(t, out, "Unknown flag",
		"stderr must contain 'Unknown flag': %s", out)
	assert.Contains(t, out, "--unknown-flag",
		"error must name the unknown flag: %s", out)
}
