package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestAgent returns a minimal agyAgent rooted in a temp dir so tests
// never touch the real ~/.gemini directory.
func newTestAgent(t *testing.T) *agyAgent {
	t.Helper()
	return &agyAgent{workDir: t.TempDir()}
}

// readMCPConfig reads and unmarshals the mcpServers map from path.
func readMCPConfig(t *testing.T, path string) map[string]rawMCPServer {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "mcp_config.json must be readable")
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &top), "mcp_config.json must be valid JSON")
	blob, ok := top["mcpServers"]
	require.True(t, ok, "mcp_config.json must contain 'mcpServers' key (not 'mcp_servers')")
	var servers map[string]rawMCPServer
	require.NoError(t, json.Unmarshal(blob, &servers))
	return servers
}

// ── Test 1: Unit-style smoke test ─────────────────────────────────────────────
// AddMCP with Global=false writes the entry to <workDir>/.gemini/config/mcp_config.json.
func TestAddMCP_WritesEntry(t *testing.T) {
	a := newTestAgent(t)

	_, err := a.AddMCP(codeagent.AddMCPParams{
		Server: codeagent.MCPServer{
			Name:      "axolink-test",
			Transport: codeagent.MCPTransportStdio,
			Command:   "/usr/bin/true",
		},
		Global: false,
	})
	require.NoError(t, err)

	cfgPath := filepath.Join(a.workDir, ".gemini", "config", "mcp_config.json")
	servers := readMCPConfig(t, cfgPath)
	_, ok := servers["axolink-test"]
	assert.True(t, ok, "AddMCP must write 'axolink-test' entry to mcp_config.json")
}

// ── Test 2: DeleteMCP and ListMCP round-trip ──────────────────────────────────
func TestDeleteMCP_RemovesEntry(t *testing.T) {
	a := newTestAgent(t)

	_, err := a.AddMCP(codeagent.AddMCPParams{
		Server: codeagent.MCPServer{Name: "axolink", Transport: codeagent.MCPTransportStdio, Command: "/usr/bin/omni"},
		Global: false,
	})
	require.NoError(t, err)

	_, err = a.DeleteMCP(codeagent.DeleteMCPParams{Name: "axolink", Global: false})
	require.NoError(t, err)

	cfgPath := filepath.Join(a.workDir, ".gemini", "config", "mcp_config.json")
	servers := readMCPConfig(t, cfgPath)
	_, ok := servers["axolink"]
	assert.False(t, ok, "axolink must be absent after DeleteMCP")
}

func TestDeleteMCP_NonExistent_IsNoOp(t *testing.T) {
	a := newTestAgent(t)
	_, err := a.DeleteMCP(codeagent.DeleteMCPParams{Name: "does-not-exist", Global: false})
	require.NoError(t, err, "deleting a non-existent server must not error")
}

func TestListMCP_RoundTrip(t *testing.T) {
	a := newTestAgent(t)

	_, err := a.AddMCP(codeagent.AddMCPParams{
		Server: codeagent.MCPServer{Name: "axolink", Transport: codeagent.MCPTransportStdio, Command: "/usr/bin/omni", Args: []string{"axolink"}},
		Global: false,
	})
	require.NoError(t, err)

	res, err := a.ListMCP(codeagent.ListMCPParams{Global: false})
	require.NoError(t, err)
	require.Len(t, res.Servers, 1)
	assert.Equal(t, "axolink", res.Servers[0].Name)
	assert.Equal(t, codeagent.MCPTransportStdio, res.Servers[0].Transport)
}

func TestAddMCP_ConcurrentWrites(t *testing.T) {
	a := newTestAgent(t)
	const n = 10
	errs := make(chan error, n)
	for i := range n {
		go func(name string) {
			_, err := a.AddMCP(codeagent.AddMCPParams{
				Server: codeagent.MCPServer{Name: name, Transport: codeagent.MCPTransportStdio, Command: "/usr/bin/true"},
				Global: false,
			})
			errs <- err
		}("server-" + string(rune('a'+i)))
	}
	for range n {
		require.NoError(t, <-errs)
	}
	res, err := a.ListMCP(codeagent.ListMCPParams{Global: false})
	require.NoError(t, err)
	assert.Len(t, res.Servers, n, "all concurrent writes must persist")
}

// ── Test 3: Idempotency ────────────────────────────────────────────────────────
// Calling AddMCP twice for the same server name must produce exactly 1 entry.
func TestAddMCP_Idempotent(t *testing.T) {
	a := newTestAgent(t)

	server := codeagent.MCPServer{
		Name:      "axolink",
		Transport: codeagent.MCPTransportStdio,
		Command:   "/usr/bin/omni",
		Args:      []string{"axolink"},
	}
	params := codeagent.AddMCPParams{Server: server, Global: false}

	_, err := a.AddMCP(params)
	require.NoError(t, err, "first AddMCP must not fail")
	_, err = a.AddMCP(params)
	require.NoError(t, err, "second AddMCP must not fail")

	cfgPath := filepath.Join(a.workDir, ".gemini", "config", "mcp_config.json")
	servers := readMCPConfig(t, cfgPath)

	count := 0
	for name := range servers {
		if name == "axolink" {
			count++
		}
	}
	assert.Equal(t, 1, count, "exactly 1 'axolink' entry must exist after two identical AddMCP calls")
}

// ── Test 4: Format validation ─────────────────────────────────────────────────
// The written file must use 'mcpServers' (not 'mcp_servers') and the axolink
// entry must have "type": "stdio".
func TestAddMCP_Format(t *testing.T) {
	a := newTestAgent(t)

	_, err := a.AddMCP(codeagent.AddMCPParams{
		Server: codeagent.MCPServer{
			Name:      "axolink",
			Transport: codeagent.MCPTransportStdio,
			Command:   "/usr/bin/omni",
			Args:      []string{"axolink"},
		},
		Global: false,
	})
	require.NoError(t, err)

	cfgPath := filepath.Join(a.workDir, ".gemini", "config", "mcp_config.json")

	// Top-level key must be 'mcpServers', not 'mcp_servers'
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &top))
	_, hasMcpServers := top["mcpServers"]
	_, hasMcpSnake := top["mcp_servers"]
	assert.True(t, hasMcpServers, "top-level key must be 'mcpServers'")
	assert.False(t, hasMcpSnake, "top-level key must NOT be 'mcp_servers'")

	servers := readMCPConfig(t, cfgPath)
	entry, ok := servers["axolink"]
	require.True(t, ok, "axolink entry must be present")
	assert.Equal(t, "stdio", entry.Type, "axolink entry must have type=stdio")
	assert.Equal(t, "/usr/bin/omni", entry.Command, "axolink entry must preserve command path")
	assert.Equal(t, []string{"axolink"}, entry.Args, "axolink entry must preserve args")
}

// ── Global scope: AddMCP with Global=true writes to ~/.gemini/config ──────────
// This mirrors what ServiceMux does on startup. We redirect HOME so the test
// never touches the real home directory.
func TestAddMCP_GlobalWritesToHomeDir(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	a := newTestAgent(t)

	_, err := a.AddMCP(codeagent.AddMCPParams{
		Server: codeagent.MCPServer{
			Name:      "axolink",
			Transport: codeagent.MCPTransportStdio,
			Command:   "/usr/bin/omni",
			Args:      []string{"axolink"},
		},
		Global: true,
	})
	require.NoError(t, err)

	cfgPath := filepath.Join(fakeHome, ".gemini", "config", "mcp_config.json")
	servers := readMCPConfig(t, cfgPath)
	entry, ok := servers["axolink"]
	require.True(t, ok, "global AddMCP must write entry to $HOME/.gemini/config/mcp_config.json")
	assert.Equal(t, "stdio", entry.Type)
}
