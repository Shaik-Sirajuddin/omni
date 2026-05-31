package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
)

// ============================================================
// GetSessionSandbox / UpdateSessionSandbox
// ============================================================

func (a *claudeAgent) GetSessionSandbox(_ codeagent.GetSessionSandboxParams) (*codeagent.GetSessionSandboxResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	logger.Debug("GetSessionSandbox", "sandbox", a.sbx)
	return &codeagent.GetSessionSandboxResult{Sandbox: a.sbx}, nil
}

func (a *claudeAgent) UpdateSessionSandbox(p codeagent.UpdateSessionSandboxParams) (*codeagent.UpdateSessionSandboxResult, error) {
	a.mu.Lock()
	a.sbx = p.Sandbox
	workDir := a.workDir
	if err := a.syncSandboxRuntimeLocked(); err != nil {
		a.mu.Unlock()
		logger.Error("UpdateSessionSandbox: runtime sync failed", "err", err)
		return nil, fmt.Errorf("claude: update sandbox: sync runtime: %w", err)
	}
	a.mu.Unlock()

	if err := writeClaudeSettings(claudeWorkspaceSettingsPath(workDir), &codeagent.Settings{
		Provider: Claude,
		Config: codeagent.Config{
			Sandbox: p.Sandbox,
		},
	}); err != nil {
		logger.Error("UpdateSessionSandbox: settings sync failed", "err", err)
		return nil, fmt.Errorf("claude: update sandbox: sync settings: %w", err)
	}
	logger.Info("UpdateSessionSandbox: updated")
	return &codeagent.UpdateSessionSandboxResult{Sandbox: p.Sandbox}, nil
}

// ============================================================
// HookManager
// ============================================================

// hookEventName maps the abstract HookID to Claude's settings.json event key.
var hookEventName = map[hooks.HookID]string{
	hooks.PreToolUse:         "PreToolUse",
	hooks.PostToolUse:        "PostToolUse",
	hooks.PostToolUseFailure: "PostToolUseFailure",
	hooks.SessionStart:    "SessionStart",
	hooks.SessionEnd:   "SessionStart",
	hooks.PrePrompt:          "UserPromptSubmit",
	hooks.PostPrompt:         "Stop",
}

func (a *claudeAgent) SupportedHooks() (*hooks.Capabilities, error) {
	return &hooks.Capabilities{
		PreToolUse:         true,
		PostToolUse:        true,
		PostToolUseFailure: true,
		SessionStart:    true,
		SessionEnd:      true,
		PrePrompt:          true,
		PostPrompt:         false,
	}, nil
}

// Register adds a hook to the in-memory list and syncs it to .claude/settings.json.
func (a *claudeAgent) Register(p hooks.RegisterHookParams) error {
	if p.Data == nil {
		return errors.New("claude: register hook: nil HookData")
	}
	a.mu.Lock()
	a.registeredHooks = append(a.registeredHooks, p.Data)
	all := copyHooks(a.registeredHooks)
	workDir := a.workDir
	a.mu.Unlock()

	if err := syncHooksToSettings(workDir, all); err != nil {
		logger.Error("Register: settings sync failed", "err", err)
		return fmt.Errorf("claude: register hook: sync settings: %w", err)
	}

	logger.Info("Register: hook registered", "uid", p.Data.UID, "id", p.Data.Info.ID)
	return nil
}

// GetRegisteredHooks returns a snapshot of all registered hooks.
func (a *claudeAgent) GetRegisteredHooks() []*hooks.HookData {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return copyHooks(a.registeredHooks)
}

// DeleteHook removes the hook matching UID and re-syncs settings.json.
func (a *claudeAgent) DeleteHook(p hooks.DeleteHookParams) (bool, error) {
	a.mu.Lock()
	found := false
	for i, h := range a.registeredHooks {
		if h.UID == p.UID {
			a.registeredHooks = append(a.registeredHooks[:i], a.registeredHooks[i+1:]...)
			found = true
			break
		}
	}
	all := copyHooks(a.registeredHooks)
	workDir := a.workDir
	a.mu.Unlock()

	if !found {
		logger.Warn("DeleteHook: hook not found", "uid", p.UID)
		return false, fmt.Errorf("claude: delete hook: uid %q not found", p.UID)
	}

	if err := syncHooksToSettings(workDir, all); err != nil {
		logger.Error("DeleteHook: settings sync failed", "err", err)
		return true, fmt.Errorf("claude: delete hook: sync settings: %w", err)
	}

	logger.Info("DeleteHook: removed", "uid", p.UID)
	return true, nil
}

// ============================================================
// settings.json sync
// ============================================================

type claudeHookMatcher struct {
	Matcher string             `json:"matcher,omitempty"`
	Hooks   []claudeHookEntry  `json:"hooks"`
}

type claudeHookEntry struct {
	Type    string `json:"type"`              // "command" | "http"
	Command string `json:"command,omitempty"` // type=command
	URL     string `json:"url,omitempty"`     // type=http
	Timeout int    `json:"timeout,omitempty"`
}

// syncHooksToSettings writes all registered hooks into .claude/settings.json
// under the "hooks" key, keyed by Claude event name.
// Existing non-hooks fields are preserved.
func syncHooksToSettings(workDir string, registered []*hooks.HookData) error {
	settingsDir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", settingsDir, err)
	}

	settingsPath := filepath.Join(settingsDir, "settings.json")

	// Read existing settings as a raw map to preserve unknown fields.
	raw := map[string]json.RawMessage{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(data, &raw)
	}

	// Build hooks section from registered hooks.
	hooksMap := map[string][]claudeHookMatcher{}
	for _, h := range registered {
		if h.Info == nil {
			continue
		}
		eventName, ok := hookEventName[h.Info.ID]
		if !ok {
			continue
		}
		entry := hookDataToEntry(h)
		hooksMap[eventName] = append(hooksMap[eventName], claudeHookMatcher{
			Hooks: []claudeHookEntry{entry},
		})
	}

	// Serialise the hooks map into the raw settings.
	if len(hooksMap) > 0 {
		b, err := json.Marshal(hooksMap)
		if err != nil {
			return fmt.Errorf("marshal hooks: %w", err)
		}
		raw["hooks"] = b
	} else {
		delete(raw, "hooks")
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", settingsPath, err)
	}
	logger.Debug("syncHooksToSettings: wrote", "path", settingsPath, "hookCount", len(registered))
	return nil
}

func hookDataToEntry(h *hooks.HookData) claudeHookEntry {
	entry := claudeHookEntry{Timeout: h.Info.Timeout}
	switch h.Info.Type {
	case hooks.Webhook:
		entry.Type = "http"
		if h.Info.Url != nil {
			entry.URL = *h.Info.Url
		}
	default: // CMD
		entry.Type = "command"
		if len(h.Info.Args) > 0 {
			entry.Command = h.Info.Command + " " + joinArgs(h.Info.Args)
		} else {
			entry.Command = h.Info.Command
		}
	}
	return entry
}

// ============================================================
// Sandbox helper
// ============================================================

// ============================================================
// Utilities
// ============================================================

func copyHooks(src []*hooks.HookData) []*hooks.HookData {
	out := make([]*hooks.HookData, len(src))
	copy(out, src)
	return out
}

func joinArgs(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(a)
	}
	return b.String()
}
