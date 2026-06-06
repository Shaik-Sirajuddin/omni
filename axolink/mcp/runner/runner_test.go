//go:build unit

package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfigDrivenBridgeHeaders(t *testing.T) {
	cfg := Config{
		AuthToken:      "token-from-config",
		SenderID:       "sender-from-config",
		SenderType:     "omni_agent",
		AgentWorkspace: "/workspace-from-config",
	}

	headers := bridgeHeaders(cfg)

	assert.Equal(t, "Bearer token-from-config", headers["Authorization"], "Auth header should use runner config")
	assert.Equal(t, "sender-from-config", headers["X-SENDER-ID"], "Sender id should use runner config")
	assert.Equal(t, "omni_agent", headers["X-SENDER-TYPE"], "Sender type should use runner config")
	assert.Equal(t, "/workspace-from-config", headers["X-AGENT-WORKSPACE"], "Workspace should use runner config")
}

func TestDefaultConfigUsesServiceHTTPEnv(t *testing.T) {
	t.Run("Service Unix Socket Env", func(t *testing.T) {
		t.Setenv("AXO_LINK_SERVICE_UNIX_SOCKET", "/tmp/service.sock")

		cfg := DefaultConfig()

		assert.Equal(t, "/tmp/service.sock", cfg.ServiceUnixSocket, "Service unix socket env should configure pure HTTP service socket")
	})

	t.Run("Service HTTP Bind Env", func(t *testing.T) {
		t.Setenv("AXO_LINK_SERVICE_HTTP_BIND", ServiceHTTPBindTCP)
		t.Setenv("AXO_LINK_SERVICE_ADDR", ":19061")

		cfg := DefaultConfig()

		assert.Equal(t, ServiceHTTPBindTCP, cfg.ServiceHTTPBind, "Service bind env should configure pure HTTP service bind")
		assert.Equal(t, ":19061", cfg.ServiceAddr, "Service addr env should configure pure HTTP service TCP address")
	})
}

func TestTransportAndEndpointMapping(t *testing.T) {
	t.Run("Default MCP Transport Is Streamable HTTP", func(t *testing.T) {
		cfg := DefaultConfig()

		assert.Equal(t, TransportStreamableHTTP, cfg.Transport, "Default transport should be streamable HTTP")
		assert.Equal(t, DefaultAddr, cfg.Addr, "Default MCP address should use MCP TCP port")
	})

	t.Run("Stdio Endpoint Uses TCP HTTP", func(t *testing.T) {
		got := mcpHTTPEndpoint(":18062", "/mcp")

		assert.Equal(t, "http://127.0.0.1:18062/mcp", got, "Stdio endpoint should target TCP MCP HTTP by default")
	})
}
