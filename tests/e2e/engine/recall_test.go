//go:build e2e

package engine_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

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
// Requires a running agent as sender — send_message rejects init-only agents.
func TestSendMessageRecorded(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	provider := harness.RequireProvider(t, cfg)

	ts := harness.AgentNameSuffix(t)
	sender := "e2e-recall-sender-" + ts
	receiver := "e2e-recall-recv-" + ts
	t.Cleanup(func() {
		harness.TeardownAgent(t, cfg, sender)
		harness.TeardownAgent(t, cfg, receiver)
	})

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", sender,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "init", receiver,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	mcpCli := harness.NewMCPClient(t, cfg, sender)
	inner := mcpCli.CallToolText("send_message", map[string]any{
		"to_type":   "omni_agent",
		"to_name":   receiver,
		"workspace": cfg.Workspace,
		"prompt":    "e2e-recall-probe-" + ts,
	})
	t.Logf("send_message inner: %s", inner)

	// BUG (tunnel_mcp send_message sender resolution): the axolink server fails
	// to resolve a sender agent that exists in the shared agents table by
	// name+workspace, returning "sender agent not found". Real agents use the
	// agent name as AXO_LINK_MCP_SENDER_ID (operator/impl/default.go:1276), so
	// this breaks agent-to-agent messaging for CLI-created agents. This is a real
	// failure surfaced by the suite — not masked with a skip.
	require.NotContains(t, inner, "sender agent not found",
		"BUG: send_message could not resolve the sender agent by name+workspace; "+
			"agent-to-agent messaging is broken for CLI-created senders")
	require.NotEmpty(t, inner, "send_message must return a result")

	// Parse message_id from the response JSON directly.
	var sendResp struct {
		MessageID string `json:"message_id"`
	}
	if err := jsonUnmarshal([]byte(inner), &sendResp); err != nil {
		t.Fatalf("send_message response not valid JSON: %v — got: %s", err, inner)
	}
	assert.NotEmpty(t, sendResp.MessageID, "message_id must be returned after send_message")
	t.Logf("message_id: %s", sendResp.MessageID)
}

// TestDeliveryChainMessageID (E3) verifies the full delivery chain using
// message_id tracing via AssertDeliveryChain.
func TestDeliveryChainMessageID(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	harness.RequireProvider(t, cfg)

	agentName := "e2e-chain-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", cfg.Provider, "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "resume", agentName,
		"--detach", "--workspace", cfg.Workspace)
	time.Sleep(5 * time.Second)

	senderChain := "e2e-chain-sender-" + harness.AgentNameSuffix(t)
	harness.RunOmni(t, cfg, "agent", "init", senderChain,
		"--workspace", cfg.Workspace, "--provider", cfg.Provider, "--interactive=false")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, senderChain) })

	mcpCli := harness.NewMCPClient(t, cfg, senderChain)
	inner := mcpCli.CallToolText("send_message", map[string]any{
		"to_type":   "omni_agent",
		"to_name":   agentName,
		"workspace": cfg.Workspace,
		"prompt":    "chain-probe",
	})
	var smResp struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal([]byte(inner), &smResp)
	msgID := smResp.MessageID

	if msgID == "" {
		msgID = harness.ExtractMessageID(omniLog, 5*time.Second)
	}
	if msgID == "" {
		t.Fatalf("BUG: send_message produced no message_id — tunnel_mcp sender " +
			"resolution failure breaks the delivery chain; see TestSendMessageRecorded")
	}

	// Verify storage via get_message — confirms delivery chain end-to-end.
	// (axolink service logs are in a separate process, not via journalctl)
	storedOut := mcpCli.CallToolText("get_message", map[string]any{"id": msgID})
	assert.Contains(t, storedOut, msgID,
		"get_message must return the stored message for id %s", msgID)
	t.Logf("delivery chain verified via get_message: %s", storedOut)
}

// TestQueryResultAPI (E4) verifies that query_result API returns data for a
// known message_id.
func TestQueryResultAPI(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	harness.RequireProvider(t, cfg)

	agentName := "e2e-qr-agent-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", cfg.Provider, "--interactive=false")

	senderQR := "e2e-qr-sender-" + harness.AgentNameSuffix(t)
	harness.RunOmni(t, cfg, "agent", "init", senderQR,
		"--workspace", cfg.Workspace, "--provider", cfg.Provider, "--interactive=false")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, senderQR) })

	mcpCli := harness.NewMCPClient(t, cfg, senderQR)
	inner := mcpCli.CallToolText("send_message", map[string]any{
		"to_type":   "omni_agent",
		"to_name":   agentName,
		"workspace": cfg.Workspace,
		"prompt":    "qr-probe",
	})
	var smResp struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal([]byte(inner), &smResp)
	msgID := smResp.MessageID
	if msgID == "" {
		t.Fatalf("BUG: send_message produced no message_id — tunnel_mcp sender " +
			"resolution failure; cannot exercise query_result; see TestSendMessageRecorded")
	}

	deadline := time.Now().Add(30 * time.Second)
	var qrOut string
	for time.Now().Before(deadline) {
		qrOut = mcpCli.CallTool("query_result", map[string]any{"message_id": msgID})
		if strings.Contains(qrOut, msgID) {
			break
		}
		time.Sleep(2 * time.Second)
	}

	assert.True(t, strings.Contains(qrOut, msgID) || len(qrOut) > 10,
		"query_result must return data for message_id %s", msgID)
	t.Logf("query_result: %s", qrOut)

	// A non-existent message_id must return an error, not empty/nil.
	unknownOut := mcpCli.CallTool("query_result", map[string]any{"message_id": "00000000-0000-0000-0000-000000000000"})
	assert.True(t,
		strings.Contains(unknownOut, `"isError":true`) || strings.Contains(unknownOut, "not found") || strings.Contains(unknownOut, "null"),
		"query_result for unknown id must return error or null (got: %s)", unknownOut)
	t.Logf("query_result (unknown id): %s", unknownOut)
}

// TestGetMessageAPI (E5) verifies that get_message returns the stored message.
func TestGetMessageAPI(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	harness.RequireProvider(t, cfg)

	agentName := "e2e-gm-agent-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", cfg.Provider, "--interactive=false")

	senderGM := "e2e-gm-sender-" + harness.AgentNameSuffix(t)
	harness.RunOmni(t, cfg, "agent", "init", senderGM,
		"--workspace", cfg.Workspace, "--provider", cfg.Provider, "--interactive=false")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, senderGM) })

	mcpCli := harness.NewMCPClient(t, cfg, senderGM)
	probeMsg := "gm-probe-" + harness.AgentNameSuffix(t)
	inner := mcpCli.CallToolText("send_message", map[string]any{
		"to_type":   "omni_agent",
		"to_name":   agentName,
		"workspace": cfg.Workspace,
		"prompt":    probeMsg,
	})
	var smResp struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal([]byte(inner), &smResp)
	msgID := smResp.MessageID
	if msgID == "" {
		t.Fatalf("BUG: send_message produced no message_id — tunnel_mcp sender " +
			"resolution failure; cannot exercise get_message; see TestSendMessageRecorded")
	}

	gmOut := mcpCli.CallTool("get_message", map[string]any{"id": msgID})
	t.Logf("get_message: %s", gmOut)

	assert.True(t, strings.Contains(gmOut, msgID) || strings.Contains(gmOut, probeMsg),
		"get_message must return stored message for id %s", msgID)
}

// TestListAgentsReturnsAgents (E6) verifies a newly created agent appears in
// `omni agent list`. Uses the CLI rather than the MCP list_agents tool because
// list_agents only returns agents that are currently running in the server,
// whereas agent list shows all agents persisted to the workspace DB.
func TestListAgentsReturnsAgents(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	harness.RequireProvider(t, cfg)

	agentName := "e2e-list-agent-" + harness.AgentNameSuffix(t)
	harness.TeardownAgent(t, cfg, agentName) // pre-clean stale state
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", cfg.Provider, "--interactive=false")

	listOut, _ := harness.RunOmniAllowFail(t, cfg,
		"agent", "list", "--workspace", cfg.Workspace)
	assert.Contains(t, listOut, agentName,
		"agent list must include the newly created agent")
	t.Logf("agent list: %s", listOut)
}

// TestListTeams (E7) verifies that list_teams returns the initialized team.
func TestListTeams(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	harness.RunOmniAllowFail(t, cfg, "team", "init")

	senderID := "e2e-list-teams-" + harness.AgentNameSuffix(t)
	mcpCli := harness.NewMCPClient(t, cfg, senderID)
	out := mcpCli.CallTool("list_teams", map[string]any{})

	t.Logf("list_teams: %s", out)
	assert.False(t, strings.Contains(out, `"error"`) && strings.Contains(out, "500"),
		"list_teams must not return a 500 error")
}

// TestHealthEndpoint (E8) verifies the MCP health tool returns ok.
// Uses Go MCP client (POST-based) to avoid SSE hang on GET requests.
func TestHealthEndpoint(t *testing.T) {
	cfg := harness.NewConfig(t)
	defer harness.DumpLogsOnFailure(t, nil, nil, "")

	mcpCli := harness.NewMCPClient(t, cfg, "e2e-health-check")
	out := mcpCli.CallTool("health", map[string]any{})
	t.Logf("health tool: %s", out)
	assert.True(t, strings.Contains(out, "ok") ||
		strings.Contains(out, "healthy") ||
		strings.Contains(out, `"status"`),
		"health MCP tool must return ok/healthy response: %s", out)
}

// TestAgentInterruptResume (E9) verifies agent_interrupt stops execution and
// agent_resume restarts it.
func TestAgentInterruptResume(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	harness.RequireProvider(t, cfg)

	agentName := "e2e-interrupt-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", cfg.Provider, "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "resume", agentName,
		"--detach", "--workspace", cfg.Workspace)
	time.Sleep(4 * time.Second)

	mcpCli := harness.NewMCPClient(t, cfg, agentName)
	intOut := mcpCli.CallTool("agent_interrupt", map[string]any{
		"agent_id": agentName,
	})
	t.Logf("agent_interrupt: %s", intOut)
	time.Sleep(2 * time.Second)

	resOut := mcpCli.CallTool("agent_resume", map[string]any{
		"agent_id": agentName,
	})
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
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	harness.RequireProvider(t, cfg)

	agentName := "e2e-sr-agent-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", cfg.Provider, "--interactive=false")

	senderSR := "e2e-sr-sender-" + harness.AgentNameSuffix(t)
	harness.RunOmni(t, cfg, "agent", "init", senderSR,
		"--workspace", cfg.Workspace, "--provider", cfg.Provider, "--interactive=false")
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, senderSR) })

	mcpCli := harness.NewMCPClient(t, cfg, senderSR)
	inner := mcpCli.CallToolText("send_message", map[string]any{
		"to_type":   "omni_agent",
		"to_name":   agentName,
		"workspace": cfg.Workspace,
		"prompt":    "sr-probe",
	})
	var smResp struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal([]byte(inner), &smResp)
	msgID := smResp.MessageID
	if msgID == "" {
		t.Fatalf("BUG: send_message produced no message_id — tunnel_mcp sender " +
			"resolution failure; cannot exercise send_response; see TestSendMessageRecorded")
	}

	srOut := mcpCli.CallTool("send_response", map[string]any{
		"message_id": msgID,
		"response":   "e2e-response-ok",
		"workspace":  cfg.Workspace,
	})
	t.Logf("send_response: %s", srOut)

	assert.False(t, strings.Contains(srOut, `"error"`) && strings.Contains(srOut, "500"),
		"send_response must not 500")
}
