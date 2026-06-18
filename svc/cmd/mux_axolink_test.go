package main

import (
	"testing"

	omniconfig "github.com/Shaik-Sirajuddin/memory/config"
)

func TestResolveAxolinkMCPEnabled(t *testing.T) {
	featureOn := &omniconfig.OmniConfig{
		Features: &omniconfig.Features{AxolinkMCP: true},
	}
	featureOff := &omniconfig.OmniConfig{
		Features: &omniconfig.Features{AxolinkMCP: false},
	}

	tests := []struct {
		name       string
		cliDisable bool
		cfg        *omniconfig.OmniConfig
		want       bool
	}{
		{
			name:       "neither flag nor config → enabled",
			cliDisable: false,
			cfg:        nil,
			want:       true,
		},
		{
			name:       "CLI flag disable=true → disabled regardless of nil config",
			cliDisable: true,
			cfg:        nil,
			want:       false,
		},
		{
			name:       "CLI flag disable=true → disabled even when config says true",
			cliDisable: true,
			cfg:        featureOn,
			want:       false,
		},
		{
			name:       "config AxolinkMCP=false → disabled",
			cliDisable: false,
			cfg:        featureOff,
			want:       false,
		},
		{
			name:       "config AxolinkMCP=true + CLI flag not set → enabled",
			cliDisable: false,
			cfg:        featureOn,
			want:       true,
		},
		{
			name:       "config AxolinkMCP=true + CLI flag disable=true → disabled (CLI wins)",
			cliDisable: true,
			cfg:        featureOn,
			want:       false,
		},
		{
			name:       "nil cfg.Features → defaults to enabled",
			cliDisable: false,
			cfg:        &omniconfig.OmniConfig{Features: nil},
			want:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAxolinkMCPEnabled(tc.cliDisable, tc.cfg)
			if got != tc.want {
				t.Errorf("resolveAxolinkMCPEnabled(cliDisable=%v, cfg) = %v, want %v",
					tc.cliDisable, got, tc.want)
			}
		})
	}
}
