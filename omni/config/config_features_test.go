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
		if cfg.Features.AxolinkMCP == nil || !*cfg.Features.AxolinkMCP {
			t.Errorf("expected AxolinkMCP to default to true, got %v", cfg.Features.AxolinkMCP)
		}
	})

	t.Run("nil Features produces default with AxolinkMCP=true", func(t *testing.T) {
		cfg := ApplyOmniConfigDefaults(&OmniConfig{Features: nil})
		if cfg.Features == nil {
			t.Fatal("expected Features to be non-nil")
		}
		if cfg.Features.AxolinkMCP == nil || !*cfg.Features.AxolinkMCP {
			t.Errorf("expected AxolinkMCP to default to true, got %v", cfg.Features.AxolinkMCP)
		}
	})

	t.Run("ProvisionDefaultOmniConfig sets AxolinkMCP=true", func(t *testing.T) {
		cfg := ProvisionDefaultOmniConfig()
		if cfg.Features == nil {
			t.Fatal("expected Features to be non-nil")
		}
		if cfg.Features.AxolinkMCP == nil || !*cfg.Features.AxolinkMCP {
			t.Errorf("expected AxolinkMCP to default to true in provisioned config, got %v", cfg.Features.AxolinkMCP)
		}
	})
}

// TestAxolinkMCPFalsePreserved verifies that explicitly setting AxolinkMCP=false
// is respected and not overwritten by ApplyOmniConfigDefaults.
func TestAxolinkMCPFalsePreserved(t *testing.T) {
	f := false
	cfg := ApplyOmniConfigDefaults(&OmniConfig{
		Features: &Features{
			AxolinkMCP: &f,
		},
	})
	if cfg.Features == nil {
		t.Fatal("expected Features to be non-nil")
	}
	if cfg.Features.AxolinkMCP == nil || *cfg.Features.AxolinkMCP {
		t.Errorf("expected AxolinkMCP=false to be preserved after applying defaults, got %v", cfg.Features.AxolinkMCP)
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
		if f.AxolinkMCP == nil || *f.AxolinkMCP {
			t.Errorf("expected AxolinkMCP=false after unmarshal, got %v", f.AxolinkMCP)
		}
	})

	t.Run("marshal then unmarshal roundtrip with explicit false", func(t *testing.T) {
		fval := false
		original := Features{
			AutoSync:   true,
			AxolinkMCP: &fval,
		}
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		var result Features
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if result.AxolinkMCP == nil || *result.AxolinkMCP != *original.AxolinkMCP {
			t.Errorf("roundtrip: expected AxolinkMCP=%v, got %v", *original.AxolinkMCP, result.AxolinkMCP)
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
		if f.AxolinkMCP == nil || !*f.AxolinkMCP {
			t.Errorf("expected AxolinkMCP=true after unmarshal, got %v", f.AxolinkMCP)
		}
	})

	t.Run("absent axolink_mcp key marshals as omitted", func(t *testing.T) {
		// When AxolinkMCP is nil (not set), it should be omitted from JSON output.
		f := Features{AutoSync: true}
		data, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal to map error: %v", err)
		}
		if _, present := m["axolink_mcp"]; present {
			t.Error("expected axolink_mcp to be omitted from JSON when nil, but it was present")
		}
	})
}

// TestAxolinkMCP_DefaultsTrue_WhenFeaturesPartiallyPopulated simulates an
// existing user config with other Features fields present but the axolink_mcp
// key absent — the real upgrade scenario.
func TestAxolinkMCP_DefaultsTrue_WhenFeaturesPartiallyPopulated(t *testing.T) {
	// auto_sync is a promoted Features field; its presence in JSON causes
	// json.Unmarshal to allocate *Features, leaving AxolinkMCP nil.
	raw := []byte(`{"auto_sync": true}`)
	var cfg OmniConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := ApplyOmniConfigDefaults(&cfg)
	if out.Features == nil || out.Features.AxolinkMCP == nil || !*out.Features.AxolinkMCP {
		t.Errorf("expected AxolinkMCP=true after ApplyOmniConfigDefaults on partial Features, got %+v", out.Features)
	}
}
