package settings

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Shaik-Sirajuddin/omni/connector/codeagent"
	sandbox "github.com/Shaik-Sirajuddin/omni/sandbox/provider"
)

// rawConfig mirrors the codex config.toml fields that SettingsResolver maps to
// codeagent.Settings. The generated schema documents the same top-level keys,
// especially `model` and `sandbox_mode`.
type rawConfig struct {
	Model       *string
	SandboxMode *string
}

// ============================================================
// Resolver
// ============================================================

// Resolver implements codeagent.SettingsResolver for the codex CLI.
// It reads and writes ~/.codex/config.toml (global) and
// <workspaceDir>/.codex/config.toml (workspace).
type Resolver struct {
	provider  codeagent.Provider
	watchMu   sync.Mutex
	watchStop context.CancelFunc
}

// New returns a Resolver for the given codex provider value.
func New(provider codeagent.Provider) *Resolver {
	return &Resolver{provider: provider}
}

// ============================================================
// Path resolution — first-resolved order
// ============================================================

// UserConfigPath returns the user-level codex config path.
// Returns the primary path even if it does not exist
// (SaveDefaultSettings will create it).
//  1. ~/.codex/config.toml
func UserConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("settings: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

// WorkspaceConfigPath returns <workspaceDir>/.codex/config.toml.
func WorkspaceConfigPath(workspaceDir string) string {
	return filepath.Join(workspaceDir, ".codex", "config.toml")
}

// ============================================================
// File I/O
// ============================================================

// readConfig reads the top-level string keys we sync in codex config.toml.
// Returns zero value without error if the file does not exist.
func readConfig(path string) (rawConfig, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return rawConfig{}, nil
	}
	if err != nil {
		return rawConfig{}, fmt.Errorf("settings: read %s: %w", path, err)
	}
	var cfg rawConfig
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), "\"")
		switch key {
		case "model":
			v := value
			cfg.Model = &v
		case "sandbox_mode":
			v := value
			cfg.SandboxMode = &v
		}
	}
	return cfg, nil
}

// mergeAndWrite reads the existing config.toml top-level scalar keys,
// applies key transformations from s, then writes it back.
// Comment lines and TOML tables are preserved verbatim.
func mergeAndWrite(path string, s codeagent.Settings) error {
	existing := map[string]string{}
	var preserved []string
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				preserved = append(preserved, line)
				continue
			}
			if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[") {
				preserved = append(preserved, line)
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				preserved = append(preserved, line)
				continue
			}
			key := strings.TrimSpace(parts[0])
			existing[key] = strings.Trim(strings.TrimSpace(parts[1]), "\"")
		}
	}

	if s.Config.Model.Model != "" {
		existing["model"] = s.Config.Model.Model
	}

	if s.Config.Sandbox != nil {
		if s.Config.Sandbox.AgentPolicy != nil && s.Config.Sandbox.AgentPolicy.FSPolicy == sandbox.FSPolicy(sandbox.AllPermissiveRead) {
			existing["sandbox_mode"] = "danger-full-access"
		} else if s.Config.Sandbox.AgentPolicy != nil && s.Config.Sandbox.AgentPolicy.FSPolicy == sandbox.FSPolicy(sandbox.Inherit) {
			existing["sandbox_mode"] = "workspace-write"
		} else {
			existing["sandbox_mode"] = "read-only"
		}
	} else {
		delete(existing, "sandbox_mode")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("settings: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder
	written := map[string]bool{}
	for _, line := range preserved {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	if model, ok := existing["model"]; ok {
		sb.WriteString(fmt.Sprintf("model = %q\n", model))
		written["model"] = true
	}
	if sandboxMode, ok := existing["sandbox_mode"]; ok {
		sb.WriteString(fmt.Sprintf("sandbox_mode = %q\n", sandboxMode))
		written["sandbox_mode"] = true
	}
	for key, value := range existing {
		if written[key] {
			continue
		}
		sb.WriteString(fmt.Sprintf("%s = %q\n", key, value))
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("settings: write %s: %w", path, err)
	}
	return nil
}

// ============================================================
// rawConfig → codeagent.Settings
// ============================================================

func (r *Resolver) convert(cfg rawConfig) *codeagent.Settings {
	s := &codeagent.Settings{Provider: r.provider}
	if cfg.Model != nil && *cfg.Model != "" {
		s.Config.Model = codeagent.Model{Provider: r.provider, Model: *cfg.Model}
	}
	if cfg.SandboxMode != nil {
		switch *cfg.SandboxMode {
		case "danger-full-access":
			s.Config.Sandbox = &sandbox.Config{
				AgentPolicy: &sandbox.Policy{FSPolicy: sandbox.FSPolicy(sandbox.AllPermissiveRead)},
			}
		case "workspace-write":
			s.Config.Sandbox = &sandbox.Config{
				AgentPolicy: &sandbox.Policy{FSPolicy: sandbox.FSPolicy(sandbox.Inherit)},
			}
		case "read-only":
			s.Config.Sandbox = &sandbox.Config{
				AgentPolicy: &sandbox.Policy{FSPolicy: sandbox.FSPolicy(sandbox.PermissiveRead)},
			}
		}
	}
	return s
}

// ============================================================
// SettingsResolver implementation
// ============================================================

// GetUserSettings reads the user-level config from ~/.codex/config.toml.
func (r *Resolver) GetUserSettings() (*codeagent.Settings, error) {
	path, err := UserConfigPath()
	if err != nil {
		return nil, err
	}
	cfg, err := readConfig(path)
	if err != nil {
		return nil, err
	}
	return r.convert(cfg), nil
}

// GetWorkspaceSettings reads <workspaceDir>/.codex/config.toml.
func (r *Resolver) GetWorkspaceSettings(dir sandbox.WorkspaceDir) (*codeagent.Settings, error) {
	cfg, err := readConfig(WorkspaceConfigPath(string(dir)))
	if err != nil {
		return nil, err
	}
	return r.convert(cfg), nil
}

// SaveDefaultSettings merges s into ~/.codex/config.toml, preserving all
// existing keys that are not explicitly overridden.
func (r *Resolver) SaveDefaultSettings(s *codeagent.Settings) error {
	if s == nil {
		return nil
	}
	path, err := UserConfigPath()
	if err != nil {
		return err
	}
	return mergeAndWrite(path, *s)
}

// WatchDefaultSettings starts a 1-second polling watcher on ~/.codex/config.toml.
// cb is invoked whenever the file's modification time changes.
// A second call stops the previous watcher before starting a new one.
func (r *Resolver) WatchDefaultSettings(cb func(*codeagent.Settings)) error {
	path, err := UserConfigPath()
	if err != nil {
		return err
	}

	r.watchMu.Lock()
	if r.watchStop != nil {
		r.watchStop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.watchStop = cancel
	r.watchMu.Unlock()

	go func() {
		var lastMod time.Time
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				info, statErr := os.Stat(path)
				if statErr != nil {
					continue
				}
				if info.ModTime().After(lastMod) {
					lastMod = info.ModTime()
					if cfg, readErr := readConfig(path); readErr == nil {
						cb(r.convert(cfg))
					}
				}
			}
		}
	}()
	return nil
}

// StopWatch cancels any active WatchDefaultSettings goroutine.
func (r *Resolver) StopWatch() {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	if r.watchStop != nil {
		r.watchStop()
		r.watchStop = nil
	}
}
