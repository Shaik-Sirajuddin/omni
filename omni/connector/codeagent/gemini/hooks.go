package gemini

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

var eventNameByHookID = map[hooks.HookID]string{
	hooks.PreToolUse:         "BeforeTool",
	hooks.PostToolUse:        "AfterTool",
	hooks.PostToolUseFailure: "AfterTool",
	hooks.SessionStart:    "SessionStart",
	hooks.SessionEnd:   "SessionStart",
	hooks.PrePrompt:          "BeforeAgent",
	hooks.PostPrompt:         "AfterAgent",
}

var hookIDByEventName = map[string]hooks.HookID{
	"BeforeTool":   hooks.PreToolUse,
	"AfterTool":    hooks.PostToolUse,
	"SessionStart": hooks.SessionStart,
	"BeforeAgent":  hooks.PrePrompt,
	"AfterAgent":   hooks.PostPrompt,
}

func hookIDFromEvent(eventName string) (hooks.HookID, bool) {
	id, ok := hookIDByEventName[eventName]
	return id, ok
}

func globalGeminiDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("gemini: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".gemini"), nil
}

func globalSettingsPath() (string, error) {
	dir, err := globalGeminiDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

func settingsPathForHookPath(path hooks.HookPath, fallbackDir string) string {
	if path.Global {
		if p, err := globalSettingsPath(); err == nil {
			return p
		}
	}
	dir := fallbackDir
	if path.WorkspaceDir != nil && *path.WorkspaceDir != "" {
		dir = *path.WorkspaceDir
	}
	return filepath.Join(dir, ".gemini", "settings.json")
}

func readGlobalSettings() (map[string]string, error) {
	path, err := globalSettingsPath()
	if err != nil {
		return nil, err
	}
	f, err := readSettingsFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := map[string]string{}
	if f.Model.Name != nil && *f.Model.Name != "" {
		cfg["model"] = *f.Model.Name
	}
	if f.General.DefaultApprovalMode != "" {
		cfg["approvalMode"] = string(f.General.DefaultApprovalMode)
		cfg["defaultApprovalMode"] = string(f.General.DefaultApprovalMode)
	}
	if sandboxValue, ok := f.Tools.Sandbox.(string); ok && sandboxValue != "" {
		cfg["sandbox"] = sandboxValue
	}
	return cfg, nil
}

func writeGlobalSettings(cfg map[string]string) error {
	path, err := globalSettingsPath()
	if err != nil {
		return err
	}
	var f SettingsSchemaJson
	if existing, err := readSettingsFile(path); err == nil {
		f = existing
	}

	if model := strings.TrimSpace(cfg["model"]); model != "" {
		f.Model.Name = stringPtr(model)
	}
	if approvalMode := firstNonEmpty(cfg["defaultApprovalMode"], cfg["approvalMode"]); approvalMode != "" {
		f.General.DefaultApprovalMode = SettingsSchemaJsonGeneralDefaultApprovalMode(approvalMode)
	}
	if sandboxValue := strings.TrimSpace(cfg["sandbox"]); sandboxValue != "" {
		f.Tools.Sandbox = sandboxValue
	}
	return writeSettingsFile(path, f)
}

func readSettingsFile(path string) (SettingsSchemaJson, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SettingsSchemaJson{}, err
	}

	var f SettingsSchemaJson
	if err := json.Unmarshal(data, &f); err != nil {
		return SettingsSchemaJson{}, fmt.Errorf("gemini: parse settings %s: %w", path, err)
	}
	return f, nil
}

func writeSettingsFile(path string, f SettingsSchemaJson) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("gemini: mkdir settings dir: %w", err)
	}

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("gemini: marshal settings: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("gemini: write settings %s: %w", path, err)
	}
	return nil
}

func syncHooksToSettings(workDir string, registered []*hooks.HookData) error {
	path := filepath.Join(workDir, ".gemini", "settings.json")
	f, err := readSettingsFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	newHooks := &SettingsSchemaJsonHooks{}
	for _, h := range registered {
		if h == nil || h.Info == nil {
			continue
		}
		eventName, ok := eventNameByHookID[h.Info.ID]
		if !ok {
			continue
		}
		def := hookDataToDefinition(h)
		appendHookDefinition(newHooks, eventName, def)
	}
	f.Hooks = newHooks
	return writeSettingsFile(path, f)
}

func hookDataToDefinition(h *hooks.HookData) HookDefinitionArrayElemHooksElem {
	def := HookDefinitionArrayElemHooksElem{Name: stringPtr(h.UID)}
	if h.Info.Timeout > 0 {
		def.Timeout = floatPtr(float64(h.Info.Timeout))
	}
	if h.Info.Type == hooks.Webhook {
		if h.Info.Url != nil {
			def.Type = stringPtr("webhook")
			def.Command = stringPtr(*h.Info.Url)
		}
	} else {
		def.Type = stringPtr("command")
		if len(h.Info.Args) > 0 {
			def.Command = stringPtr(strings.Join(append([]string{h.Info.Command}, h.Info.Args...), " "))
		} else {
			def.Command = stringPtr(h.Info.Command)
		}
	}
	return def
}

func hookDefinitionToData(uid string, id hooks.HookID, d HookDefinitionArrayElemHooksElem, path hooks.HookPath) *hooks.HookData {
	timeout := 0
	if d.Timeout != nil {
		timeout = int(*d.Timeout)
	}
	info := &hooks.HookInfo{ID: id, Timeout: timeout}
	if d.Type != nil && *d.Type == "webhook" {
		info.Type = hooks.Webhook
		if d.Command != nil && *d.Command != "" {
			u := *d.Command
			info.Url = &u
		}
	} else {
		info.Type = hooks.CMD
		if d.Command != nil {
			info.Command = *d.Command
		}
	}
	return &hooks.HookData{UID: uid, Path: path, Info: info}
}

func appendHookDefinition(settingsHooks *SettingsSchemaJsonHooks, eventName string, def HookDefinitionArrayElemHooksElem) {
	entry := struct {
		Hooks   []HookDefinitionArrayElemHooksElem `json:"hooks,omitempty,omitzero" yaml:"hooks,omitempty" mapstructure:"hooks,omitempty"`
		Matcher *string                            `json:"matcher,omitempty,omitzero" yaml:"matcher,omitempty" mapstructure:"matcher,omitempty"`
	}{Hooks: []HookDefinitionArrayElemHooksElem{def}}

	switch eventName {
	case "BeforeTool":
		settingsHooks.BeforeTool = append(settingsHooks.BeforeTool, entry)
	case "AfterTool":
		settingsHooks.AfterTool = append(settingsHooks.AfterTool, entry)
	case "SessionStart":
		settingsHooks.SessionStart = append(settingsHooks.SessionStart, entry)
	case "BeforeAgent":
		settingsHooks.BeforeAgent = append(settingsHooks.BeforeAgent, entry)
	case "AfterAgent":
		settingsHooks.AfterAgent = append(settingsHooks.AfterAgent, entry)
	}
}

func hookArraysByEvent(settingsHooks *SettingsSchemaJsonHooks) map[string]HookDefinitionArray {
	if settingsHooks == nil {
		return map[string]HookDefinitionArray{}
	}
	return map[string]HookDefinitionArray{
		"BeforeTool":   settingsHooks.BeforeTool,
		"AfterTool":    settingsHooks.AfterTool,
		"SessionStart": settingsHooks.SessionStart,
		"BeforeAgent":  settingsHooks.BeforeAgent,
		"AfterAgent":   settingsHooks.AfterAgent,
	}
}

func stringPtr(v string) *string {
	return &v
}

func floatPtr(v float64) *float64 {
	return &v
}

// sandboxFromFlag reconstructs sandbox policy from stored settings.
func sandboxFromFlag(flag string) *sandbox.Config {
	switch strings.TrimSpace(flag) {
	case "danger-full-access", "full-access":
		return &sandbox.Config{
			AgentPolicy: &sandbox.Policy{
				FSPolicy: sandbox.FSPolicy(sandbox.Inherit),
			},
		}
	case "read-only":
		return &sandbox.Config{
			AgentPolicy: &sandbox.Policy{
				FSPolicy: sandbox.FSPolicy(sandbox.PermissiveRead),
			},
		}
	default:
		return nil
	}
}
