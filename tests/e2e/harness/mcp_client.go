//go:build e2e

package harness

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

const defaultMCPServerURL = "http://127.0.0.1:18062/mcp"

// MCPClient wraps the mark3labs/mcp-go client with axolink-specific headers.
// Use NewMCPClient to construct; use CallTool to invoke tools.
type MCPClient struct {
	cli       *mcpclient.Client
	senderID  string
	workspace string
	t         *testing.T
}

// NewMCPClient dials the axolink MCP server and initializes an MCP session.
// senderID is sent as X-SENDER-ID; it identifies the calling agent.
// The test is skipped (not failed) if the server is unreachable.
func NewMCPClient(t *testing.T, cfg TestConfig, senderID string) *MCPClient {
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

// CallTool invokes a named MCP tool with the given arguments and returns the
// full JSON-encoded CallToolResult as a string. On transport error the test is
// logged but not failed — the caller inspects the returned string.
func (m *MCPClient) CallTool(toolName string, args map[string]any) string {
	m.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := m.cli.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: args,
		},
	})
	if err != nil {
		m.t.Logf("MCPClient.CallTool(%s) error: %v", toolName, err)
		return ""
	}
	b, _ := json.Marshal(result)
	return string(b)
}

// CallToolText invokes a tool and returns the first text-content string from
// the response. This is the unwrapped payload (the inner JSON) rather than the
// full MCP envelope, making it easier to assert on specific fields.
// Returns "" on transport error or if no text content is present.
func (m *MCPClient) CallToolText(toolName string, args map[string]any) string {
	m.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := m.cli.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: args,
		},
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
