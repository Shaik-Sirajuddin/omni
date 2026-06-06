//go:build e2e

package harness

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// MCPClient wraps the mark3labs/mcp-go client speaking to the axolink MCP server
// over its stdio bridge (`omni axolink`). Use NewMCPClient to construct; use
// CallTool to invoke tools.
type MCPClient struct {
	cli       *mcpclient.Client
	senderID  string
	workspace string
	t         *testing.T
}

// NewMCPClient starts the axolink stdio MCP bridge INSIDE the target container
// (`omni axolink` over `docker exec`) and initializes an MCP session.
//
// Using the stdio bridge — rather than an HTTP client to 127.0.0.1:18062 — means
// the request always reaches the *same container under test* via its own service
// socket, with no host port publishing and no risk of hitting a stray host
// omni-server that happens to own 18062. Sender identity is passed through the
// AXO_LINK_MCP_* env the bridge reads. Works identically for multi-worktree,
// Docker Desktop, and CI (it's just `docker exec`).
//
// The test is skipped (not failed) if the bridge cannot be started/initialized.
func NewMCPClient(t *testing.T, cfg TestConfig, senderID string) *MCPClient {
	t.Helper()

	// Mirror exactly what `omni agent resume` injects into a real agent session
	// so the bridge authenticates and identifies itself the same way. The token
	// is the operator's fixed dev token (operator/impl/default.go); override via
	// E2E_MCP_AUTH_TOKEN if the deployment uses a different one.
	senderEnv := []string{
		"AXO_LINK_MCP_SENDER_ID=" + senderID,
		"AXO_LINK_MCP_SENDER_TYPE=omni_agent",
		"AXO_LINK_MCP_AGENT_WORKSPACE=" + cfg.Workspace,
		"AXO_LINK_MCP_AUTH_TOKEN=" + EnvOr("E2E_MCP_AUTH_TOKEN", "axolink-dev-token"),
	}

	var (
		command string
		args    []string
		env     []string
	)
	switch cfg.Exec.(type) {
	case *DockerExecutor:
		// Run the bridge inside the container; pass sender identity via -e.
		command = "docker"
		args = []string{"exec", "-i"}
		for _, e := range senderEnv {
			args = append(args, "-e", e)
		}
		args = append(args, cfg.Container, cfg.OmniPath, "axolink")
	default:
		// Local target: run the bridge directly with sender env.
		command = cfg.OmniPath
		args = []string{"axolink"}
		env = append(os.Environ(), senderEnv...)
	}

	cli, err := mcpclient.NewStdioMCPClient(command, env, args...)
	if err != nil {
		t.Skipf("MCP client: failed to start axolink stdio bridge: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := cli.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "e2e", Version: "1"},
		},
	}); err != nil {
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
