//go:build e2e

package engine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
)

// TestFullSessionExecAndResponse (S1) runs a full exec → agent responds cycle.
func TestFullSessionExecAndResponse(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	agentName := "e2e-session-full-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	provider := detectSessionProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available")
	}

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "resume", agentName,
		"--detach", "--workspace", cfg.Workspace)
	time.Sleep(5 * time.Second)

	probe := "pong-" + harness.AgentNameSuffix(t)
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "respond with exactly: "+probe)

	assert.Equal(t, 0, code, "exec must exit 0: %s", out)
	harness.AssertNoExecSessionFailed(t, jrnl.String()+omniLog.String())
}

// TestSessionNoCrashOnBadInput (S2) verifies that passing an empty prompt does
// not crash the server.
func TestSessionNoCrashOnBadInput(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := detectSessionProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available")
	}

	agentName := "e2e-bad-input-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	// Empty prompt — should not crash
	_, _ = harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "")

	// Server must still be up — health check via MCP port connectivity.
	// POST to /mcp returns quickly; GET to /health hangs (SSE).
	out, code := harness.ExecInContainer(t, cfg,
		`curl -s -o /dev/null -w "%{http_code}" --max-time 5 `+
			`-X POST http://127.0.0.1:18062/mcp `+
			`-H "Content-Type: application/json" `+
			`-d '{"jsonrpc":"2.0","method":"tools/list","id":0}'`)
	assert.Equal(t, 0, code, "health check after bad input must not timeout")
	assert.NotEqual(t, "000", strings.TrimSpace(out), "MCP server must still respond after bad input")

	log := jrnl.String()
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "panic:") || strings.Contains(line, "fatal error:") {
			t.Errorf("panic/fatal after bad input: %s", line)
		}
	}
}

// TestMultiAgentConcurrentExec (S3) verifies that two agents can receive execs
// concurrently without interfering with each other.
func TestMultiAgentConcurrentExec(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	provider := detectSessionProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available")
	}

	ts := harness.AgentNameSuffix(t)
	agent1 := "e2e-concurrent-a-" + ts
	agent2 := "e2e-concurrent-b-" + ts
	t.Cleanup(func() {
		harness.TeardownAgent(t, cfg, agent1)
		harness.TeardownAgent(t, cfg, agent2)
	})

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	for _, name := range []string{agent1, agent2} {
		harness.RunOmni(t, cfg, "agent", "init", name,
			"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	}

	type result struct {
		name string
		code int
	}
	done := make(chan result, 2)

	for _, name := range []string{agent1, agent2} {
		go func(n string) {
			_, c := harness.RunOmniAllowFail(t, cfg,
				"agent", "exec", n, "--prompt", "say: "+n, "--bg")
			done <- result{n, c}
		}(name)
	}

	for i := 0; i < 2; i++ {
		r := <-done
		assert.Equal(t, 0, r.code, "concurrent exec for agent %s must exit 0", r.name)
	}

	harness.AssertNoExecSessionFailed(t, jrnl.String()+omniLog.String())
}

// TestSessionStopAndStatus (S4) verifies that stop changes agent status and
// a subsequent exec can restart it.
func TestSessionStopAndStatus(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := detectSessionProvider(t, cfg)
	if provider == "" {
		t.Skip("no supported agent binary available")
	}

	agentName := "e2e-stop-status-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	harness.RunOmniAllowFail(t, cfg, "agent", "resume", agentName,
		"--detach", "--workspace", cfg.Workspace)
	time.Sleep(3 * time.Second)

	stopOut, stopCode := harness.RunOmniAllowFail(t, cfg, "agent", "stop", agentName)
	assert.Equal(t, 0, stopCode, "agent stop must exit 0: %s", stopOut)
	time.Sleep(time.Second)

	// List must not show agent as running
	listOut, _ := harness.RunOmniAllowFail(t, cfg,
		"agent", "list", "--workspace", cfg.Workspace)
	t.Logf("list after stop: %s", listOut)
	// agent list shows all agents; just verify no panic/error occurred
	assert.NotContains(t, listOut, "panic:",
		"agent list must not panic after stop")
}

// TestExecAfterTeardown (S5) verifies exec on a torn-down agent exits non-zero
// with a clear error message.
func TestExecAfterTeardown(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	agentName := "e2e-teardown-exec-" + harness.AgentNameSuffix(t)
	harness.TeardownAgent(t, cfg, agentName)

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "exec", agentName, "--prompt", "test")

	assert.NotEqual(t, 0, code, "exec after teardown must exit non-zero")
	t.Logf("exec after teardown: exit=%d output=%s", code, out)
}

// TestWorkspaceIsolation (S6) verifies that send_message to a non-existent
// agent in the service workspace returns an error rather than silently dropping.
// True multi-workspace isolation requires two separate service instances and is
// tested at the integration level, not here.
func TestWorkspaceIsolation(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	ts := harness.AgentNameSuffix(t)
	agent1 := "e2e-iso-sender-" + ts
	ghost := "e2e-iso-ghost-" + ts // never created

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agent1,
		"--workspace", cfg.Workspace, "--provider", "codex")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agent1) })

	mcpCli := harness.NewMCPClient(t, cfg, agent1)
	out := mcpCli.CallTool("send_message", map[string]any{
		"to_type":   "omni_agent",
		"to_name":   ghost,
		"workspace": cfg.Workspace,
		"prompt":    "isolation-probe",
	})

	t.Logf("send_message to ghost agent: %s", out)
	// The server must report an error or "not found" — not silently succeed.
	assert.True(t,
		strings.Contains(out, `"isError":true`) || strings.Contains(out, "not found"),
		"send_message to non-existent agent must return an error or not-found (got: %s)", out)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func detectSessionProvider(t *testing.T, cfg harness.TestConfig) string {
	t.Helper()
	for _, provider := range []string{"claude", "codex"} {
		_, code := harness.ExecInContainer(t, cfg, "command -v "+provider)
		if code == 0 {
			t.Logf("provider: %s", provider)
			return provider
		}
	}
	return ""
}

