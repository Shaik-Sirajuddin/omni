//go:build e2e

package memlayout_test

// VFX — Validate --fix naming scenarios
//
// These tests verify that:
//   - A freshly created agent's memory.md files do NOT contain literal `<agent_name>`.
//   - `omni memory validate` warns when `<agent_name>` is found in memory.md.
//   - `omni memory validate --fix` replaces `<agent_name>` with the real name.
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

// VFX-01: A freshly created v3 agent must have no literal `<agent_name>`
// in any memory.md file.
func TestVFX01CreatedAgentHasNoAgentNamePlaceholder(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	ctx := context.Background()

	agentName := "vfxcreated"
	out, code := harness.RunOmniAllowFail(t, cfg, "agent", "init", agentName, "--workspace", cfg.Workspace, "--provider", "claude")
	t.Logf("create: %s", out)
	require.Equal(t, 0, code, "agent create failed: %s", out)

	// Grep for literal <agent_name> in any memory.md under the agent dir.
	agentDir := fmt.Sprintf("%s/%s", memDir, agentName)
	grepCode, grepOut, grepErr := cfg.Exec.RunCommand(ctx, []string{
		"grep", "-r", "<agent_name>", agentDir,
	})
	require.NoError(t, grepErr)
	// grep exits 0 when found, 1 when not found. We want NOT found (exit 1).
	assert.NotEqual(t, 0, grepCode,
		"no file under %s should contain literal <agent_name>, found:\n%s", agentDir, string(grepOut))
}

// VFX-02: `omni memory validate` warns when a memory.md contains
// literal `<agent_name>` (simulate a manually broken file).
func TestVFX02ValidateWarnsOnAgentNamePlaceholder(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)

	agentName := "vfxwarn"
	makeV3Agent(t, cfg, memDir, agentName)

	// Overwrite the root memory.md with a literal <agent_name> placeholder.
	agentDir := fmt.Sprintf("%s/%s", memDir, agentName)
	broken := "---\ncircuit: root\nversion: v3\nsummary:\n---\n# Agent: <agent_name>\n"
	writeFile(t, cfg, agentDir+"/memory.md", broken)

	// Run validate without --fix.
	out, code := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", agentName, "--memory-root", memDir)
	t.Logf("validate (no --fix):\n%s", out)

	// Validate may pass (exit 0) but must emit a warning about the placeholder.
	_ = code
	assert.True(t, strings.Contains(out, "<agent_name>"),
		"validate output should mention the <agent_name> placeholder: %s", out)
}

// VFX-03: `omni memory validate --fix` rewrites the memory.md replacing
// `<agent_name>` with the real agent name.
func TestVFX03ValidateFixReplacesAgentNamePlaceholder(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	ctx := context.Background()

	agentName := "vfxfix"
	makeV3Agent(t, cfg, memDir, agentName)

	// Inject literal <agent_name> into the root memory.md.
	agentDir := fmt.Sprintf("%s/%s", memDir, agentName)
	broken := "---\ncircuit: root\nversion: v3\nsummary:\n---\n# Agent: <agent_name>\nHello <agent_name>!\n"
	writeFile(t, cfg, agentDir+"/memory.md", broken)

	// Run validate --fix.
	out, code := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", agentName, "--memory-root", memDir, "--fix")
	t.Logf("validate --fix:\n%s", out)
	assert.Equal(t, 0, code, "validate --fix should exit 0: %s", out)

	// The file should now contain the real agent name, not the placeholder.
	grepBadCode, _, badErr := cfg.Exec.RunCommand(ctx, []string{
		"grep", "-l", "<agent_name>", agentDir + "/memory.md",
	})
	require.NoError(t, badErr)
	assert.NotEqual(t, 0, grepBadCode, "memory.md must not contain <agent_name> after --fix")

	grepGoodCode, goodOut, goodErr := cfg.Exec.RunCommand(ctx, []string{
		"grep", agentName, agentDir + "/memory.md",
	})
	require.NoError(t, goodErr)
	assert.Equal(t, 0, grepGoodCode,
		"memory.md should contain the real agent name %q after --fix: %s", agentName, string(goodOut))
}

// VFX-04: `omni memory validate --fix` is idempotent — running it twice on
// a clean file (no placeholder) must succeed without corrupting the file.
func TestVFX04ValidateFixIdempotentOnCleanFile(t *testing.T) {
	t.Parallel()
	cfg, memDir := memTestConfig(t)
	ctx := context.Background()

	agentName := "vfxidem"
	makeV3Agent(t, cfg, memDir, agentName)

	agentDir := fmt.Sprintf("%s/%s", memDir, agentName)
	origCode, original, origErr := cfg.Exec.RunCommand(ctx, []string{"cat", agentDir + "/memory.md"})
	require.NoError(t, origErr)
	require.Equal(t, 0, origCode)

	// First fix (already clean).
	_, code1 := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", agentName, "--memory-root", memDir, "--fix")
	assert.Equal(t, 0, code1, "first --fix must succeed")

	// Second fix.
	_, code2 := harness.RunOmniAllowFail(t, cfg,
		"memory", "validate", "--agent", agentName, "--memory-root", memDir, "--fix")
	assert.Equal(t, 0, code2, "second --fix must succeed (idempotent)")

	// File content must be unchanged (no spurious rewrites).
	afterCode, after, afterErr := cfg.Exec.RunCommand(ctx, []string{"cat", agentDir + "/memory.md"})
	require.NoError(t, afterErr)
	require.Equal(t, 0, afterCode)
	assert.Equal(t, string(original), string(after),
		"file content must be unchanged after --fix on a clean file")
}
