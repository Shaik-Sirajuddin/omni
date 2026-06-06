//go:build unit

package main

import (
	"strings"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/mcp/mcp/runner"
	"github.com/stretchr/testify/assert"
)

func TestConfigHelpers(t *testing.T) {
	t.Run("Default Service Unix Socket Path Is Deterministic", func(t *testing.T) {
		path := runner.DefaultServiceUnixSocketPath()

		assert.True(t, strings.HasPrefix(path, "/run/omni-"), "Default service socket path should use /run/omni-<user>")
		assert.True(t, strings.HasSuffix(path, "service.sock"), "Default service socket path should end with service.sock")
	})

	t.Run("Default Runner Config Uses Streamable MCP", func(t *testing.T) {
		cfg := runner.DefaultConfig()

		assert.Equal(t, runner.TransportStreamableHTTP, cfg.Transport, "Default MCP transport should be streamable HTTP")
		assert.Equal(t, runner.ServiceHTTPBindUnix, cfg.ServiceHTTPBind, "Default service HTTP bind should use unix")
		assert.Equal(t, runner.DefaultHTTPPath, cfg.HTTPPath, "Default HTTP path should expose MCP streamable HTTP")
	})
}
