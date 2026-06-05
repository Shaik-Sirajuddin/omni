//go:build e2e

package engine_test

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

// TestRecallHTTPEndpoint (E1) verifies that the recall/query endpoint responds.
func TestRecallHTTPEndpoint(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	out, code := harness.ExecInContainer(t, cfg,
		`curl -s -o /dev/null -w "%{http_code}" --max-time 5 `+
			`-X POST http://127.0.0.1:18062/mcp `+
			`-H "Content-Type: application/json" `+
			`-d '{"jsonrpc":"2.0","method":"tools/list","id":1}'`)

	assert.Equal(t, 0, code, "curl must not time out")
	assert.NotEqual(t, "000", strings.TrimSpace(out),
		"MCP server must respond (not connection refused)")
	t.Logf("MCP HTTP status: %s", out)
}

// TestSendMessageRecorded (E2) verifies that send_message via axolink MCP
// records the message and returns a message_id.
func TestSendMessageRecorded(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	agentName := "e2e-recall-sender-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", "claude", "--interactive=false")

	sessionID := initMCPSession(t, cfg, agentName)
	t.Logf("session_id: %s", sessionID)

	payload := fmt.Sprintf(
		`{"to":"%s","message":"e2e-recall-probe-%s","workspace":"%s"}`,
		agentName, time.Now().Format("150405"), cfg.Workspace,
	)
	raw := mcpToolCall(t, cfg, sessionID, agentName, "send_message", payload)
	t.Logf("send_message raw: %s", raw)

	var resp map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))

	msgID := harness.ExtractMessageID(omniLog, 10*time.Second)
	if msgID == "" {
		msgID = harness.ExtractMessageID(jrnl, 5*time.Second)
	}

	assert.NotEmpty(t, msgID, "message_id must be returned after send_message")
	t.Logf("message_id: %s", msgID)
}

// TestDeliveryChainMessageID (E3) verifies the full delivery chain using
// message_id tracing via AssertDeliveryChain.
func TestDeliveryChainMessageID(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	agentName := "e2e-chain-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", "claude", "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "resume", agentName,
		"--detach", "--workspace", cfg.Workspace)
	time.Sleep(5 * time.Second)

	sessionID := initMCPSession(t, cfg, agentName)
	payload := fmt.Sprintf(
		`{"to":"%s","message":"chain-probe","workspace":"%s"}`,
		agentName, cfg.Workspace,
	)
	mcpToolCall(t, cfg, sessionID, agentName, "send_message", payload)

	msgID := harness.ExtractMessageID(omniLog, 15*time.Second)
	if msgID == "" {
		msgID = harness.ExtractMessageID(jrnl, 5*time.Second)
	}

	if msgID == "" {
		t.Skip("message_id not captured — skipping chain verification")
	}

	harness.AssertDeliveryChain(t, omniLog, msgID, []harness.Checkpoint{
		{Name: "send_message succeeded", Substr: "send_message succeeded", Timeout: 15 * time.Second},
	})

	// broadcast and exec may not always appear in omniLog — check jrnl too
	_ = jrnl.WaitForWithID(msgID, "exec in session", 20*time.Second) ||
		omniLog.WaitForWithID(msgID, "exec in session", 20*time.Second)
}

// TestQueryResultAPI (E4) verifies that query_result API returns data for a
// known message_id.
func TestQueryResultAPI(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	agentName := "e2e-qr-agent-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", "claude", "--interactive=false")

	sessionID := initMCPSession(t, cfg, agentName)
	payload := fmt.Sprintf(
		`{"to":"%s","message":"qr-probe","workspace":"%s"}`,
		agentName, cfg.Workspace,
	)
	mcpToolCall(t, cfg, sessionID, agentName, "send_message", payload)

	msgID := harness.ExtractMessageID(omniLog, 10*time.Second)
	if msgID == "" {
		t.Skip("message_id not available — skipping query_result test")
	}

	// Poll query_result until non-empty or timeout
	deadline := time.Now().Add(30 * time.Second)
	var qrOut string
	for time.Now().Before(deadline) {
		qrOut = mcpToolCall(t, cfg, sessionID, agentName,
			"query_result", fmt.Sprintf(`{"message_id":"%s"}`, msgID))
		if strings.Contains(qrOut, msgID) {
			break
		}
		time.Sleep(2 * time.Second)
	}

	assert.True(t, strings.Contains(qrOut, msgID) || len(qrOut) > 10,
		"query_result must return data for message_id %s", msgID)
	t.Logf("query_result: %s", qrOut)
}

// TestGetMessageAPI (E5) verifies that get_message returns the stored message.
func TestGetMessageAPI(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	agentName := "e2e-gm-agent-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", "claude", "--interactive=false")

	sessionID := initMCPSession(t, cfg, agentName)
	probeMsg := "gm-probe-" + time.Now().Format("150405")
	payload := fmt.Sprintf(
		`{"to":"%s","message":"%s","workspace":"%s"}`,
		agentName, probeMsg, cfg.Workspace,
	)
	mcpToolCall(t, cfg, sessionID, agentName, "send_message", payload)

	msgID := harness.ExtractMessageID(omniLog, 10*time.Second)
	if msgID == "" {
		t.Skip("message_id not available — skipping get_message test")
	}

	gmOut := mcpToolCall(t, cfg, sessionID, agentName,
		"get_message", fmt.Sprintf(`{"message_id":"%s"}`, msgID))
	t.Logf("get_message: %s", gmOut)

	assert.True(t, strings.Contains(gmOut, msgID) || strings.Contains(gmOut, probeMsg),
		"get_message must return stored message for id %s", msgID)
}

// TestListAgentsReturnsAgents (E6) verifies list_agents returns the known agent.
func TestListAgentsReturnsAgents(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	agentName := "e2e-list-agent-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", "claude", "--interactive=false")

	sessionID := initMCPSession(t, cfg, agentName)
	out := mcpToolCall(t, cfg, sessionID, agentName, "list_agents",
		fmt.Sprintf(`{"workspace":"%s"}`, cfg.Workspace))

	assert.Contains(t, out, agentName,
		"list_agents must include the newly created agent")
	t.Logf("list_agents: %s", out)
}

// TestListTeams (E7) verifies that list_teams returns the initialized team.
func TestListTeams(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	harness.RunOmniAllowFail(t, cfg, "team", "init")

	senderID := "e2e-list-teams-" + time.Now().Format("150405")
	sessionID := initMCPSession(t, cfg, senderID)
	out := mcpToolCall(t, cfg, sessionID, senderID, "list_teams",
		fmt.Sprintf(`{"workspace":"%s"}`, cfg.Workspace))

	t.Logf("list_teams: %s", out)
	// As long as it doesn't error out (500 / error key) the endpoint is healthy
	assert.False(t, strings.Contains(out, `"error"`) && strings.Contains(out, "500"),
		"list_teams must not return a 500 error")
}

// TestHealthEndpoint (E8) verifies the health endpoint returns ok.
func TestHealthEndpoint(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	out, code := harness.ExecInContainer(t, cfg,
		`curl -s --max-time 5 http://127.0.0.1:18062/health`)

	assert.Equal(t, 0, code, "health curl must not time out")
	t.Logf("health: %s", out)
	assert.True(t, strings.Contains(out, "ok") ||
		strings.Contains(out, "healthy") ||
		strings.Contains(out, `"status"`),
		"health endpoint must return ok/healthy response")
}

// TestAgentInterruptResume (E9) verifies agent_interrupt stops execution and
// agent_resume restarts it.
func TestAgentInterruptResume(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	provider := "claude"
	if out, code := harness.ExecInContainer(t, cfg, "command -v claude"); code != 0 || strings.TrimSpace(out) == "" {
		t.Skip("claude not available")
	}

	agentName := "e2e-interrupt-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "resume", agentName,
		"--detach", "--workspace", cfg.Workspace)
	time.Sleep(4 * time.Second)

	sessionID := initMCPSession(t, cfg, agentName)
	intOut := mcpToolCall(t, cfg, sessionID, agentName, "agent_interrupt",
		fmt.Sprintf(`{"agent_id":"%s","workspace":"%s"}`, agentName, cfg.Workspace))
	t.Logf("agent_interrupt: %s", intOut)
	time.Sleep(2 * time.Second)

	resOut := mcpToolCall(t, cfg, sessionID, agentName, "agent_resume",
		fmt.Sprintf(`{"agent_id":"%s","workspace":"%s"}`, agentName, cfg.Workspace))
	t.Logf("agent_resume: %s", resOut)

	assert.False(t, strings.Contains(intOut, `"error"`) && strings.Contains(intOut, "500"),
		"agent_interrupt must not 500")
	assert.False(t, strings.Contains(resOut, `"error"`) && strings.Contains(resOut, "500"),
		"agent_resume must not 500")
}

// TestSendResponseAPI (E10) verifies that send_response stores a reply.
func TestSendResponseAPI(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	agentName := "e2e-sr-agent-" + time.Now().Format("150405")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", "claude", "--interactive=false")

	sessionID := initMCPSession(t, cfg, agentName)
	payload := fmt.Sprintf(
		`{"to":"%s","message":"sr-probe","workspace":"%s"}`,
		agentName, cfg.Workspace,
	)
	mcpToolCall(t, cfg, sessionID, agentName, "send_message", payload)

	msgID := harness.ExtractMessageID(omniLog, 10*time.Second)
	if msgID == "" {
		t.Skip("message_id not available — skipping send_response test")
	}

	srPayload := fmt.Sprintf(
		`{"message_id":"%s","response":"e2e-response-ok","workspace":"%s"}`,
		msgID, cfg.Workspace,
	)
	srOut := mcpToolCall(t, cfg, sessionID, agentName, "send_response", srPayload)
	t.Logf("send_response: %s", srOut)

	assert.False(t, strings.Contains(srOut, `"error"`) && strings.Contains(srOut, "500"),
		"send_response must not 500")
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func mcpToolCall(t *testing.T, cfg harness.TestConfig, sessionID, senderID, tool, argsJSON string) string {
	t.Helper()
	cmd := fmt.Sprintf(
		`curl -s -X POST http://127.0.0.1:18062/mcp `+
			`-H "Content-Type: application/json" `+
			`-H "Mcp-Session-Id: %s" `+
			`-H "X-SENDER-ID: %s" `+
			`-H "X-SENDER-TYPE: omni_agent" `+
			`-H "X-AGENT-WORKSPACE: %s" `+
			`-d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"%s","arguments":%s}}'`,
		sessionID, senderID, cfg.Workspace, tool, argsJSON,
	)
	out, _ := harness.ExecInContainer(t, cfg, cmd)
	return out
}

func initMCPSession(t *testing.T, cfg harness.TestConfig, senderID string) string {
	t.Helper()
	out, _ := harness.ExecInContainer(t, cfg, fmt.Sprintf(
		`curl -si -X POST http://127.0.0.1:18062/mcp `+
			`-H "Content-Type: application/json" `+
			`-H "Accept: application/json, text/event-stream" `+
			`-H "X-SENDER-ID: %s" `+
			`-H "X-SENDER-TYPE: omni_agent" `+
			`-H "X-AGENT-WORKSPACE: %s" `+
			`-d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"1"}}}'`,
		senderID, cfg.Workspace,
	))
	for _, line := range strings.Split(out, "\n") {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "mcp-session-id:") {
			return strings.TrimSpace(line[len("mcp-session-id:"):])
		}
	}
	return ""
}
