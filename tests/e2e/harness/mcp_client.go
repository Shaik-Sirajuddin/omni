//go:build e2e

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

const defaultMCPServerURL = "http://127.0.0.1:18062/mcp"

// MCPClient wraps MCP tool calls with axolink-specific headers.
// When the test config uses a DockerExecutor, all calls are routed
// through docker exec + curl so they hit the container's own
// 127.0.0.1:18062 — zero host ports, zero collisions, CI-safe.
// Falls back to the mcp-go Go client for local (non-docker) mode.
type MCPClient struct {
	// go-client mode (local/host)
	cli *mcpclient.Client

	// docker-exec curl mode
	dockerEx  *DockerExecutor
	sessionID string
	callSeq   atomic.Int64 // unique jsonrpc id per call

	senderID  string
	workspace string
	t         *testing.T
}

// NewMCPClient dials the axolink MCP server and initializes an MCP session.
// When cfg.Exec is a *DockerExecutor the call is routed through docker exec
// curl so it always hits the container's own 127.0.0.1:18062.
func NewMCPClient(t *testing.T, cfg TestConfig, senderID string) *MCPClient {
	t.Helper()
	if dockerEx, ok := cfg.Exec.(*DockerExecutor); ok {
		return newDockerExecMCPClient(t, dockerEx, senderID, cfg.Workspace)
	}
	return newGoMCPClient(t, cfg, senderID)
}

// ─── docker-exec curl backend ─────────────────────────────────────────────────

func newDockerExecMCPClient(t *testing.T, ex *DockerExecutor, senderID, workspace string) *MCPClient {
	t.Helper()
	m := &MCPClient{
		dockerEx:  ex,
		senderID:  senderID,
		workspace: workspace,
		t:         t,
	}
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"1"}}}`
	cmd := m.curlCmd(true, "", initBody)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, out, err := ex.RunCommand(ctx, cmd)
	if err != nil {
		t.Skipf("MCP curl init: exec error: %v", err)
	}
	raw := string(out)
	for _, line := range strings.Split(raw, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "mcp-session-id:") {
			m.sessionID = strings.TrimSpace(line[strings.Index(line, ":")+1:])
			break
		}
	}
	if m.sessionID == "" {
		t.Skipf("MCP curl init: no Mcp-Session-Id in response:\n%s", raw)
	}
	return m
}

// curlCmd builds the curl argv for a MCP POST. withHeaders=true adds -i to
// include response headers in output (needed for initialize to get session ID).
func (m *MCPClient) curlCmd(withHeaders bool, sessionID, body string) []string {
	args := []string{"curl", "-s"}
	if withHeaders {
		args = append(args, "-i")
	}
	args = append(args,
		"-X", "POST", "http://127.0.0.1:18062/mcp",
		"-H", "Content-Type: application/json",
		"-H", "Accept: application/json",
		"-H", "X-SENDER-ID: "+m.senderID,
		"-H", "X-SENDER-TYPE: omni_agent",
		"-H", "X-AGENT-WORKSPACE: "+m.workspace,
	)
	if sessionID != "" {
		args = append(args, "-H", "Mcp-Session-Id: "+sessionID)
	}
	args = append(args, "-d", body)
	return args
}

func (m *MCPClient) curlCall(toolName string, args map[string]any) string {
	m.t.Helper()
	id := m.callSeq.Add(1) + 10 // start at 11 to avoid colliding with initialize id=1
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": args,
		},
	})
	if err != nil {
		m.t.Logf("MCPClient.curlCall(%s): marshal error: %v", toolName, err)
		return ""
	}
	cmd := m.curlCmd(false, m.sessionID, string(payload))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, out, execErr := m.dockerEx.RunCommand(ctx, cmd)
	if execErr != nil {
		m.t.Logf("MCPClient.curlCall(%s): exec error: %v", toolName, execErr)
		return ""
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		m.t.Logf("MCPClient.curlCall(%s): empty response", toolName)
		return ""
	}
	var resp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal([]byte(raw), &resp); jsonErr != nil {
		m.t.Logf("MCPClient.curlCall(%s): unmarshal error: %v — raw: %s", toolName, jsonErr, raw)
		return fmt.Sprintf("parse-error: %s", raw)
	}
	if resp.Error != nil {
		m.t.Logf("MCPClient.curlCall(%s): server error %d: %s", toolName, resp.Error.Code, resp.Error.Message)
		return fmt.Sprintf("server-error: %s", resp.Error.Message)
	}
	for _, c := range resp.Result.Content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}

// ─── mcp-go Go client backend (local mode) ───────────────────────────────────

func newGoMCPClient(t *testing.T, cfg TestConfig, senderID string) *MCPClient {
	t.Helper()
	serverURL := EnvOr("MCP_SERVER_URL", defaultMCPServerURL)
	headers := map[string]string{
		"X-SENDER-ID":       senderID,
		"X-SENDER-TYPE":     "omni_agent",
		"X-AGENT-WORKSPACE": cfg.Workspace,
	}
	cli, err := mcpclient.NewStreamableHttpClient(serverURL,
		transport.WithHTTPHeaders(headers),
		transport.WithHTTPTimeout(15*time.Second),
	)
	if err != nil {
		t.Skipf("MCP client: failed to create (%s): %v", serverURL, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cli.Start(ctx); err != nil {
		t.Skipf("MCP client: start failed: %v", err)
	}
	_, err = cli.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "e2e", Version: "1"},
		},
	})
	if err != nil {
		t.Skipf("MCP client: initialize failed: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return &MCPClient{cli: cli, senderID: senderID, workspace: cfg.Workspace, t: t}
}

// ─── public API ───────────────────────────────────────────────────────────────

// CallTool invokes a named MCP tool and returns the full JSON-encoded result.
func (m *MCPClient) CallTool(toolName string, args map[string]any) string {
	m.t.Helper()
	if m.dockerEx != nil {
		return m.curlCall(toolName, args)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := m.cli.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: toolName, Arguments: args},
	})
	if err != nil {
		m.t.Logf("MCPClient.CallTool(%s) error: %v", toolName, err)
		return ""
	}
	b, _ := json.Marshal(result)
	return string(b)
}

// CallToolText invokes a tool and returns the first text-content string.
func (m *MCPClient) CallToolText(toolName string, args map[string]any) string {
	m.t.Helper()
	if m.dockerEx != nil {
		return m.curlCall(toolName, args)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := m.cli.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: toolName, Arguments: args},
	})
	if err != nil {
		m.t.Logf("MCPClient.CallToolText(%s) error: %v", toolName, err)
		return ""
	}
	for _, c := range result.Content {
		if txt, ok := c.(mcp.TextContent); ok {
			return txt.Text
		}
	}
	return ""
}
