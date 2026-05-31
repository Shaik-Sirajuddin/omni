package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

// rawPermissions and rawFile hold only the fields we map to codeagent.Settings.
// The full generated schema lives in agy/settings.gen.go (agy package).
type rawPermissions struct {
	DefaultMode *string `json:"defaultMode,omitempty"`
}

type rawFile struct {
	Model           *string         `json:"model,omitempty"`
	Permissions     *rawPermissions `json:"permissions,omitempty"`
	AvailableModels []string        `json:"availableModels,omitempty"`
}

// Resolver implements codeagent.SettingsResolver backed by agy's settings.json.
type Resolver struct {
	provider  codeagent.Provider
	watchMu   sync.Mutex
	watchStop context.CancelFunc
}

// New returns a Resolver for the given agy provider.
func New(provider codeagent.Provider) *Resolver {
	return &Resolver{provider: provider}
}

// ============================================================
// Path resolution — first-resolved order
// ============================================================

// userSettingsCandidates returns the ordered list of user-level settings.json
// paths relative to home. First existing path wins; primary path is used when
// none exist (SaveDefaultSettings will create it).
//
// Search order:
//  1. ~/.agy/settings.json        (Agy Code default)
//  2. ~/.config/agy/settings.json (XDG fallback)
var userSettingsCandidates = []string{
	".agy/settings.json",
	".config/agy/settings.json",
}

// UserSettingsPath returns the first existing user-level settings.json path,
// or the primary path if none exist.
func UserSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("settings: resolve home dir: %w", err)
	}
	for _, rel := range userSettingsCandidates {
		p := filepath.Join(home, rel)
		if _, statErr := os.Stat(p); statErr == nil {
			return p, nil
		}
	}
	return filepath.Join(home, userSettingsCandidates[0]), nil
}

// WorkspaceSettingsPath returns <workspaceDir>/.agy/settings.json.
func WorkspaceSettingsPath(workspaceDir string) string {
	return filepath.Join(workspaceDir, ".agy", "settings.json")
}

// ============================================================
// File I/O
// ============================================================

func readRawFile(path string) (rawFile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return rawFile{}, nil
	}
	if err != nil {
		return rawFile{}, fmt.Errorf("settings: read %s: %w", path, err)
	}
	var f rawFile
	if err := json.Unmarshal(data, &f); err != nil {
		return rawFile{}, fmt.Errorf("settings: parse %s: %w", path, err)
	}
	return f, nil
}

// ReadUserSettingsRaw reads the first-resolved user settings file as raw JSON.
// Missing settings files return an empty map.
func ReadUserSettingsRaw() (map[string]json.RawMessage, error) {
	path, err := UserSettingsPath()
	if err != nil {
		return nil, err
	}
	return readSettingsRaw(path)
}

// UpdateUserSettingsRaw preserves unrelated keys while applying update to the
// first-resolved user settings file.
func UpdateUserSettingsRaw(update func(map[string]json.RawMessage) error) error {
	path, err := UserSettingsPath()
	if err != nil {
		return err
	}
	raw, err := readSettingsRaw(path)
	if err != nil {
		return err
	}
	if update != nil {
		if err := update(raw); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("settings: marshal %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("settings: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("settings: write %s: %w", path, err)
	}
	return nil
}

func readSettingsRaw(path string) (map[string]json.RawMessage, error) {
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return raw, nil
	}
	if err != nil {
		return nil, fmt.Errorf("settings: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("settings: decode raw %s: %w", path, err)
	}
	return raw, nil
}

// mergeAndWrite reads the existing settings.json at path as a raw map,
// applies key transformations from s, then writes it back.
// All unrelated keys in the file are preserved.
func mergeAndWrite(path string, s codeagent.Settings) error {
	existing := map[string]interface{}{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &existing)
	}

	if s.Config.Model.Model != "" {
		existing["model"] = s.Config.Model.Model
	}

	if s.Config.PermissionMode != "" {
		perms, ok := existing["permissions"].(map[string]interface{})
		if !ok {
			perms = map[string]interface{}{}
		}
		perms["defaultMode"] = string(s.Config.PermissionMode)
		existing["permissions"] = perms
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("settings: marshal %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("settings: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("settings: write %s: %w", path, err)
	}
	return nil
}

// ============================================================
// rawFile → codeagent.Settings
// ============================================================

func (r *Resolver) convert(f rawFile) *codeagent.Settings {
	s := &codeagent.Settings{Provider: r.provider}
	if f.Model != nil && *f.Model != "" {
		s.Config.Model = codeagent.Model{Provider: r.provider, Model: *f.Model}
	}
	if f.Permissions != nil && f.Permissions.DefaultMode != nil {
		s.Config.PermissionMode = codeagent.PermissionMode(*f.Permissions.DefaultMode)
	}
	return s
}

// ============================================================
// SettingsResolver implementation
// ============================================================

// GetUserSettings reads the user-level settings.json (first-resolved path).
func (r *Resolver) GetUserSettings() (*codeagent.Settings, error) {
	path, err := UserSettingsPath()
	if err != nil {
		return nil, err
	}
	f, err := readRawFile(path)
	if err != nil {
		return nil, err
	}
	return r.convert(f), nil
}

// GetWorkspaceSettings reads <workspaceDir>/.agy/settings.json.
func (r *Resolver) GetWorkspaceSettings(dir sandbox.WorkspaceDir) (*codeagent.Settings, error) {
	f, err := readRawFile(WorkspaceSettingsPath(string(dir)))
	if err != nil {
		return nil, err
	}
	return r.convert(f), nil
}

// SaveDefaultSettings merges s into the user-level settings.json, preserving
// all keys not explicitly overridden.
func (r *Resolver) SaveDefaultSettings(s *codeagent.Settings) error {
	if s == nil {
		return nil
	}
	path, err := UserSettingsPath()
	if err != nil {
		return err
	}
	return mergeAndWrite(path, *s)
}

// WatchDefaultSettings starts a native OS file watcher (inotify on Linux,
// kqueue on macOS, polling elsewhere) on the user-level settings.json.
// cb is called whenever the file changes. A second call stops the previous
// watcher before starting a new one.
func (r *Resolver) WatchDefaultSettings(cb func(*codeagent.Settings)) error {
	path, err := UserSettingsPath()
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

	onChange := func() {
		if f, readErr := readRawFile(path); readErr == nil {
			cb(r.convert(f))
		}
	}

	return osWatch(ctx, path, onChange)
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

// DiscoverModels reads availableModels from the user-level settings.json.
// Returns nil when the field is absent (caller should fall back to static list).
func (r *Resolver) DiscoverModels() ([]string, error) {
	path, err := UserSettingsPath()
	if err != nil {
		return nil, err
	}
	f, err := readRawFile(path)
	if err != nil {
		return nil, err
	}
	return f.AvailableModels, nil
}
