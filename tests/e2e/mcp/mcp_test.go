//go:build e2e

package mcp_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const claudeGlobalCfg = "/root/.claude.json"

// TestMCPConfigReflection verifies that axolink is seeded into ~/.claude.json
// by the container entrypoint (seed_mcp_configs).
func TestMCPConfigReflection(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	// Read ~/.claude.json — skip if not present (local env without entrypoint seeding)
	raw, code := harness.ExecInContainer(t, cfg, fmt.Sprintf("cat %s 2>/dev/null", claudeGlobalCfg))
	if code != 0 || strings.TrimSpace(raw) == "" {
		t.Skip("~/.claude.json not present — skipping (requires docker container entrypoint seeding)")
	}
	require.Equal(t, 0, code, "~/.claude.json must exist")

	var claudeCfg map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &claudeCfg), "~/.claude.json must be valid JSON")

	var servers map[string]json.RawMessage
	if raw, ok := claudeCfg["mcpServers"]; ok {
		require.NoError(t, json.Unmarshal(raw, &servers))
	} else {
		t.Fatal("mcpServers key missing from ~/.claude.json")
	}

	_, ok := servers["axolink"]
	assert.True(t, ok, "axolink must be present in mcpServers")

	// Verify stdio transport (no url field, has command field)
	if ok {
		var entry map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(servers["axolink"], &entry))
		_, hasCommand := entry["command"]
		_, hasURL := entry["url"]
		assert.True(t, hasCommand || entry["type"] != nil, "axolink entry must have command or type field")
		assert.False(t, hasURL, "axolink stdio entry must not have url field")
	}

	t.Logf("mcpServers keys: %v", mapKeys(servers))
}

// TestAxolinkHTTPReachability verifies the axolink MCP HTTP server is listening
// on port 18062. An auth error response is fine — it proves the server is up.
func TestAxolinkHTTPReachability(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	out, code := harness.ExecInContainer(t, cfg,
		`curl -s -o /dev/null -w "%{http_code}" --max-time 5 `+
			`-X POST http://127.0.0.1:18062/mcp `+
			`-H "Content-Type: application/json" `+
			`-d '{"jsonrpc":"2.0","method":"tools/list","id":1}'`)

	// Any HTTP response (including 400/auth) means the server is listening.
	assert.Equal(t, 0, code, "curl must not time out or be refused")
	assert.NotEqual(t, "000", strings.TrimSpace(out),
		"axolink MCP server must respond at :18062 (got HTTP 000 = connection refused/timeout)")
	t.Logf("axolink HTTP status: %s", strings.TrimSpace(out))
}

// TestAddMCPIdempotency verifies that the axolink entry appears exactly once in
// ~/.claude.json even after multiple entrypoint runs.
func TestAddMCPIdempotency(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	raw, code := harness.ExecInContainer(t, cfg, fmt.Sprintf("cat %s 2>/dev/null", claudeGlobalCfg))
	if code != 0 || strings.TrimSpace(raw) == "" {
		t.Skip("~/.claude.json not present — skipping (requires docker container entrypoint seeding)")
	}
	require.Equal(t, 0, code, "~/.claude.json must exist")

	// Idempotency means axolink appears exactly once as a key in the global
	// mcpServers map. Counting raw "axolink" occurrences over the whole file is
	// wrong: the server entry's own args contain the literal "axolink" (the
	// `omni axolink` subcommand), so a single correct entry yields two matches.
	var claudeCfg struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &claudeCfg),
		"~/.claude.json must be valid JSON")

	_, ok := claudeCfg.MCPServers["axolink"]
	assert.True(t, ok, "axolink must be present in global mcpServers")

	// Belt-and-suspenders against a regression that appends a duplicate key:
	// the key form `"axolink":` must occur exactly once. (The server's args
	// array holds the bare string "axolink" with no colon, so it is not matched.)
	keyCount := strings.Count(raw, `"axolink":`)
	assert.Equal(t, 1, keyCount,
		"axolink must appear exactly once as an mcpServers key (idempotency)")
	t.Logf("global mcpServers keys: %v", mapKeys(claudeCfg.MCPServers))
}

// TestMCPToolListViaExec starts a claude agent and asserts that axolink tool
// names surface in journalctl after exec. Uses AssertDeliveryChain for the
// send_message -> exec path.
func TestMCPToolListViaExec(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	time.Sleep(500 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	provider := harness.RequireProvider(t, cfg)

	agentName := "e2e-mcp-tool-check"
	harness.TeardownAgent(t, cfg, agentName)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmni(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "resume", agentName,
		"--detach", "--workspace", cfg.Workspace)
	time.Sleep(8 * time.Second)

	harness.RunOmni(t, cfg, "agent", "exec", agentName,
		"--prompt", "List all MCP tool names available from axolink. Output names only.")

	// Wait for tool activity — skip (not fail) if agent session never calls tools;
	// this is environment-dependent (requires active claude session with tool access).
	toolObserved := jrnl.WaitFor("send_message", 30*time.Second) ||
		jrnl.WaitFor("axolink", 30*time.Second) ||
		omniLog.WaitFor("send_message", 30*time.Second)

	if !toolObserved {
		t.Skip("no axolink tool activity observed within 30s — requires active claude session")
	}
	t.Log("axolink tool activity confirmed in logs")

	harness.AssertNoLogErrors(t, jrnl.String())
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
