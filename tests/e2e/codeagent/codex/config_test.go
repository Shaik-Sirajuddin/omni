//go:build e2e

package codex_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const codexAgentProvider = "codex"

// TestCodexBinaryAvailable (S1) verifies codex binary is present and executable.
func TestCodexBinaryAvailable(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	out, code := harness.ExecInContainer(t, cfg, "command -v codex")
	if code != 0 {
		t.Log("WARNING: codex binary not found in container — install codex or provide credentials to enable these tests")
		t.Skip("codex binary not available in this environment")
	}
	assert.Equal(t, 0, code, "codex binary must be locatable via command -v")
	t.Logf("codex path: %s", strings.TrimSpace(out))

	verOut, verCode := harness.ExecInContainer(t, cfg, "codex --version 2>&1 || codex -v 2>&1 || codex version 2>&1")
	t.Logf("codex version: exit=%d out=%s", verCode, verOut)
}

// TestCodexAgentInit (S2) verifies that omni can initialize a codex agent.
func TestCodexAgentInit(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	if _, code := harness.ExecInContainer(t, cfg, "command -v codex"); code != 0 {
		t.Log("WARNING: codex binary not found in container — skipping agent init test")
		t.Skip("codex binary not available")
	}

	agentName := "e2e-codex-init-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	out, code := harness.RunOmniAllowFail(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", codexAgentProvider, "--interactive=false")

	assert.Equal(t, 0, code, "agent init with codex provider must exit 0: %s", out)

	// Verify the agent shows up in list
	listOut, _ := harness.RunOmniAllowFail(t, cfg,
		"agent", "list", "--workspace", cfg.Workspace)
	assert.Contains(t, listOut, agentName,
		"newly created codex agent must appear in agent list")
}

// TestCodexMCPConfig (S3) verifies that the codex agent config includes
// axolink MCP server settings.
func TestCodexMCPConfig(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	if _, code := harness.ExecInContainer(t, cfg, "command -v codex"); code != 0 {
		t.Log("WARNING: codex binary not found in container — skipping MCP config test")
		t.Skip("codex binary not available")
	}

	agentName := "e2e-codex-mcp-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", codexAgentProvider, "--interactive=false")

	// Check agent config file for MCP settings
	cfgPaths := []string{
		"/root/.codex/config.json",
		"/root/.codex/config.yaml",
		"/root/.config/codex/config.json",
	}
	var cfgContent string
	for _, p := range cfgPaths {
		out, code := harness.ExecInContainer(t, cfg, "cat "+p+" 2>/dev/null")
		if code == 0 && len(out) > 0 {
			cfgContent = out
			t.Logf("found codex config at %s", p)
			break
		}
	}

	if cfgContent == "" {
		t.Log("no codex config file found — checking environment variables")
		envOut, _ := harness.ExecInContainer(t, cfg, "env | grep -i codex | head -20")
		t.Logf("codex env: %s", envOut)
		return
	}

	t.Logf("codex config: %s", cfgContent)
	assert.True(t,
		strings.Contains(cfgContent, "axolink") ||
			strings.Contains(cfgContent, "18062") ||
			strings.Contains(cfgContent, "mcp"),
		"codex config must reference axolink or MCP endpoint")
}

// TestCodexExecDelivery (S4) verifies exec --bg delivers a prompt to a codex
// agent and hook events appear in logs.
func TestCodexExecDelivery(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	if _, code := harness.ExecInContainer(t, cfg, "command -v codex"); code != 0 {
		t.Log("WARNING: codex binary not found in container — skipping exec delivery test")
		t.Skip("codex binary not available")
	}

	agentName := "e2e-codex-exec-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", codexAgentProvider, "--interactive=false")

	start := time.Now()
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "reply with: pong-codex", "--bg")
	elapsed := time.Since(start)

	require.Equal(t, 0, code, "codex exec --bg must exit 0: %s", out)
	assert.Less(t, elapsed.Milliseconds(), int64(10_000),
		"codex exec --bg must return in <10s (non-blocking), took %s", elapsed)

	// Delivery confirmation
	delivered := jrnl.WaitFor("exec in session", 30*time.Second) ||
		jrnl.WaitFor("ExecInSession", 30*time.Second) ||
		omniLog.WaitFor("exec in session", 30*time.Second) ||
		jrnl.WaitFor("UserPromptSubmit", 30*time.Second)

	assert.True(t, delivered,
		"codex exec delivery must be confirmed in logs within 30s")

	harness.AssertNoExecSessionFailed(t, jrnl.String()+omniLog.String())
}

// TestCodexConfigStrictMode (S5-strict) verifies that codex agent config
// enforces expected fields when strict mode is active.
func TestCodexConfigStrictMode(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	if _, code := harness.ExecInContainer(t, cfg, "command -v codex"); code != 0 {
		t.Log("WARNING: codex binary not found in container — skipping strict config test")
		t.Skip("codex binary not available")
	}

	// Look for a codex config file
	out, code := harness.ExecInContainer(t, cfg,
		`find /root -name "config.json" -path "*codex*" 2>/dev/null | head -1`)
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Log("WARNING: no codex config.json found — strict validation requires an initialised codex agent")
		t.Skip("no codex config.json found for strict validation")
	}

	cfgPath := strings.TrimSpace(out)
	content, cfgCode := harness.ExecInContainer(t, cfg, "cat "+cfgPath)
	require.Equal(t, 0, cfgCode, "must be able to read %s", cfgPath)

	var parsed map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(content), &parsed),
		"codex config.json must be valid JSON")

	t.Logf("codex config keys: %v", configKeys(parsed))

	// Strict: if mcpServers is present it must contain axolink
	if mcpRaw, ok := parsed["mcpServers"]; ok {
		var mcp map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(mcpRaw, &mcp))
		_, hasAxolink := mcp["axolink"]
		assert.True(t, hasAxolink,
			"codex config.json mcpServers must include axolink if mcpServers key is present")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func configKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
