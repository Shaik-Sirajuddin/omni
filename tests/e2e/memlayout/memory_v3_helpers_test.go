//go:build e2e

package memlayout_test

// Shared helpers for v3 test scenarios.
// These extend the helpers already in memory_v3_test.go with additional
// scaffolding utilities used across VAL, AI, COEX, MIG, RACE, and NEG tests.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/require"
)

// makeV3Agent creates a complete v3 agent directory at memDir/<name>/ with all
// required dirs, memory.md files, and metadata.yaml. This is the "clean" v3
// scaffold used by VAL, COEX, and RACE tests.
func makeV3Agent(t *testing.T, cfg harness.TestConfig, memDir, name string) {
	t.Helper()
	agentDir := fmt.Sprintf("%s/%s", memDir, name)

	// Required v3 dirs.
	dirs := []string{
		agentDir,
		agentDir + "/instructions",
		agentDir + "/skills",
		agentDir + "/knowledge",
		agentDir + "/knowledge/com",
		agentDir + "/gen",
		agentDir + "/gen/plans",
		agentDir + "/gen/state",
	}
	for _, d := range dirs {
		out, code := harness.ExecInContainer(t, cfg, fmt.Sprintf("mkdir -p '%s'", d))
		require.Equal(t, 0, code, "mkdir %s: %s", d, out)
	}

	// metadata.yaml.
	writeFile(t, cfg, agentDir+"/metadata.yaml", "version_code: 3\nversion: v3\n")

	// circuit rule: memory.md in every dir.
	for _, d := range dirs {
		base := d[len(agentDir):]
		if base == "" {
			base = name
		}
		stub := fmt.Sprintf("---\ncircuit: %s\nversion: v3\nsummary:\n---\n# %s circuit\n", base, base)
		writeFile(t, cfg, d+"/memory.md", stub)
	}
}

// makeV2Agent creates a v2 agent directory at memDir/agents/<name>/ with the v2
// required dirs (instructions/, tasks/, gen/, state/) and metadata.yaml.
func makeV2Agent(t *testing.T, cfg harness.TestConfig, memDir, name string) {
	t.Helper()
	agentDir := fmt.Sprintf("%s/agents/%s", memDir, name)

	dirs := []string{
		agentDir + "/instructions",
		agentDir + "/tasks",
		agentDir + "/gen",
		agentDir + "/state",
	}
	for _, d := range dirs {
		out, code := harness.ExecInContainer(t, cfg, fmt.Sprintf("mkdir -p '%s'", d))
		require.Equal(t, 0, code, "mkdir %s: %s", d, out)
	}

	writeFile(t, cfg, agentDir+"/metadata.yaml", "version_code: 2\nversion: v2\n")
	// Write some content files.
	writeFile(t, cfg, agentDir+"/instructions/instr.yaml", "instruction: hello v2\n")
	writeFile(t, cfg, agentDir+"/tasks/task.yaml", "task: v2-task\nstatus: pending\n")
	writeFile(t, cfg, agentDir+"/gen/plan.md", "# v2 plan\n")
	writeFile(t, cfg, agentDir+"/state/progress.md", "# v2 state\n")
}

// assertFileContains checks that a file's content contains the given substring.
func assertFileContains(t *testing.T, cfg harness.TestConfig, path, substr string) {
	t.Helper()
	out, code := harness.ExecInContainer(t, cfg, fmt.Sprintf("cat '%s'", path))
	require.Equal(t, 0, code, "cat %s: %s", path, out)
	if !strings.Contains(out, substr) {
		t.Errorf("file %s: expected to contain %q, got:\n%s", path, substr, out)
	}
}
