package configsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

const (
	ProviderAgy              codeagent.Provider = "agy"
	defaultSettingsPollEvery                    = time.Second
)

var (
	ErrMissingSettingsResolver = errors.New("configsync: missing settings resolver")
	ErrMissingWorkspace        = errors.New("configsync: missing workspace dir")
	ErrUnsupportedProvider     = errors.New("configsync: unsupported settings provider")
)

type watchStopper interface {
	StopWatch()
}

// SettingsSyncTarget describes one agent settings file pair to keep synchronized.
type SettingsSyncTarget struct {
	AgentID               string
	Provider              codeagent.Provider
	Resolver              codeagent.SettingsResolver
	WorkspaceDir          string
	WorkspaceSettingsPath string
	PollInterval          time.Duration
}

func (t SettingsSyncTarget) validate() error {
	if t.AgentID == "" {
		return ErrMissingAgentID
	}
	if t.Resolver == nil {
		return ErrMissingSettingsResolver
	}
	if t.WorkspaceDir == "" && t.WorkspaceSettingsPath == "" {
		return ErrMissingWorkspace
	}
	if t.Provider != "" && t.Provider != ProviderAgy {
		return fmt.Errorf("%w: %s", ErrUnsupportedProvider, t.Provider)
	}
	return nil
}

func (t SettingsSyncTarget) workspacePath() string {
	if t.WorkspaceSettingsPath != "" {
		return t.WorkspaceSettingsPath
	}
	return filepath.Join(t.WorkspaceDir, ".agy", "settings.json")
}

func (t SettingsSyncTarget) pollInterval() time.Duration {
	if t.PollInterval > 0 {
		return t.PollInterval
	}
	return defaultSettingsPollEvery
}

type settingsSync struct {
	target SettingsSyncTarget

	mu            sync.Mutex
	cancel        context.CancelFunc
	global        settingsSnapshot
	workspace     settingsSnapshot
	workspaceStat fileState
}

func newSettingsSync(target SettingsSyncTarget) *settingsSync {
	if target.Provider == "" {
		target.Provider = ProviderAgy
	}
	return &settingsSync{target: target}
}

func (s *settingsSync) start(parent context.Context) error {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.mu.Unlock()

	if err := s.startDefaultWatcher(ctx); err != nil {
		cancel()
		return err
	}
	s.refreshWorkspaceStat()
	go s.watchWorkspace(ctx)
	return nil
}

func (s *settingsSync) stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if stopper, ok := s.target.Resolver.(watchStopper); ok {
		stopper.StopWatch()
	}
}

func (s *settingsSync) syncDefaultToWorkspace() error {
	settings, err := s.target.Resolver.GetUserSettings()
	if err != nil {
		return fmt.Errorf("configsync: read agy default settings: %w", err)
	}
	snap := snapshotSettings(settings)

	s.mu.Lock()
	if snap.equal(s.workspace) {
		s.global = snap
		s.mu.Unlock()
		return nil
	}
	s.global = snap
	s.workspace = snap
	s.mu.Unlock()

	if err := writeAgyWorkspaceSettings(s.target.workspacePath(), settings); err != nil {
		return err
	}
	s.refreshWorkspaceStat()
	return nil
}

func (s *settingsSync) startDefaultWatcher(ctx context.Context) error {
	return s.target.Resolver.WatchDefaultSettings(func(settings *codeagent.Settings) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_ = s.syncWorkspaceFromDefault(settings)
	})
}

func (s *settingsSync) syncWorkspaceFromDefault(settings *codeagent.Settings) error {
	snap := snapshotSettings(settings)

	s.mu.Lock()
	if snap.equal(s.workspace) {
		s.global = snap
		s.mu.Unlock()
		return nil
	}
	s.global = snap
	s.workspace = snap
	s.mu.Unlock()

	if err := writeAgyWorkspaceSettings(s.target.workspacePath(), settings); err != nil {
		return err
	}
	s.refreshWorkspaceStat()
	return nil
}

func (s *settingsSync) watchWorkspace(ctx context.Context) {
	ticker := time.NewTicker(s.target.pollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.workspaceChanged() {
				_ = s.syncDefaultFromWorkspace()
			}
		}
	}
}

func (s *settingsSync) syncDefaultFromWorkspace() error {
	settings, err := s.target.Resolver.GetWorkspaceSettings(sandbox.WorkspaceDir(s.target.WorkspaceDir))
	if err != nil {
		return fmt.Errorf("configsync: read agy workspace settings: %w", err)
	}
	snap := snapshotSettings(settings)

	s.mu.Lock()
	if snap.equal(s.global) {
		s.workspace = snap
		s.mu.Unlock()
		return nil
	}
	s.workspace = snap
	s.global = snap
	s.mu.Unlock()

	if err := s.target.Resolver.SaveDefaultSettings(settings); err != nil {
		return fmt.Errorf("configsync: write agy default settings: %w", err)
	}
	return nil
}

func (s *settingsSync) refreshWorkspaceStat() {
	state := statFile(s.target.workspacePath())
	s.mu.Lock()
	s.workspaceStat = state
	s.mu.Unlock()
}

func (s *settingsSync) workspaceChanged() bool {
	next := statFile(s.target.workspacePath())
	s.mu.Lock()
	defer s.mu.Unlock()
	if next.equal(s.workspaceStat) {
		return false
	}
	s.workspaceStat = next
	return true
}

type fileState struct {
	exists  bool
	size    int64
	modTime time.Time
}

func statFile(path string) fileState {
	info, err := os.Stat(path)
	if err != nil {
		return fileState{}
	}
	return fileState{exists: true, size: info.Size(), modTime: info.ModTime()}
}

func (s fileState) equal(other fileState) bool {
	return s.exists == other.exists && s.size == other.size && s.modTime.Equal(other.modTime)
}

type settingsSnapshot struct {
	model          string
	permissionMode codeagent.PermissionMode
	sandboxJSON    string
}

func snapshotSettings(s *codeagent.Settings) settingsSnapshot {
	if s == nil {
		return settingsSnapshot{}
	}
	snap := settingsSnapshot{
		model:          s.Config.Model.Model,
		permissionMode: s.Config.PermissionMode,
	}
	if s.Config.Sandbox != nil {
		if data, err := json.Marshal(s.Config.Sandbox); err == nil {
			snap.sandboxJSON = string(data)
		}
	}
	return snap
}

func (s settingsSnapshot) equal(other settingsSnapshot) bool {
	return s.model == other.model &&
		s.permissionMode == other.permissionMode &&
		s.sandboxJSON == other.sandboxJSON
}

func writeAgyWorkspaceSettings(path string, settings *codeagent.Settings) error {
	raw := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("configsync: decode agy workspace settings %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("configsync: read agy workspace settings %s: %w", path, err)
	}

	if err := applySettingsToAgyRaw(raw, settings); err != nil {
		return err
	}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("configsync: marshal agy workspace settings %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("configsync: mkdir agy workspace settings dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("configsync: write agy workspace settings %s: %w", path, err)
	}
	return nil
}

func applySettingsToAgyRaw(raw map[string]json.RawMessage, settings *codeagent.Settings) error {
	if settings == nil {
		return nil
	}
	if settings.Config.Model.Model != "" {
		data, err := json.Marshal(settings.Config.Model.Model)
		if err != nil {
			return fmt.Errorf("configsync: marshal agy model: %w", err)
		}
		raw["model"] = data
	}
	if settings.Config.PermissionMode != "" {
		data, err := json.Marshal(map[string]string{
			"defaultMode": string(settings.Config.PermissionMode),
		})
		if err != nil {
			return fmt.Errorf("configsync: marshal agy permissions: %w", err)
		}
		raw["permissions"] = data
	}
	if settings.Config.Sandbox != nil {
		data, err := json.Marshal(agySandboxFromConfig(settings.Config.Sandbox))
		if err != nil {
			return fmt.Errorf("configsync: marshal agy sandbox: %w", err)
		}
		raw["sandbox"] = data
	}
	return nil
}

type agySandboxSettings struct {
	Enabled    *bool                       `json:"enabled,omitempty"`
	Filesystem *agySandboxFilesystemConfig `json:"filesystem,omitempty"`
}

type agySandboxFilesystemConfig struct {
	AllowWrite []string `json:"allowWrite,omitempty"`
	DenyWrite  []string `json:"denyWrite,omitempty"`
}

func agySandboxFromConfig(cfg *sandbox.Config) *agySandboxSettings {
	enabled := true
	result := &agySandboxSettings{Enabled: &enabled}
	if cfg == nil || cfg.AgentPolicy == nil {
		return result
	}

	filesystem := &agySandboxFilesystemConfig{}
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
