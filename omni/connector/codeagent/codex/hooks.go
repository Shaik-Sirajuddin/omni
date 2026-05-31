package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	codehooks "github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

// ============================================================
// Path resolution
// ============================================================

func globalCodexDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("codex: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

func globalConfigPath() (string, error) {
	dir, err := globalCodexDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// ============================================================
// Legacy hooks.json types — used by codex.go Register/Delete/GetRegistered
// ============================================================

type codexHookEntry struct {
	UID     string   `json:"uid"`
	Event   string   `json:"event"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Timeout int      `json:"timeout,omitempty"`
	Url     *string  `json:"url,omitempty"`
}

type codexHooksFile struct {
	Hooks []codexHookEntry `json:"hooks"`
}

func hooksFilePath(path codehooks.HookPath, fallbackDir string) (string, error) {
	if path.Global {
		dir, err := globalCodexDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "hooks.json"), nil
	}
	dir := fallbackDir
	if path.WorkspaceDir != nil && *path.WorkspaceDir != "" {
		dir = *path.WorkspaceDir
	}
	return filepath.Join(dir, ".codex", "hooks.json"), nil
}

func readHooksFile(path string) (codexHooksFile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return codexHooksFile{}, nil
	}
	if err != nil {
		return codexHooksFile{}, fmt.Errorf("codex: read hooks file %s: %w", path, err)
	}
	var f codexHooksFile
	if err := json.Unmarshal(data, &f); err != nil {
		return codexHooksFile{}, fmt.Errorf("codex: parse hooks file %s: %w", path, err)
	}
	return f, nil
}

func writeHooksFile(path string, f codexHooksFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("codex: mkdir hooks dir: %w", err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("codex: marshal hooks: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func hookDataToEntry(h *codehooks.HookData) codexHookEntry {
	return codexHookEntry{
		UID:     h.UID,
		Event:   string(h.Info.ID),
		Command: h.Info.Command,
		Args:    h.Info.Args,
		Timeout: h.Info.Timeout,
		Url:     h.Info.Url,
	}
}

func entryToHookData(e codexHookEntry, path codehooks.HookPath) *codehooks.HookData {
	return &codehooks.HookData{
		UID:  e.UID,
		Path: path,
		Info: &codehooks.HookInfo{
			ID:      codehooks.HookID(e.Event),
			Command: e.Command,
			Args:    e.Args,
			Timeout: e.Timeout,
			Url:     e.Url,
		},
	}
}

// ============================================================
// config.toml — TOML hook types (used by HookTransformer)
// ============================================================

type codexHookMatcher struct {
	Matcher string         `toml:"matcher,omitempty"`
	Hooks   []codexHookDef `toml:"hooks"`
}

type codexHookDef struct {
	Type    string `toml:"type"`
	Command string `toml:"command"`
	Timeout int    `toml:"timeout,omitempty"`
}

// ============================================================
// config.toml round-trip (preserves unknown keys)
// ============================================================

func readConfigTOML(path string) (map[string]interface{}, error) {
	cfg := map[string]interface{}{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("codex: read config.toml: %w", err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("codex: parse config.toml: %w", err)
	}
	return cfg, nil
}

func writeConfigTOML(path string, cfg map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("codex: mkdir config dir: %w", err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("codex: encode config.toml: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ============================================================
// config.toml — scalar key read/write (used by syncModelConfig)
// ============================================================

func readGlobalConfig() (map[string]string, error) {
	path, err := globalConfigPath()
	if err != nil {
		return nil, err
	}
	raw, err := readConfigTOML(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out, nil
}

func writeGlobalConfig(cfg map[string]string) error {
	path, err := globalConfigPath()
	if err != nil {
		return err
	}
	raw, err := readConfigTOML(path)
	if err != nil {
		return err
	}
	for k, v := range cfg {
		raw[k] = v
	}
	return writeConfigTOML(path, raw)
}

// ============================================================
// config.toml — hooks section (used by HookTransformer)
// ============================================================

func readHooksConfig(path string) (map[string][]codexHookMatcher, error) {
	raw, err := readConfigTOML(path)
	if err != nil {
		return nil, err
	}
	hooks := extractHooks(raw)
	events := make([]string, 0, len(hooks))
	for e := range hooks {
		events = append(events, e)
	}
	logger.Debug("readHooksConfig", "path", path, "events", events)
	return hooks, nil
}

func writeHooksConfig(path string, hooksByEvent map[string][]codexHookMatcher) error {
	raw, err := readConfigTOML(path)
	if err != nil {
		return err
	}

	// Ensure [features] hooks = true.
	features, _ := raw["features"].(map[string]interface{})
	if features == nil {
		features = map[string]interface{}{}
	}
	features["hooks"] = true
	raw["features"] = features

	// Convert typed hooks to []map[string]interface{} for TOML array-of-tables.
	hooksRaw := map[string]interface{}{}
	for event, matchers := range hooksByEvent {
		var arr []map[string]interface{}
		for _, m := range matchers {
			entry := map[string]interface{}{}
			if m.Matcher != "" {
				entry["matcher"] = m.Matcher
			}
			var defs []map[string]interface{}
			for _, d := range m.Hooks {
				def := map[string]interface{}{"type": d.Type, "command": d.Command}
				if d.Timeout > 0 {
					def["timeout"] = d.Timeout
				}
				defs = append(defs, def)
			}
			entry["hooks"] = defs
			arr = append(arr, entry)
		}
		hooksRaw[event] = arr
	}
	raw["hooks"] = hooksRaw

	events := make([]string, 0, len(hooksByEvent))
	for e := range hooksByEvent {
		events = append(events, e)
	}
	logger.Debug("writeHooksConfig", "path", path, "events", events)
	return writeConfigTOML(path, raw)
}

// extractHooks converts the raw TOML hooks value back to typed matchers.
// BurntSushi/toml decodes arrays-of-tables into []interface{} (not
// []map[string]interface{}), so each element must be cast individually.
func extractHooks(raw map[string]interface{}) map[string][]codexHookMatcher {
	out := map[string][]codexHookMatcher{}
	hooksMap, ok := raw["hooks"].(map[string]interface{})
	if !ok {
		return out
	}
	for event, matchersVal := range hooksMap {
		matchersSlice, ok := matchersVal.([]interface{})
		if !ok {
			continue
		}
		for _, elem := range matchersSlice {
			m, ok := elem.(map[string]interface{})
			if !ok {
				continue
			}
			var hm codexHookMatcher
			if v, ok := m["matcher"].(string); ok {
				hm.Matcher = v
			}
			if defsRaw, ok := m["hooks"].([]interface{}); ok {
				for _, dr := range defsRaw {
					d, ok := dr.(map[string]interface{})
					if !ok {
						continue
					}
					hd := codexHookDef{Type: "command"}
					if v, ok := d["type"].(string); ok {
						hd.Type = v
					}
					if v, ok := d["command"].(string); ok {
						hd.Command = v
					}
					if v, ok := d["timeout"].(int64); ok {
						hd.Timeout = int(v)
					}
					hm.Hooks = append(hm.Hooks, hd)
				}
			}
			out[event] = append(out[event], hm)
		}
	}
	return out
}

// ============================================================
// config.toml — MCP server approval mode
// ============================================================

func ensureMCPApprovalMode(serverName string) error {
	path, err := globalConfigPath()
	if err != nil {
		return err
	}
	raw, err := readConfigTOML(path)
	if err != nil {
		return err
	}
	mcpServers, _ := raw["mcp_servers"].(map[string]interface{})
	if mcpServers == nil {
		// No mcp_servers section — nothing to patch.
		return nil
	}
	server, _ := mcpServers[serverName].(map[string]interface{})
	if server == nil {
		// Server entry absent — skip rather than creating a transport-less stub
		// that Codex would reject as "invalid transport".
		return nil
	}

	// Set server-level default.
	server["default_tools_approval_mode"] = "auto"

	// Override any per-tool entries that still have "approve" — they would
	// block auto-approval even with the server-level default set to "auto".
	if tools, ok := server["tools"].(map[string]interface{}); ok {
		for toolName, toolVal := range tools {
			toolCfg, ok := toolVal.(map[string]interface{})
			if !ok {
				continue
			}
			if toolCfg["approval_mode"] == "approve" {
				toolCfg["approval_mode"] = "auto"
				tools[toolName] = toolCfg
			}
		}
		server["tools"] = tools
	}

	mcpServers[serverName] = server
	raw["mcp_servers"] = mcpServers
	return writeConfigTOML(path, raw)
}

// ============================================================
// Event name mapping — omni ↔ Codex CLI
// ============================================================

func codexEventName(omniEvent string) (string, bool) {
	switch omniEvent {
	case string(codehooks.PreToolUse):
		return "PreToolUse", true
	case string(codehooks.PostToolUse):
		return "PostToolUse", true
	case string(codehooks.PostToolUseFailure):
		return "PostToolUseFailure", true
	case string(codehooks.SessionStart):
		return "SessionStart", true
	case string(codehooks.SessionEnd):
		return "Stop", true
	case string(codehooks.PrePrompt):
		return "UserPromptSubmit", true
	case string(codehooks.PostPrompt):
		return "Stop", true
	default:
		return "", false
	}
}

func omniEventFromCodex(codexEvent string) (string, bool) {
	switch codexEvent {
	case "PreToolUse":
		return string(codehooks.PreToolUse), true
	case "PostToolUse":
		return string(codehooks.PostToolUse), true
	case "PostToolUseFailure":
		return string(codehooks.PostToolUseFailure), true
	case "SessionStart":
		return string(codehooks.SessionStart), true
	case "Stop":
		return string(codehooks.SessionEnd), true
	case "UserPromptSubmit":
		return string(codehooks.PrePrompt), true
	default:
		return "", false
	}
}

// ============================================================
// Sandbox flag ↔ *sandbox.Config
// ============================================================

func sandboxFromFlag(flag string) *sandbox.Config {
	switch flag {
	case "danger-full-access":
		return &sandbox.Config{
			AgentPolicy: &sandbox.Policy{FSPolicy: sandbox.FSPolicy(sandbox.AllPermissiveRead)},
		}
	case "read-only":
		return &sandbox.Config{
			AgentPolicy: &sandbox.Policy{FSPolicy: sandbox.FSPolicy(sandbox.PermissiveRead)},
		}
	default:
		return nil
	}
}
