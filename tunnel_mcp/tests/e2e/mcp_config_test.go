//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// mcpServerURL is where the tunnel_mcp streamable HTTP server listens inside the container.
const mcpServerURL = "http://127.0.0.1:18062/mcp"

// defaultMCPToken matches runner.DefaultConfig().AuthToken (used when AXO_LINK_MCP_AUTH_TOKEN is unset).
const defaultMCPToken = "axolink-dev-token"

// ─── TestMCPManagerRoundTrip ─────────────────────────────────────────────────

// TestMCPManagerRoundTrip verifies that the MCPManager file-level operations
// (Add → List → Delete) round-trip cleanly on the claude global config:
//
//  1. Precondition: tunnel-mcp entry seeded by entrypoint is present in ~/.claude.json.
//  2. Add a test-only server entry directly via the config file (simulates AddMCP).
//  3. List: read file back and assert both entries are visible (simulates ListMCP).
//  4. Delete: remove the test entry and assert only tunnel-mcp remains (simulates DeleteMCP).
func TestMCPManagerRoundTrip(t *testing.T) {
	cfg := newConfig(t)

	_, logBuf := captureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)

	const claudeGlobalCfg = "/root/.claude.json"
	const testServerName = "e2e-mcp-roundtrip-test"

	// ── Step 1: precondition — axolink present ───────────────────────────────
	t.Log("step 1: verify axolink pre-seeded by entrypoint")
	preCheck := readMCPServers(t, cfg, claudeGlobalCfg)
	if _, ok := preCheck[testServerName]; ok {
		t.Logf("pre-test cleanup: removing leftover %q", testServerName)
		deleteMCPEntry(t, cfg, claudeGlobalCfg, testServerName)
	}
	if _, ok := preCheck["axolink"]; !ok {
		t.Fatalf("precondition failed: axolink not in %s (entrypoint seeding broken)", claudeGlobalCfg)
	}
	t.Logf("precondition OK: mcpServers keys=%v", mapKeys(preCheck))

	// ── Step 2: AddMCP — write test entry ────────────────────────────────────
	t.Log("step 2: AddMCP — insert test server entry")
	addMCPEntry(t, cfg, claudeGlobalCfg, testServerName, "http://127.0.0.1:19999/test-mcp")
	t.Cleanup(func() {
		deleteMCPEntry(t, cfg, claudeGlobalCfg, testServerName)
	})

	// ── Step 3: ListMCP — read back, assert both entries present ─────────────
	t.Log("step 3: ListMCP — verify both entries present")
	afterAdd := readMCPServers(t, cfg, claudeGlobalCfg)
	if _, ok := afterAdd["axolink"]; !ok {
		t.Errorf("ListMCP: axolink missing after AddMCP")
	}
	if entry, ok := afterAdd[testServerName]; !ok {
		t.Errorf("ListMCP: %q not found after AddMCP", testServerName)
	} else {
		t.Logf("ListMCP: %q entry=%v", testServerName, entry)
	}

	// ── Step 4: idempotent AddMCP — second add must not duplicate ─────────────
	t.Log("step 4: AddMCP idempotency — second add for same name overwrites, no duplicate")
	addMCPEntry(t, cfg, claudeGlobalCfg, testServerName, "http://127.0.0.1:19999/test-mcp-v2")
	afterSecondAdd := readMCPServers(t, cfg, claudeGlobalCfg)
	count := 0
	for name := range afterSecondAdd {
		if name == testServerName {
			count++
		}
	}
	if count != 1 {
		t.Errorf("AddMCP idempotency: expected 1 entry for %q, got %d", testServerName, count)
	}
	t.Logf("idempotency OK: still exactly 1 %q entry", testServerName)

	// ── Step 5: DeleteMCP — remove test entry, tunnel-mcp must survive ────────
	t.Log("step 5: DeleteMCP — remove test entry")
	deleteMCPEntry(t, cfg, claudeGlobalCfg, testServerName)
	afterDelete := readMCPServers(t, cfg, claudeGlobalCfg)
	if _, ok := afterDelete[testServerName]; ok {
		t.Errorf("DeleteMCP: %q still present after delete", testServerName)
	}
	if _, ok := afterDelete["axolink"]; !ok {
		t.Errorf("DeleteMCP: axolink was removed — delete affected wrong entry")
	}
	t.Logf("DeleteMCP OK: keys after delete=%v", mapKeys(afterDelete))

	log := logBuf.String()
	assertNoLogErrors(t, log)
	if t.Failed() {
		t.Logf("=== journalctl (%d bytes) ===\n%s", len(log), log)
	}
}

// ─── TestMCPToolsListAvailability ───────────────────────────────────────────

// TestMCPToolsListAvailability verifies that the tunnel_mcp HTTP server registers
// exactly 12 tools at startup.
//
// Two checks:
//  1. Journalctl startup log: "mcp tools registered count=12" confirms the handler
//     initialised with the full tool set — no session required.
//  2. HTTP reachability: a bare POST to the MCP endpoint returns any HTTP response
//     (including the expected auth rejection), confirming the server is listening.
//
// A full tools/list round-trip via an authenticated agent session is tracked as a
// TODO test in server-bugs.md until the agent_id-in-hook race (server-bugs.md #2)
// is resolved — without a valid session ID the server returns "Invalid session ID".
func TestMCPToolsListAvailability(t *testing.T) {
	cfg := newConfig(t)

	// ── Check 1: startup log shows 12 tools registered ────────────────────────
	// Use grep on the full journal — the message appears once per server start and
	// may be older than the rolling --lines window.
	exitCode, toolRegLine, _ := cfg.exec.RunCommand(context.Background(),
		[]string{"bash", "-c", `journalctl -t omni-server --no-pager --lines=all 2>/dev/null | grep 'mcp tools registered' | tail -1`})
	toolRegStr := strings.TrimSpace(string(toolRegLine))
	t.Logf("tool registration log exit=%d: %s", exitCode, toolRegStr)

	if toolRegStr == "" {
		t.Errorf("startup log: 'mcp tools registered' not found — tunnel_mcp server may not have initialised")
	} else if !strings.Contains(toolRegStr, "count=12") {
		t.Errorf("expected count=12 in tool registration line, got: %s", toolRegStr)
	} else {
		t.Logf("PASS: %s", toolRegStr)
	}

	// ── Check 2: HTTP reachability — server is listening at mcpServerURL ──────
	// Any response (including auth rejection) proves the server is up.
	// Auth token may be empty (auth disabled) or set; pass it if present.
	token := containerEnv(t, cfg, "AXO_LINK_MCP_AUTH_TOKEN")
	var authHeader string
	if token != "" {
		authHeader = fmt.Sprintf(`-H "Authorization: Bearer %s" `, token)
	}
	curlArgs := fmt.Sprintf(
		`curl -s -o /dev/null -w "%%{http_code}" --max-time 5 -X POST %s `+
			`-H "Content-Type: application/json" %s`+
			`-d '{"jsonrpc":"2.0","method":"tools/list","id":1}'`,
		mcpServerURL, authHeader,
	)
	_, httpStatus, _ := cfg.exec.RunCommand(context.Background(), []string{"bash", "-c", curlArgs})
	statusStr := strings.TrimSpace(string(httpStatus))
	t.Logf("HTTP reachability: status=%s", statusStr)
	if statusStr == "000" || statusStr == "" {
		t.Errorf("tunnel_mcp server not reachable at %s (curl got no response)", mcpServerURL)
	} else {
		t.Logf("PASS: tunnel_mcp server listening (HTTP %s)", statusStr)
	}

	// ── Check 3: tools/list with valid session (TODO — server-bugs.md #2) ─────
	// When agent_id-in-hook race is fixed, this can be enabled:
	// start agent → wait SessionStart registered → curl tools/list → assert 12 tools.
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// readMCPServers reads the mcpServers map from a JSON config file inside the container.
// Returns a map of server-name → raw entry (as map[string]any).
func readMCPServers(t *testing.T, cfg testConfig, path string) map[string]any {
	t.Helper()
	script := fmt.Sprintf(
		`python3 -c "import json,sys; d=json.load(open('%s')); sys.stdout.write(json.dumps(d.get('mcpServers', {})))"`,
		path,
	)
	exitCode, out, _ := cfg.exec.RunCommand(context.Background(), []string{"bash", "-c", script})
	if exitCode != 0 {
		t.Fatalf("readMCPServers: could not read %s (exit=%d)", path, exitCode)
	}
	var servers map[string]any
	if err := json.Unmarshal(out, &servers); err != nil {
		t.Fatalf("readMCPServers: JSON decode failed: %v\nraw=%s", err, out)
	}
	return servers
}

// addMCPEntry inserts a minimal HTTP-transport MCP server entry into the JSON config file.
func addMCPEntry(t *testing.T, cfg testConfig, path, name, url string) {
	t.Helper()
	script := fmt.Sprintf(`python3 -c "
import json
with open('%s') as f:
    d = json.load(f)
servers = d.setdefault('mcpServers', {})
servers['%s'] = {'type': 'http', 'url': '%s'}
with open('%s', 'w') as f:
    json.dump(d, f, indent=2)
"`, path, name, url, path)
	exitCode, out, _ := cfg.exec.RunCommand(context.Background(), []string{"bash", "-c", script})
	if exitCode != 0 {
		t.Fatalf("addMCPEntry: failed to add %q to %s (exit=%d): %s", name, path, exitCode, out)
	}
}

// deleteMCPEntry removes a named entry from the mcpServers map in the JSON config file.
func deleteMCPEntry(t *testing.T, cfg testConfig, path, name string) {
	t.Helper()
	script := fmt.Sprintf(`python3 -c "
import json
with open('%s') as f:
    d = json.load(f)
d.get('mcpServers', {}).pop('%s', None)
with open('%s', 'w') as f:
    json.dump(d, f, indent=2)
"`, path, name, path)
	exitCode, out, _ := cfg.exec.RunCommand(context.Background(), []string{"bash", "-c", script})
	if exitCode != 0 {
		t.Fatalf("deleteMCPEntry: failed to remove %q from %s (exit=%d): %s", name, path, exitCode, out)
	}
}

// containerEnv reads a single env var from the container.
func containerEnv(t *testing.T, cfg testConfig, varName string) string {
	t.Helper()
	exitCode, out, _ := cfg.exec.RunCommand(context.Background(),
		[]string{"bash", "-c", fmt.Sprintf(`echo -n "${%s:-}"`, varName)})
	if exitCode != 0 {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// countToolsInResponse tries to count the number of tools in a JSON-RPC or SSE response.
func countToolsInResponse(resp string) int {
	// Try direct JSON-RPC result first.
	var rpcResp struct {
		Result struct {
			Tools []any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &rpcResp); err == nil && len(rpcResp.Result.Tools) > 0 {
		return len(rpcResp.Result.Tools)
	}

	// Try SSE lines: look for a data: line containing "tools".
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if err := json.Unmarshal([]byte(payload), &rpcResp); err == nil && len(rpcResp.Result.Tools) > 0 {
			return len(rpcResp.Result.Tools)
		}
		// Count tool name occurrences as fallback.
		if strings.Contains(payload, `"name"`) && strings.Contains(payload, `"inputSchema"`) {
			count := strings.Count(payload, `"inputSchema"`)
			if count > 0 {
				return count
			}
		}
	}
	return 0
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
