package config

import (
	"encoding/json"
	"testing"
)

// TestAxolinkMCPDefaultsToTrue verifies that AxolinkMCP is true when Features
// is nil or zero-valued, after applying defaults.
func TestAxolinkMCPDefaultsToTrue(t *testing.T) {
	t.Run("nil config produces default with AxolinkMCP=true", func(t *testing.T) {
		cfg := ApplyOmniConfigDefaults(nil)
		if cfg.Features == nil {
			t.Fatal("expected Features to be non-nil")
		}
		if !cfg.Features.AxolinkMCP {
			t.Error("expected AxolinkMCP to default to true, got false")
		}
	})

	t.Run("nil Features produces default with AxolinkMCP=true", func(t *testing.T) {
		cfg := ApplyOmniConfigDefaults(&OmniConfig{Features: nil})
		if cfg.Features == nil {
			t.Fatal("expected Features to be non-nil")
		}
		if !cfg.Features.AxolinkMCP {
			t.Error("expected AxolinkMCP to default to true, got false")
		}
	})

	t.Run("ProvisionDefaultOmniConfig sets AxolinkMCP=true", func(t *testing.T) {
		cfg := ProvisionDefaultOmniConfig()
		if cfg.Features == nil {
			t.Fatal("expected Features to be non-nil")
		}
		if !cfg.Features.AxolinkMCP {
			t.Error("expected AxolinkMCP to default to true in provisioned config, got false")
		}
	})
}

// TestAxolinkMCPFalsePreserved verifies that explicitly setting AxolinkMCP=false
// is respected and not overwritten by ApplyOmniConfigDefaults.
func TestAxolinkMCPFalsePreserved(t *testing.T) {
	cfg := ApplyOmniConfigDefaults(&OmniConfig{
		Features: &Features{
			AxolinkMCP: false,
		},
	})
	if cfg.Features == nil {
		t.Fatal("expected Features to be non-nil")
	}
	if cfg.Features.AxolinkMCP {
		t.Error("expected AxolinkMCP=false to be preserved after applying defaults, got true")
	}
}

// TestAxolinkMCPJSONRoundTrip verifies that {"axolink_mcp": false} unmarshals
// correctly and that the field survives a marshal/unmarshal cycle.
func TestAxolinkMCPJSONRoundTrip(t *testing.T) {
	t.Run("unmarshal axolink_mcp=false", func(t *testing.T) {
		raw := `{"axolink_mcp": false}`
		var f Features
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if f.AxolinkMCP {
			t.Error("expected AxolinkMCP=false after unmarshal, got true")
		}
	})

	t.Run("marshal then unmarshal roundtrip", func(t *testing.T) {
		original := Features{
			AutoSync:   true,
			AxolinkMCP: false,
		}
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		var result Features
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if result.AxolinkMCP != original.AxolinkMCP {
			t.Errorf("roundtrip: expected AxolinkMCP=%v, got %v", original.AxolinkMCP, result.AxolinkMCP)
		}
		if result.AutoSync != original.AutoSync {
			t.Errorf("roundtrip: expected AutoSync=%v, got %v", original.AutoSync, result.AutoSync)
		}
	})

	t.Run("unmarshal axolink_mcp=true", func(t *testing.T) {
		raw := `{"axolink_mcp": true}`
		var f Features
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if !f.AxolinkMCP {
			t.Error("expected AxolinkMCP=true after unmarshal, got false")
		}
	})
}
