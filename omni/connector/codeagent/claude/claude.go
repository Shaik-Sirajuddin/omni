package claude

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/claude/settings"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
	claudelog "github.com/Shaik-Sirajuddin/memory/connector/codeagent/log"
	rootsandbox "github.com/Shaik-Sirajuddin/memory/sandbox"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

var Config codeagent.ConfigPaths = codeagent.ConfigPaths{
	GlobalConfigDirs: []string{
		".claude",
		".config/claude",
	},
	WorkspaceConfigDirs: []string{
		".claude",
	},
	Binary: []string{
		"claude",
		"/usr/local/bin/claude",
		"/opt/homebrew/bin/claude",
	},
}

// logger is the package-level structured logger for the claude connector.
var logger = claudelog.NewLogger("claude")

// ============================================================
// Available models
// ============================================================

// ModelID is a Claude model identifier.
type ModelID = string

const (
	ModelOpus4   ModelID = "claude-opus-4-6"
	ModelSonnet4 ModelID = "claude-sonnet-4-6"
	ModelHaiku45 ModelID = "claude-haiku-4-5-20251001"
)

// StaticModels is the curated list of Claude models.
var StaticModels = []ModelID{ModelOpus4, ModelSonnet4, ModelHaiku45}

// DefaultModel is used when the caller does not specify a model.
const DefaultModel = ModelSonnet4

// ============================================================
// Agent struct
// ============================================================

type claudeAgent struct {
	*ClaudeParser
	resolver        *settings.Resolver
	mu              sync.RWMutex
	binPath         string
	workDir         string
	model           string
	permMode        codeagent.PermissionMode
	systemPrompt    string
	sessionID       string
	sbx             *sandbox.Config
	sbxRuntime      sandbox.SandboxRuntime
	ptyClient       codeagent.PTYClient
	info            codeagent.CodeAgentInfo
	registeredHooks []*hooks.HookData
}

// New returns a CodeAgent backed by the local claude CLI binary.
// workDir defaults to the process working directory; model defaults to DefaultModel.
// c is the PTY daemon client used by ExecInSession; pass nil to disable PTY support.
func New(workDir, model string, c codeagent.PTYClient) (codeagent.CodeAgent, error) {
	binPath, err := lookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("claude: binary not found in PATH: %w", err)
	}
	logger.Debug("resolved binary", "binary", "claude", "path", binPath)

	if workDir == "" {
		workDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("claude: resolve workdir: %w", err)
		}
	}
	if model == "" {
		model = DefaultModel
	}

	ver, _ := captureOutput(workDir, binPath, "--version")
	ver = strings.TrimSpace(ver)
	logger.Info("claude agent initialised", "workDir", workDir, "model", model, "version", ver)

	a := &claudeAgent{
		ClaudeParser: &ClaudeParser{},
		resolver:     settings.New(Claude),
		binPath:      binPath,
		workDir:      workDir,
		model:        model,
		permMode:     codeagent.PermissionDefault,
		ptyClient:    c,
		info:         codeagent.CodeAgentInfo{Provider: Claude, Name: "claude", Version: ver},
	}
	return a, nil
}

// SetPTYClient wires the PTY daemon client used by ExecInSession.
// Call this after New() when interactive PTY support is needed.
func (a *claudeAgent) SetPTYClient(c codeagent.PTYClient) {
	a.mu.Lock()
	a.ptyClient = c
	a.mu.Unlock()
}

// ============================================================
// Info / Identity
// ============================================================

func (a *claudeAgent) Info() *codeagent.CodeAgentInfo { return &a.info }

// GetUserIdentity checks login status via `claude auth status`.
// Exit code 0 means authenticated; non-zero means not logged in.
func (a *claudeAgent) GetUserIdentity() codeagent.UserIdentify {
	a.mu.RLock()
	binPath := a.binPath
	workDir := a.workDir
	a.mu.RUnlock()

	cmd := exec.Command(binPath, "auth", "status")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		logger.Debug("GetUserIdentity: not authenticated", "err", err)
		return codeagent.UserIdentify{Authenticated: false}
	}

	// `claude auth status` outputs JSON; parse email if present.
	raw := strings.TrimSpace(string(out))
	identity := parseAuthStatus(raw)
	logger.Debug("GetUserIdentity: authenticated", "email", identity.Email)
	return identity
}

// ============================================================
// Capabilities / Defaults / UpdateDefaults
// ============================================================

func (a *claudeAgent) Capabilities() (*codeagent.Capabilities, error) {
	return &codeagent.Capabilities{
		Hooks: &hooks.Capabilities{
			PreToolUse:         true,
			PostToolUse:        true,
			PostToolUseFailure: true,
			SessionStart:       true,
			SessionEnd:         true,
			PrePrompt:          true,
			PostPrompt:         false,
		},
		Streaming:  true,
		MCPSupport: true,
		Worktrees:  true,
		Subagents:  true,
	}, nil
}

// TODO: read defaults from ~/.claude/settings.json
func (a *claudeAgent) Defaults() (*codeagent.Config, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return &codeagent.Config{
		Model:          codeagent.Model{Provider: Claude, Model: a.model},
		PermissionMode: a.permMode,
		Hooks:          nil,
		Sandbox:        a.sbx,
	}, nil
}

// TODO: persist to ~/.claude/settings.json
func (a *claudeAgent) UpdateDefaults(cfg *codeagent.Config) error {
	if cfg == nil {
		return errors.New("claude: UpdateDefaults: nil config")
	}
	a.mu.Lock()
	if cfg.Model.Model != "" {
		a.model = cfg.Model.Model
	}
	if cfg.PermissionMode != "" {
		a.permMode = cfg.PermissionMode
	}
	if cfg.Sandbox != nil {
		a.sbx = cfg.Sandbox
	}
	a.mu.Unlock()
	logger.Info("UpdateDefaults applied", "model", a.model, "permMode", a.permMode)
	return nil
}

// ============================================================
// SettingsResolver — delegated to settings.Resolver
// ============================================================

func (a *claudeAgent) GetUserSettings() (*codeagent.Settings, error) {
	path, err := claudeUserSettingsPath()
	if err != nil {
		return nil, err
	}
	return readClaudeSettings(path)
}

func (a *claudeAgent) GetWorkspaceSettings(dir sandbox.WorkspaceDir) (*codeagent.Settings, error) {
	return readClaudeSettings(claudeWorkspaceSettingsPath(string(dir)))
}

func (a *claudeAgent) SaveDefaultSettings(s *codeagent.Settings) error {
	path, err := claudeUserSettingsPath()
	if err != nil {
		return err
	}
	return writeClaudeSettings(path, s)
}

func (a *claudeAgent) WatchDefaultSettings(fn func(*codeagent.Settings)) error {
	return a.resolver.WatchDefaultSettings(func(_ *codeagent.Settings) {
		cfg, err := a.GetUserSettings()
		if err == nil {
			fn(cfg)
		}
	})
}

// Discover returns the list of available Claude models using a three-tier strategy:
//  1. Run `claude /model` (piped stdin) and parse the interactive model list output.
//  2. Fall back to availableModels in the user settings.json.
//  3. Fall back to StaticModels.
func (a *claudeAgent) Discover() (codeagent.DiscoverResult, error) {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()

	// Tier 1: live CLI discovery.
	if ids, err := discoverFromCLI(workDir); err == nil && len(ids) > 0 {
		logger.Debug("Discover: resolved via CLI", "count", len(ids))
		return codeagent.DiscoverResult{Models: ids}, nil
	}

	// Tier 2: settings.json availableModels.
	if models, err := a.resolver.DiscoverModels(); err == nil && len(models) > 0 {
		ids := make([]codeagent.ModelID, len(models))
		for i, m := range models {
			ids[i] = codeagent.ModelID(m)
		}
		logger.Debug("Discover: resolved via settings", "count", len(ids))
		return codeagent.DiscoverResult{Models: ids}, nil
	}

	// Tier 3: static fallback.
	logger.Warn("Discover: using static model list")
	ids := make([]codeagent.ModelID, len(StaticModels))
	for i, m := range StaticModels {
		ids[i] = codeagent.ModelID(m)
	}
	return codeagent.DiscoverResult{Models: ids}, nil
}

func (a *claudeAgent) syncSandboxRuntimeLocked() error {
	if a.sbx == nil {
		a.sbxRuntime = nil
		return nil
	}

	if a.sbxRuntime != nil {
		return a.sbxRuntime.Sync(a.sbx)
	}

	supported := rootsandbox.SupportedProvisioners(runtime.GOOS)
	if len(supported) == 0 {
		return fmt.Errorf("claude: sandbox runtime: no supported provisioner for %s", runtime.GOOS)
	}

	provisioner, err := rootsandbox.NewProvisioner(supported[0], &sandbox.Sandbox{
		Config: a.sbx,
	}, rootsandbox.ProvisionerOptions{
		WorkDir: a.workDir,
	})
	if err != nil {
		return fmt.Errorf("claude: sandbox runtime: create provisioner: %w", err)
	}

	id := a.sessionID
	if strings.TrimSpace(id) == "" {
		id = "claude-sandbox-" + generateID()
	}
	rt, err := provisioner.Create(rootsandbox.CreateSandboxParams{
		ID:     id,
		Config: a.sbx,
	})
	if err != nil {
		return fmt.Errorf("claude: sandbox runtime: create runtime: %w", err)
	}

	a.sbxRuntime = rt
	return nil
}
