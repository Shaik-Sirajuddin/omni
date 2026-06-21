//go:build e2e

package memlayout_test

// VAL — Validation scenarios (14 tests)
//
// ASSUMPTION resolutions:
//   - `omni memory validate --agent <name> --memory-root <dir> [--version v3]`  ✓
//   - `omni memory lint [<path>]` — takes a positional path arg, NOT --memory-root.
//     VAL-13/14 pass the memDir as a positional argument.
//   - agentContentDirs = {instructions, tasks, knowledge}; YAML errors there are warnings.
//   - metadata.yaml is NOT in an agentContentDir → YAML parse error is an error (not warning).
//   - inferVersion: skills/ or knowledge/ → v3, gen/ without entry/ → v2, entry/ → v1.
//   - forbidden dirs at tree root (entry, team) → warning only; agent dirs with absent_in_v3 → error.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// VAL-01 Freshly scaffolded v3 agent validates clean
func TestVAL01FreshV3ValidatesClean(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "alpha")

	out, code := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "alpha", "--memory-root", memDir)
	t.Logf("validate alpha (v3):\n%s", out)
	assert.Equal(t, 0, code, "v3 agent must validate clean: %s", out)
}

// VAL-02 Freshly scaffolded v1 agent validates clean
func TestVAL02FreshV1ValidatesClean(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV1Agent(t, cfg, memDir, "beta")

	out, code := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "beta", "--memory-root", memDir)
	t.Logf("validate beta (v1):\n%s", out)
	assert.Equal(t, 0, code, "v1 agent must validate clean: %s", out)
}

// VAL-03 Freshly scaffolded v2 agent validates clean
func TestVAL03FreshV2ValidatesClean(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV2Agent(t, cfg, memDir, "gamma")

	out, code := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "gamma", "--memory-root", memDir)
	t.Logf("validate gamma (v2):\n%s", out)
	assert.Equal(t, 0, code, "v2 agent must validate clean: %s", out)
}

// VAL-04 Missing memory.md in v3 agent instructions/ → circuit rule error
func TestVAL04MissingMemoryMdInInstructions(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "delta")

	// Remove instructions/memory.md.
	out, code := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("rm '%s/delta/instructions/memory.md'", memDir))
	require.Equal(t, 0, code, "rm instructions/memory.md: %s", out)

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "delta", "--memory-root", memDir)
	t.Logf("validate delta (missing instructions/memory.md):\n%s", valOut)
	assert.NotEqual(t, 0, valCode, "must exit non-zero for missing instructions/memory.md")
	// Either the required-file rule or the circuit rule fires.
	assert.True(t,
		strings.Contains(valOut, "instructions/memory.md") ||
			strings.Contains(valOut, "instructions"),
		"violation must reference instructions: %s", valOut)
}

// VAL-05 Missing required directory skills/ in v3 agent → error
func TestVAL05MissingSkillsDir(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "epsilon")

	// Remove skills/ entirely.
	out, code := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("rm -rf '%s/epsilon/skills'", memDir))
	require.Equal(t, 0, code, "rm -rf skills: %s", out)

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "epsilon", "--memory-root", memDir)
	t.Logf("validate epsilon (missing skills/):\n%s", valOut)
	assert.NotEqual(t, 0, valCode, "must exit non-zero for missing skills/")
	assert.True(t,
		strings.Contains(valOut, "skills"),
		`violation must reference "skills": %s`, valOut)
}

// VAL-06 Forbidden dir `entry/` in v3 agent → error (absent_in_v3)
func TestVAL06ForbiddenEntryDirInV3Agent(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "zeta")

	// Create entry/ inside the v3 agent dir.
	out, code := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("mkdir -p '%s/zeta/entry'", memDir))
	require.Equal(t, 0, code, "mkdir entry: %s", out)

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "zeta", "--memory-root", memDir)
	t.Logf("validate zeta (forbidden entry/):\n%s", valOut)
	assert.NotEqual(t, 0, valCode, "must exit non-zero for forbidden entry/ in v3 agent")
	assert.Contains(t, valOut, "entry",
		`violation must reference "entry": %s`, valOut)
}

// VAL-07 Forbidden dir `team/` at memory root → warning only, not error
func TestVAL07ForbiddenTeamAtRoot(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "eta")

	// Create team/ at memory root.
	out, code := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("mkdir -p '%s/team'", memDir))
	require.Equal(t, 0, code, "mkdir team/: %s", out)

	// Pass memDir as positional arg for tree-level validation.
	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", memDir)
	t.Logf("validate tree (team/ at root):\n%s", valOut)
	// Warnings do not cause non-zero exit.
	assert.Equal(t, 0, valCode, "team/ at root is a warning only; must exit 0: %s", valOut)
	// team/ warning should appear.
	assert.Contains(t, valOut, "team",
		`validate output must mention "team" warning: %s`, valOut)
}

// VAL-08 Roster .md at memory root → warning (tree validation)
func TestVAL08RosterMdAtRootIsWarning(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "theta")

	// Create a roster file at memory root.
	writeFile(t, cfg, memDir+"/agent_theta.md", "# roster file\n")

	// Pass memDir as positional arg for tree-level validation.
	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", memDir)
	t.Logf("validate tree (roster .md at root):\n%s", valOut)
	// Roster files produce warnings, not errors.
	assert.Equal(t, 0, valCode, "roster .md at root is a warning; must exit 0: %s", valOut)
	assert.Contains(t, valOut, "agent_theta.md",
		"violation must mention agent_theta.md: %s", valOut)
}

// VAL-09 Malformed YAML in instructions/ → warning (agentContentDir downgrade)
func TestVAL09MalformedYAMLInInstructionsIsWarning(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "iota")

	// Write an invalid YAML file inside instructions/.
	writeFile(t, cfg, memDir+"/iota/instructions/bad.yaml", "key: [unclosed bracket\n")

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "iota", "--memory-root", memDir)
	t.Logf("validate iota (bad YAML in instructions/):\n%s", valOut)
	// instructions/ is an agentContentDir → YAML error is downgraded to warning → exit 0.
	assert.Equal(t, 0, valCode,
		"YAML error in instructions/ must be a warning (exit 0): %s", valOut)
	assert.Contains(t, valOut, "bad.yaml",
		"output must mention the bad.yaml file: %s", valOut)
}

// VAL-10 Malformed metadata.yaml → error (NOT in agentContentDir)
func TestVAL10MalformedMetadataYamlIsError(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "kappa")

	// Overwrite metadata.yaml with invalid YAML.
	writeFile(t, cfg, memDir+"/kappa/metadata.yaml", "version_code: [bad\n")

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "kappa", "--memory-root", memDir)
	t.Logf("validate kappa (bad metadata.yaml):\n%s", valOut)
	// metadata.yaml is not in an agentContentDir → parse error is an error → non-zero exit.
	assert.NotEqual(t, 0, valCode,
		"malformed metadata.yaml must cause error exit: %s", valOut)
	assert.Contains(t, valOut, "metadata.yaml",
		"violation must reference metadata.yaml: %s", valOut)
}

// VAL-11 Agent with no metadata.yaml → structural version inference + error for missing file
//
// ASSUMPTION deviation: the validator infers v3 from structure (skills/ present → warning)
// but then validates the v3 contract — which requires metadata.yaml — so the result is
// exit 1 with 1 error (missing metadata.yaml) and 1 warning (inferred from structure).
// This is a real product behavior: structural inference is a best-effort warning; missing
// metadata.yaml is still a contract violation.
func TestVAL11NoMetadataYamlInference(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "lambda")

	// Remove metadata.yaml.
	out, code := harness.ExecInContainer(t, cfg,
		fmt.Sprintf("rm '%s/lambda/metadata.yaml'", memDir))
	require.Equal(t, 0, code, "rm metadata.yaml: %s", out)

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "lambda", "--memory-root", memDir)
	t.Logf("validate lambda (no metadata.yaml):\n%s", valOut)

	// Actual behavior: inferred v3 from structure (warning) but metadata.yaml
	// is required by v3 contract → exit 1.
	assert.NotEqual(t, 0, valCode, "missing metadata.yaml is a v3 contract violation: %s", valOut)
	// Inference warning must be present.
	assert.Contains(t, valOut, "inferred",
		"output must mention structural inference: %s", valOut)
	// Error about missing metadata.yaml.
	assert.Contains(t, valOut, "metadata.yaml",
		"error must reference the missing metadata.yaml: %s", valOut)
}

// VAL-12 --version v3 force-override on v1 agent → errors for missing v3 dirs
func TestVAL12ForceVersionV3OnV1Agent(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV1Agent(t, cfg, memDir, "mu")

	valOut, valCode := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", "mu", "--memory-root", memDir, "--version", "v3")
	t.Logf("validate mu (v1 forced to v3):\n%s", valOut)
	// v3 requires skills/, knowledge/, gen/plans/, gen/state/ which v1 doesn't have.
	assert.NotEqual(t, 0, valCode,
		"v1 agent forced to v3 must fail validation: %s", valOut)
	// Should see errors for missing v3 dirs.
	assert.True(t,
		strings.Contains(valOut, "skills") || strings.Contains(valOut, "knowledge"),
		"must report missing v3 required dirs: %s", valOut)
}

// VAL-13 `omni memory lint` on clean tree → zero violations
//
// ASSUMPTION deviation: `omni memory lint` takes a positional path argument, not --memory-root.
func TestVAL13LintCleanTreeZeroViolations(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "nu")

	// lint takes the path as a positional arg.
	lintOut, lintCode := harness.RunOmniAllowFail(t, cfg, "memory", "lint", memDir)
	t.Logf("lint (clean tree):\n%s", lintOut)
	assert.Equal(t, 0, lintCode, "lint on clean tree must exit 0: %s", lintOut)
}

// VAL-14 `omni memory lint` on tree with invalid YAML → error per file
//
// ASSUMPTION deviation: `omni memory lint` takes a positional path argument, not --memory-root.
func TestVAL14LintInvalidYamlIsError(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	makeV3Agent(t, cfg, memDir, "xi")

	// Write invalid YAML into metadata.yaml.
	writeFile(t, cfg, memDir+"/xi/metadata.yaml", "version_code: [bad\n")

	lintOut, lintCode := harness.RunOmniAllowFail(t, cfg, "memory", "lint", memDir)
	t.Logf("lint (invalid YAML):\n%s", lintOut)
	assert.NotEqual(t, 0, lintCode, "lint must exit non-zero for invalid YAML: %s", lintOut)
	assert.Contains(t, lintOut, "metadata.yaml",
		"lint must report the bad metadata.yaml file: %s", lintOut)
}
