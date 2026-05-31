package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"
	omniconfig "github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tempConfigPath creates a temp dir and returns a config.toml path inside it.
// The file itself is NOT created — tests decide whether to pre-seed it.
func tempConfigPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, ".codex", "config.toml")
}

// readRawTOML decodes path into a raw map. Returns empty map if file absent.
func readRawTOML(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	raw := map[string]interface{}{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return raw
	}
	require.NoError(t, err, "readRawTOML: read file")
	require.NoError(t, toml.Unmarshal(data, &raw), "readRawTOML: parse TOML")
	return raw
}

// seedTOML writes m as TOML to path, creating parent dirs.
func seedTOML(t *testing.T, path string, m map[string]interface{}) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, toml.NewEncoder(f).Encode(m))
}

// addTestMCP inserts one stdio MCP entry via withMCPConfig.
func addTestMCP(path, name, command string) error {
	return withMCPConfig(path, func(raw map[string]interface{}) error {
		servers := getMCPServersRaw(raw)
		entry, err := mcpServerToRaw(codeagent.MCPServer{
			Name:      name,
			Transport: codeagent.MCPTransportStdio,
			Command:   command,
		})
		if err != nil {
			return err
		}
		servers[name] = entry
		raw["mcp_servers"] = servers
		return nil
	})
}

// ─── CONTEXT A — fresh install (no config.toml) ──────────────────────────────

func TestConfigTransformer_ContextA_FreshInstall(t *testing.T) {
	path := tempConfigPath(t)

	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "pre-condition: config.toml must not exist")

	require.NoError(t, addTestMCP(path, "omni-mcp", "omni-server"))

	_, statErr = os.Stat(path)
	require.NoError(t, statErr, "config.toml must be created by addMCP")

	raw := readRawTOML(t, path)
	servers, _ := raw["mcp_servers"].(map[string]interface{})
	require.NotNil(t, servers, "mcp_servers section must be created")
	assert.Contains(t, servers, "omni-mcp", "omni-mcp entry must be in mcp_servers")

	srv, _ := servers["omni-mcp"].(map[string]interface{})
	require.NotNil(t, srv, "omni-mcp entry must be a map")
	assert.Equal(t, "omni-server", srv["command"], "command field must be set correctly")
}

// ─── CONTEXT B — empty config.toml ───────────────────────────────────────────

func TestConfigTransformer_ContextB_EmptyFile(t *testing.T) {
	path := tempConfigPath(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte{}, 0o644))

	require.NoError(t, addTestMCP(path, "omni-mcp", "omni-server"))

	raw := readRawTOML(t, path)
	require.NotEmpty(t, raw, "config.toml must not be empty after write")

	servers, _ := raw["mcp_servers"].(map[string]interface{})
	require.NotNil(t, servers, "mcp_servers must be present after writing to empty file")
	assert.Contains(t, servers, "omni-mcp")
}

// ─── CONTEXT C — existing [model] section only ────────────────────────────────

func TestConfigTransformer_ContextC_ModelSectionPreserved(t *testing.T) {
	path := tempConfigPath(t)
	seedTOML(t, path, map[string]interface{}{
		"model":  "gpt-4o",
		"reason": "cost-optimised",
	})

	// MCP write.
	require.NoError(t, addTestMCP(path, "omni-mcp", "omni-server"))

	// Hook write.
	require.NoError(t, writeHooksConfig(path, map[string][]codexHookMatcher{
		"UserPromptSubmit": {{
			Hooks: []codexHookDef{{Type: "command", Command: "omni hook --event UserPromptSubmit"}},
		}},
	}))

	raw := readRawTOML(t, path)

	assert.Equal(t, "gpt-4o", raw["model"], "[model] must be preserved after MCP + hook writes")
	assert.Equal(t, "cost-optimised", raw["reason"], "custom scalar must be preserved")

	servers, _ := raw["mcp_servers"].(map[string]interface{})
	require.NotNil(t, servers, "mcp_servers must be present")
	assert.Contains(t, servers, "omni-mcp")

	hooksMap, _ := raw["hooks"].(map[string]interface{})
	require.NotNil(t, hooksMap, "hooks section must be present")
	assert.Contains(t, hooksMap, "UserPromptSubmit")
}

// ─── CONTEXT D — existing non-omni mcpServers (no clobber) ───────────────────

func TestConfigTransformer_ContextD_ExistingMCPServersPreserved(t *testing.T) {
	path := tempConfigPath(t)
	seedTOML(t, path, map[string]interface{}{
		"mcp_servers": map[string]interface{}{
			"third-party": map[string]interface{}{
				"command": "/usr/bin/third-party-mcp",
				"enabled": true,
			},
		},
	})

	require.NoError(t, addTestMCP(path, "omni-mcp", "omni-server"))

	raw := readRawTOML(t, path)
	servers, _ := raw["mcp_servers"].(map[string]interface{})
	require.NotNil(t, servers, "mcp_servers must be present")

	assert.Contains(t, servers, "third-party", "third-party entry must survive omni MCP write")
	assert.Contains(t, servers, "omni-mcp", "omni-mcp must be added alongside third-party")

	tp, _ := servers["third-party"].(map[string]interface{})
	require.NotNil(t, tp)
	assert.Equal(t, "/usr/bin/third-party-mcp", tp["command"], "third-party command must be unchanged")
}

// ─── CONTEXT E — hook accumulation: multiple Add() calls don't self-clobber ──
//
// The HookTransformer accumulates hooks across multiple Add() calls by building
// hooksByEvent from its in-memory index before each write. This test verifies
// that a second Add() does not overwrite the first, and that non-hooks sections
// (model) survive each write.

func TestConfigTransformer_ContextE_HookAccumulation(t *testing.T) {
	// Redirect globalConfigPath() to a temp dir so we don't touch ~/.codex.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configPath, err := globalConfigPath()
	require.NoError(t, err)

	// Pre-seed model into the config so we can verify it survives hook writes.
	raw0 := map[string]interface{}{"model": "claude-3-5-sonnet"}
	require.NoError(t, writeConfigTOML(configPath, raw0))

	tr := NewHookTransformer()

	cmd1 := "omni"
	entry1 := omniconfig.HookEntry{
		Command: &cmd1,
		Args:    []string{"hook", "--event", "UserPromptSubmit"},
	}
	ok1 := tr.Add("omni.UserPromptSubmit", entry1)
	assert.True(t, ok1, "first Add should succeed")

	cmd2 := "omni"
	entry2 := omniconfig.HookEntry{
		Command: &cmd2,
		Args:    []string{"hook", "--event", "PreToolUse"},
	}
	ok2 := tr.Add("omni.PreToolUse", entry2)
	assert.True(t, ok2, "second Add should succeed")

	// Both hooks must be retrievable — second Add must not have overwritten the first.
	hooks := tr.GetHooks()
	assert.GreaterOrEqual(t, len(hooks), 2, "both hooks must accumulate — no self-clobber")

	// model must survive all hook writes.
	rawAfter := readRawTOML(t, configPath)
	assert.Equal(t, "claude-3-5-sonnet", rawAfter["model"], "model must survive hook writes")
}

// ─── CONTEXT F — full round-trip (mcp + hooks, cross-section preservation) ───

func TestConfigTransformer_ContextF_FullRoundTrip(t *testing.T) {
	path := tempConfigPath(t)

	// Pre-seed with model, existing mcp_servers, and existing hooks.
	seedTOML(t, path, map[string]interface{}{
		"model": "claude-3-5-sonnet",
		"mcp_servers": map[string]interface{}{
			"existing-mcp": map[string]interface{}{
				"command": "/usr/bin/existing-mcp",
				"enabled": true,
			},
		},
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "echo pre-tool-use",
						},
					},
				},
			},
		},
	})

	// Step 1: add MCP → hooks and model must be preserved.
	require.NoError(t, addTestMCP(path, "omni-mcp", "omni-server"))

	raw1 := readRawTOML(t, path)
	assert.Equal(t, "claude-3-5-sonnet", raw1["model"], "model must survive MCP write")
	_, hooks1Present := raw1["hooks"]
	assert.True(t, hooks1Present, "hooks section must survive MCP write")
	srv1, _ := raw1["mcp_servers"].(map[string]interface{})
	assert.Contains(t, srv1, "existing-mcp", "existing-mcp must survive after MCP write")
	assert.Contains(t, srv1, "omni-mcp", "omni-mcp must be added")

	// Step 2: write hooks → mcp_servers and model must be preserved.
	require.NoError(t, writeHooksConfig(path, map[string][]codexHookMatcher{
		"UserPromptSubmit": {{
			Hooks: []codexHookDef{{Type: "command", Command: "omni hook --event UserPromptSubmit"}},
		}},
	}))

	raw2 := readRawTOML(t, path)
	assert.Equal(t, "claude-3-5-sonnet", raw2["model"], "model must survive hook write")
	srv2, _ := raw2["mcp_servers"].(map[string]interface{})
	assert.Contains(t, srv2, "existing-mcp", "existing-mcp must survive hook write")
	assert.Contains(t, srv2, "omni-mcp", "omni-mcp must survive hook write")
	hooksMap, _ := raw2["hooks"].(map[string]interface{})
	require.NotNil(t, hooksMap, "hooks section must be present after hook write")
	assert.Contains(t, hooksMap, "UserPromptSubmit", "UserPromptSubmit hook must be in final file")
}

// ─── CONTEXT G — concurrent writes (stress) ──────────────────────────────────

func TestConfigTransformer_ContextG_ConcurrentWrites(t *testing.T) {
	path := tempConfigPath(t)

	const goroutines = 3
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("omni-mcp-%d", i)
			errs[i] = addTestMCP(path, name, fmt.Sprintf("omni-server-%d", i))
		}()
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "goroutine %d: addTestMCP must not error", i)
	}

	// Final file must parse as valid TOML.
	raw := readRawTOML(t, path)

	servers, _ := raw["mcp_servers"].(map[string]interface{})
	require.NotNil(t, servers, "mcp_servers must be present after concurrent writes")

	// All 3 entries must be in the final file — no partial-write corruption.
	for i := 0; i < goroutines; i++ {
		name := fmt.Sprintf("omni-mcp-%d", i)
		assert.Contains(t, servers, name,
			"concurrent write %d: entry %q must be in final config", i, name)
	}
}
