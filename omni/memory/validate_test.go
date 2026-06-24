package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateFixReplacesAgentNamePlaceholder(t *testing.T) {
	memRoot := t.TempDir()

	v3Dir := filepath.Join(memRoot, "v3")
	require.NoError(t, os.MkdirAll(v3Dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(v3Dir, "metadata.yaml"), []byte("version_code: 3\nstatus: active\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(v3Dir, "semantics.yaml"), []byte("layout:\n  agent_files:\n    - memory.md\n  agent_dirs: []\n  absent_in_v3: []\n"), 0o644))

	agentName := "testagent"
	agentDir := filepath.Join(memRoot, agentName)
	require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "instructions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "metadata.yaml"), []byte("version: v3\nversion_code: 3\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "memory.md"), []byte("# Agent: <agent_name>\nHello <agent_name>\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "instructions", "memory.md"), []byte("# Instructions for <agent_name>\n"), 0o644))

	report, err := Validate(agentDir, Options{MemoryRoot: memRoot})
	require.NoError(t, err)
	foundWarn := false
	for _, v := range report.Violations {
		if v.Severity == SeverityWarning && strings.Contains(v.Message, "<agent_name>") {
			foundWarn = true
		}
	}
	assert.True(t, foundWarn, "validate without --fix should warn about literal <agent_name>")

	report2, err := Validate(agentDir, Options{MemoryRoot: memRoot, Fix: true})
	require.NoError(t, err)
	_ = report2

	data, err := os.ReadFile(filepath.Join(agentDir, "memory.md"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "<agent_name>")
	assert.Contains(t, string(data), agentName)
}
