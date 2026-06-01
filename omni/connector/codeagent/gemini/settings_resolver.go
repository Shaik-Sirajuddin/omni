package gemini

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Shaik-Sirajuddin/omni/connector/codeagent"
	sandbox "github.com/Shaik-Sirajuddin/omni/sandbox/provider"
)

type settingsResolver struct {
	mu       sync.Mutex
	watchers []settingsWatchEntry
}

type settingsWatchEntry struct {
	path     string
	lastMod  time.Time
	callback func(*codeagent.Settings)
}

// workspaceSettingsCandidates are checked in order; first existing path wins.
var workspaceSettingsCandidates = []string{
	filepath.Join(".gemini", "settings.json"),
	filepath.Join(".config", "gemini", "settings.json"),
}

// newSettingsResolver constructs a settings resolver instance.
func newSettingsResolver() *settingsResolver { return &settingsResolver{} }

// GetUserSettings resolves and returns user-level Gemini settings.
func (a *geminiAgent) GetUserSettings() (*codeagent.Settings, error) {
	return a.settings.getUserSettings()
}

// GetWorkspaceSettings resolves and returns workspace-level Gemini settings.
func (a *geminiAgent) GetWorkspaceSettings(dir sandbox.WorkspaceDir) (*codeagent.Settings, error) {
	return a.settings.getWorkspaceSettings(dir)
}

// SaveDefaultSettings writes user-level Gemini settings.
func (a *geminiAgent) SaveDefaultSettings(s *codeagent.Settings) error {
	return a.settings.saveDefaultSettings(s)
}

// WatchDefaultSettings registers a callback for user settings changes.
func (a *geminiAgent) WatchDefaultSettings(fn func(*codeagent.Settings)) error {
	return a.settings.watchDefaultSettings(fn)
}

// getUserSettings reads the resolved user settings file.
func (r *settingsResolver) getUserSettings() (*codeagent.Settings, error) {
	path, err := resolveUserSettingsPath()
	if err != nil {
		return nil, fmt.Errorf("gemini settings: resolve user path: %w", err)
	}
	return r.read(path)
}

// getWorkspaceSettings reads the resolved workspace settings file.
func (r *settingsResolver) getWorkspaceSettings(dir sandbox.WorkspaceDir) (*codeagent.Settings, error) {
	workspaceDir := string(dir)
	if workspaceDir == "" || workspaceDir == string(sandbox.Default) {
		var err error
		workspaceDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("gemini settings: resolve workspace dir: %w", err)
		}
	}
	path := resolveWorkspaceSettingsPath(workspaceDir)
	return r.read(path)
}

// saveDefaultSettings writes default settings into the user settings file.
func (r *settingsResolver) saveDefaultSettings(s *codeagent.Settings) error {
	path, err := resolveUserSettingsPath()
	if err != nil {
		return fmt.Errorf("gemini settings: resolve user path: %w", err)
	}
	return r.save(path, s)
}

// watchDefaultSettings starts a polling watcher for the user settings file.
// Polling is used to keep this implementation dependency-free and portable.
func (r *settingsResolver) watchDefaultSettings(fn func(*codeagent.Settings)) error {
	path, err := resolveUserSettingsPath()
	if err != nil {
		return fmt.Errorf("gemini settings: resolve user path: %w", err)
	}

	info, _ := os.Stat(path)
	var lastMod time.Time
	if info != nil {
		lastMod = info.ModTime()
	}

	r.mu.Lock()
	r.watchers = append(r.watchers, settingsWatchEntry{path: path, lastMod: lastMod, callback: fn})
	idx := len(r.watchers) - 1
	r.mu.Unlock()

	go r.pollLoop(idx)
	return nil
}

// read loads and maps raw Gemini settings into neutral codeagent settings.
func (r *settingsResolver) read(path string) (*codeagent.Settings, error) {
	f, err := readSettingsFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultCodeagentSettings(), nil
		}
		return nil, fmt.Errorf("gemini settings: read %s: %w", path, err)
	}
	return settingsFileToCodeagent(f), nil
}

// save merges and persists neutral codeagent settings into Gemini settings.
func (r *settingsResolver) save(path string, s *codeagent.Settings) error {
	var f SettingsSchemaJson
	if existing, err := readSettingsFile(path); err == nil {
		f = existing
	}

	if s == nil {
		return fmt.Errorf("gemini settings: nil settings")
	}
	if s.Config.Model.Model != "" {
		f.Model.Name = stringPtr(s.Config.Model.Model)
	}
	if s.Config.PermissionMode != "" {
		f.General.DefaultApprovalMode = SettingsSchemaJsonGeneralDefaultApprovalMode(approvalModeFlag(s.Config.PermissionMode))
	}
	if sandboxValue := sandboxFlagValue(s.Config.Sandbox); sandboxValue != "" {
		f.Tools.Sandbox = sandboxValue
	}

	if err := writeSettingsFile(path, f); err != nil {
		return fmt.Errorf("gemini settings: write %s: %w", path, err)
	}
	return nil
}

// pollLoop checks for settings file updates and emits callbacks on change.
func (r *settingsResolver) pollLoop(idx int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		r.mu.Lock()
		if idx >= len(r.watchers) {
			r.mu.Unlock()
			return
		}
		entry := r.watchers[idx]
		r.mu.Unlock()

		info, err := os.Stat(entry.path)
		if err != nil {
			continue
		}
		if info.ModTime().Equal(entry.lastMod) {
			continue
		}

		r.mu.Lock()
		r.watchers[idx].lastMod = info.ModTime()
		r.mu.Unlock()

		s, err := r.read(entry.path)
		if err != nil {
			continue
		}
		entry.callback(s)
	}
}

// settingsFileToCodeagent maps provider-specific settings to neutral settings.
func settingsFileToCodeagent(f SettingsSchemaJson) *codeagent.Settings {
	model := ""
	if f.Model.Name != nil {
		model = *f.Model.Name
	}
	sandboxValue, _ := f.Tools.Sandbox.(string)
	return &codeagent.Settings{
		Provider: Gemini,
		Config: codeagent.Config{
			Model:          codeagent.Model{Provider: Gemini, Model: model},
			PermissionMode: permissionFromApprovalMode(string(f.General.DefaultApprovalMode)),
			Sandbox:        sandboxFromFlag(sandboxValue),
		},
	}
}

// defaultCodeagentSettings returns minimal default neutral settings.
func defaultCodeagentSettings() *codeagent.Settings {
	return &codeagent.Settings{
		Provider: Gemini,
		Config: codeagent.Config{
			Model:          codeagent.Model{Provider: Gemini},
			PermissionMode: codeagent.PermissionDefault,
		},
	}
}

// resolveUserSettingsPath resolves the user-scoped settings path.
func resolveUserSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return firstExistingPath([]string{
		filepath.Join(home, ".gemini", "settings.json"),
		filepath.Join(home, ".config", "gemini", "settings.json"),
	}), nil
}

// resolveWorkspaceSettingsPath resolves the workspace-scoped settings path.
func resolveWorkspaceSettingsPath(workspaceDir string) string {
	paths := make([]string, 0, len(workspaceSettingsCandidates))
	for _, rel := range workspaceSettingsCandidates {
		paths = append(paths, filepath.Join(workspaceDir, rel))
	}
	return firstExistingPath(paths)
}

// firstExistingPath returns the first existing path or the first candidate.
func firstExistingPath(paths []string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}
