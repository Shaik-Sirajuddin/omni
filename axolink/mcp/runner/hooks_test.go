//go:build unit

package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHookProvisioning(t *testing.T) {
	t.Run("Default Hook URL Uses Service Unix Socket", func(t *testing.T) {
		cfg := Config{
			ServiceHTTPBind:   ServiceHTTPBindUnix,
			ServiceUnixSocket: "/tmp/tunnel-service-test.sock",
		}

		got := defaultHookURL(cfg)

		assert.Equal(t, "unix:///tmp/tunnel-service-test.sock/hook", got, "Default hook URL should use service unix HTTP socket format")
	})

	t.Run("Service TCP Hook URL Uses Localhost For Port Only Addr", func(t *testing.T) {
		cfg := Config{
			ServiceHTTPBind: ServiceHTTPBindTCP,
			ServiceAddr:     ":18061",
		}

		got := defaultHookURL(cfg)

		assert.Equal(t, "http://127.0.0.1:18061/hook", got, "Service TCP hook URL should use localhost for port-only listen address")
	})

	t.Run("Provision Hooks Is Idempotent", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		resolver := &config.DefaultOmniConfigResolver{ConfigPath: configPath}
		cfg := Config{
			ServiceHTTPBind:   ServiceHTTPBindUnix,
			ServiceUnixSocket: "/tmp/tunnel-service-test.sock",
		}

		require.NoError(t, provisionDefaultHooksWithResolver(cfg, resolver), "First hook provision should succeed")
		require.NoError(t, provisionDefaultHooksWithResolver(cfg, resolver), "Second hook provision should be idempotent")

		data, err := os.ReadFile(configPath)
		require.NoError(t, err, "Provisioned config should be readable")
		var got config.OmniConfig
		require.NoError(t, json.Unmarshal(data, &got), "Provisioned config should decode")
		require.NotNil(t, got.Agent, "Provisioned config should contain agent settings")

		expectedURL := "unix:///tmp/tunnel-service-test.sock/hook"
		for _, event := range []hooks.HookID{
			hooks.PreSessionStart,
			hooks.PrePrompt,
			hooks.PostPrompt,
			hooks.PostToolUseFailure,
		} {
			entries := got.Agent.Hooks[string(event)]
			require.Len(t, entries, 1, "Provisioned event should have exactly one hook entry")
			require.NotNil(t, entries[0].Url, "Provisioned hook entry should contain URL")
			assert.Equal(t, expectedURL, *entries[0].Url, "Provisioned hook URL should match service HTTP hook endpoint")
		}
	})
}
