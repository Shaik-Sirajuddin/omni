package codex

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Shaik-Sirajuddin/omni/connector/codeagent"
	sandbox "github.com/Shaik-Sirajuddin/omni/sandbox/provider"
)

// sandboxFlagValue maps a *sandbox.Config to the --sandbox flag value codex accepts.
// sandbox disabled — always returns "" so no sandbox flag is appended to CLI args.
func sandboxFlagValue(_ *sandbox.Config) string {
	// if s == nil { return "" }
	// if s.AgentPolicy != nil && s.AgentPolicy.FSPolicy == sandbox.FSPolicy(sandbox.AllPermissiveRead) { return "danger-full-access" }
	// return "read-only"
	return "" // sandbox disabled
}

// ============================================================
// GetSessionSandbox / UpdateSessionSandbox
// ============================================================

func (a *codexAgent) GetSessionSandbox(_ codeagent.GetSessionSandboxParams) (*codeagent.GetSessionSandboxResult, error) {
	// sandbox disabled
	// a.mu.RLock(); defer a.mu.RUnlock()
	// return &codeagent.GetSessionSandboxResult{Sandbox: a.sbx}, nil
	return &codeagent.GetSessionSandboxResult{}, nil
}

func (a *codexAgent) UpdateSessionSandbox(_ codeagent.UpdateSessionSandboxParams) (*codeagent.UpdateSessionSandboxResult, error) {
	// sandbox disabled
	// a.mu.Lock(); a.sbx = p.Sandbox; syncSandboxRuntimeLocked; syncSandboxConfig
	return &codeagent.UpdateSessionSandboxResult{}, nil
}

// syncModelConfig writes the chosen model into .codex/config.yaml so that
// interactive `codex` sessions launched in workDir inherit the same model.
func syncModelConfig(workDir, model string) error {
	return writeCodexConfig(workDir, func(cfg map[string]string) {
		if model != "" {
			cfg["model"] = model
		} else {
			delete(cfg, "model")
		}
	})
}

// syncSandboxConfig performs the outbound sync: writes the resolved sandbox flag
// into .codex/config.toml in the active working directory so that interactive
// codex sessions launched later inherit the same sandbox policy.
func (a *codexAgent) syncSandboxConfig() error {
	// sandbox disabled — no-op
	// flag := sandboxFlagValue(a.sbx); writeCodexConfig(a.workDir, ...)
	return nil
}

// writeCodexConfig reads .codex/config.toml, applies mutateFn to the top-level
// string key/value map, and writes it back preserving all other TOML sections.
func writeCodexConfig(workDir string, mutateFn func(map[string]string)) error {
	configDir := filepath.Join(workDir, ".codex")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", configDir, err)
	}

	configPath := filepath.Join(configDir, "config.toml")

	// Full TOML round-trip so all sections ([hooks], [mcp_servers], etc.) survive.
	raw, err := readConfigTOML(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}

	// Extract current top-level string values for the mutation callback.
	existing := map[string]string{}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			existing[k] = s
		}
	}

	mutateFn(existing)

	// Merge results back: update/add changed keys, delete removed ones.
	for k, v := range existing {
		raw[k] = v
	}
	for k, v := range raw {
		if _, isStr := v.(string); isStr {
			if _, kept := existing[k]; !kept {
				delete(raw, k)
			}
		}
	}

	return writeConfigTOML(configPath, raw)
}
