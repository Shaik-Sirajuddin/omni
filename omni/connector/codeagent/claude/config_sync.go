package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

var claudeUserSettingsCandidates = []string{
	".claude/settings.json",
	".config/claude/settings.json",
}

func claudeUserSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("claude settings: resolve home dir: %w", err)
	}
	for _, rel := range claudeUserSettingsCandidates {
		path := filepath.Join(home, rel)
		if _, statErr := os.Stat(path); statErr == nil {
			return path, nil
		}
	}
	return filepath.Join(home, claudeUserSettingsCandidates[0]), nil
}

func claudeWorkspaceSettingsPath(workDir string) string {
	return filepath.Join(workDir, ".claude", "settings.json")
}

func readClaudeSettings(path string) (*codeagent.Settings, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &codeagent.Settings{Provider: Claude}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claude settings: read %s: %w", path, err)
	}

	var cfg SettingsSchemaJson
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("claude settings: parse %s: %w", path, err)
	}
	return settingsSchemaToCodeagent(cfg), nil
}

func writeClaudeSettings(path string, s *codeagent.Settings) error {
	if s == nil {
		return nil
	}

	raw := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		var cfg SettingsSchemaJson
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("claude settings: parse %s: %w", path, err)
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("claude settings: decode raw %s: %w", path, err)
		}
	}

	if err := applyCodeagentSettingsToRaw(raw, s); err != nil {
		return err
	}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("claude settings: marshal %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("claude settings: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("claude settings: write %s: %w", path, err)
	}
	return nil
}

func settingsSchemaToCodeagent(cfg SettingsSchemaJson) *codeagent.Settings {
	settings := &codeagent.Settings{Provider: Claude}
	if cfg.Model != nil {
		settings.Config.Model = codeagent.Model{Provider: Claude, Model: *cfg.Model}
	}
	if cfg.Permissions != nil && cfg.Permissions.DefaultMode != nil {
		settings.Config.PermissionMode = codeagent.PermissionMode(*cfg.Permissions.DefaultMode)
	}
	settings.Config.Sandbox = sandboxFromClaudeSettings(cfg.Sandbox)
	settings.Config.Hooks = hooksFromClaudeSettings(cfg.Hooks)
	return settings
}

func applyCodeagentSettingsToRaw(raw map[string]json.RawMessage, s *codeagent.Settings) error {
	if s.Config.Model.Model != "" {
		data, err := json.Marshal(s.Config.Model.Model)
		if err != nil {
			return fmt.Errorf("claude settings: marshal model: %w", err)
		}
		raw["model"] = data
	}

	if s.Config.PermissionMode != "" {
		mode := SettingsSchemaJsonPermissionsDefaultMode(s.Config.PermissionMode)
		perms := &SettingsSchemaJsonPermissions{DefaultMode: &mode}
		data, err := json.Marshal(perms)
		if err != nil {
			return fmt.Errorf("claude settings: marshal permissions: %w", err)
		}
		raw["permissions"] = data
	}

	if s.Config.Sandbox != nil {
		sbx := claudeSandboxFromConfig(s.Config.Sandbox)
		data, err := json.Marshal(sbx)
		if err != nil {
			return fmt.Errorf("claude settings: marshal sandbox: %w", err)
		}
		raw["sandbox"] = data
	}

	if s.Config.Hooks != nil {
		data, err := json.Marshal(claudeHooksFromData(s.Config.Hooks))
		if err != nil {
			return fmt.Errorf("claude settings: marshal hooks: %w", err)
		}
		raw["hooks"] = data
	}

	return nil
}

func sandboxFromClaudeSettings(cfg *SettingsSchemaJsonSandbox) *sandbox.Config {
	if cfg == nil || cfg.Enabled == nil || !*cfg.Enabled {
		return nil
	}

	result := &sandbox.Config{
		AgentPolicy: &sandbox.Policy{FSPolicy: sandbox.FSPolicy(sandbox.PermissiveRead)},
	}
	if cfg.Filesystem == nil {
		return result
	}
	if len(cfg.Filesystem.AllowWrite) > 0 {
		result.AgentPolicy.FSPolicy = sandbox.FSPolicy(sandbox.AllPermissiveRead)
		result.AgentPolicy.Config.AccessDirs = append([]string(nil), cfg.Filesystem.AllowWrite...)
	}
	if len(cfg.Filesystem.DenyWrite) > 0 {
		result.AgentPolicy.Config.BlockedDirs = append([]string(nil), cfg.Filesystem.DenyWrite...)
	}
	return result
}

func claudeSandboxFromConfig(cfg *sandbox.Config) *SettingsSchemaJsonSandbox {
	enabled := true
	result := &SettingsSchemaJsonSandbox{Enabled: &enabled}
	if cfg == nil || cfg.AgentPolicy == nil {
		return result
	}

	filesystem := &SettingsSchemaJsonSandboxFilesystem{}
	filesystem.AllowWrite = append(filesystem.AllowWrite, cfg.AgentPolicy.Config.AccessDirs...)
	filesystem.DenyWrite = append(filesystem.DenyWrite, cfg.AgentPolicy.Config.BlockedDirs...)
	if cfg.AgentPolicy.FSPolicy == sandbox.FSPolicy(sandbox.AllPermissiveRead) && len(filesystem.AllowWrite) == 0 {
		filesystem.AllowWrite = append(filesystem.AllowWrite, ".")
	}
	if len(filesystem.AllowWrite) > 0 || len(filesystem.DenyWrite) > 0 {
		result.Filesystem = filesystem
	}
	return result
}

func hooksFromClaudeSettings(cfg *SettingsSchemaJsonHooks) *hooks.HookData {
	if cfg == nil {
		return nil
	}

	candidates := []struct {
		id       hooks.HookID
		matchers []HookMatcher
	}{
		{id: hooks.PreToolUse, matchers: cfg.PreToolUse},
		{id: hooks.PostToolUse, matchers: cfg.PostToolUse},
		{id: hooks.PostToolUseFailure, matchers: cfg.PostToolUseFailure},
		{id: hooks.SessionStart, matchers: cfg.SessionStart},
		{id: hooks.SessionEnd, matchers: cfg.SessionStart},
		{id: hooks.PrePrompt, matchers: cfg.UserPromptSubmit},
		{id: hooks.PostPrompt, matchers: cfg.Stop},
	}

	for _, candidate := range candidates {
		for _, matcher := range candidate.matchers {
			if len(matcher.Hooks) == 0 {
				continue
			}
			entry := matcher.Hooks[0]
			info := &hooks.HookInfo{ID: candidate.id}
			switch entry.Type {
			case "http":
				info.Type = hooks.Webhook
				info.Url = entry.Url
			default:
				info.Type = hooks.CMD
				if entry.Command != nil {
					info.Command = *entry.Command
				}
			}
			if entry.Timeout != nil {
				info.Timeout = int(*entry.Timeout)
			}
			return &hooks.HookData{Info: info}
		}
	}
	return nil
}

func claudeHooksFromData(h *hooks.HookData) *SettingsSchemaJsonHooks {
	if h == nil || h.Info == nil {
		return nil
	}

	eventName, ok := hookEventName[h.Info.ID]
	if !ok {
		return nil
	}

	entry := HookCommand{Type: "command"}
	switch h.Info.Type {
	case hooks.Webhook:
		entry.Type = "http"
		entry.Url = h.Info.Url
	default:
		cmd := h.Info.Command
		if len(h.Info.Args) > 0 {
			cmd += " " + joinArgs(h.Info.Args)
		}
		entry.Command = &cmd
	}
	if h.Info.Timeout > 0 {
		timeout := float64(h.Info.Timeout)
		entry.Timeout = &timeout
	}

	matchers := &SettingsSchemaJsonHooks{}
	switch eventName {
	case "PreToolUse":
		matchers.PreToolUse = []HookMatcher{{Hooks: []HookCommand{entry}}}
	case "PostToolUse":
		matchers.PostToolUse = []HookMatcher{{Hooks: []HookCommand{entry}}}
	case "PostToolUseFailure":
		matchers.PostToolUseFailure = []HookMatcher{{Hooks: []HookCommand{entry}}}
	case "SessionStart":
		matchers.SessionStart = []HookMatcher{{Hooks: []HookCommand{entry}}}
	case "UserPromptSubmit":
		matchers.UserPromptSubmit = []HookMatcher{{Hooks: []HookCommand{entry}}}
	case "Stop":
		matchers.Stop = []HookMatcher{{Hooks: []HookCommand{entry}}}
	default:
		return nil
	}
	return matchers
}
