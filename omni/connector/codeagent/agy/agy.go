package agy
import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/agy/settings"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
	agylog "github.com/Shaik-Sirajuddin/memory/connector/codeagent/log"
	rootsandbox "github.com/Shaik-Sirajuddin/memory/sandbox"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

var Config codeagent.ConfigPaths = codeagent.ConfigPaths{
	GlobalConfigDirs: []string{
		".agy",
		".config/agy",
	},
	WorkspaceConfigDirs: []string{
		".agy",
	},
	Binary: []string{
		"agy",
		"/usr/local/bin/agy",
		"/opt/homebrew/bin/agy",
		"~/.local/bin/agy",
	},
}

// logger is the package-level structured logger for the agy connector.
var logger = agylog.NewLogger("agy")

// ============================================================
// Available models
// ============================================================

// ModelID is a Agy model identifier.
type ModelID = string

const (
	ModelGeminiPro   ModelID = "gemini-1-5-pro"
	ModelGeminiFlash ModelID = "gemini-1-5-flash"
)

// StaticModels is the curated list of Agy models.
var StaticModels = []ModelID{ModelGeminiPro, ModelGeminiFlash}

// DefaultModel is used when the caller does not specify a model.
const DefaultModel = ModelGeminiPro

// ============================================================
// Agent struct
// ============================================================

type agyAgent struct {
	*AgyParser
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

// New returns a CodeAgent backed by the local agy CLI binary.
// workDir defaults to the process working directory; model defaults to DefaultModel.
// c is the PTY daemon client used by ExecInSession; pass nil to disable PTY support.
func New(workDir, model string, c codeagent.PTYClient) (codeagent.CodeAgent, error) {
	binPath, err := lookPath("agy")
	if err != nil {
		return nil, fmt.Errorf("agy: binary not found in PATH: %w", err)
	}
	logger.Debug("resolved binary", "binary", "agy", "path", binPath)

	if workDir == "" {
		workDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("agy: resolve workdir: %w", err)
		}
	}
	if model == "" {
		model = DefaultModel
	}

	ver, _ := captureOutput(workDir, binPath, "--version")
	ver = strings.TrimSpace(ver)
	logger.Info("agy agent initialised", "workDir", workDir, "model", model, "version", ver)

	a := &agyAgent{
		AgyParser: &AgyParser{},
		resolver:     settings.New(Agy),
		binPath:      binPath,
		workDir:      workDir,
		model:        model,
		permMode:     codeagent.PermissionDefault,
		ptyClient:    c,
		info:         codeagent.CodeAgentInfo{Provider: Agy, Name: "agy", Version: ver},
	}
	return a, nil
}

// SetPTYClient wires the PTY daemon client used by ExecInSession.
// Call this after New() when interactive PTY support is needed.
func (a *agyAgent) SetPTYClient(c codeagent.PTYClient) {
	a.mu.Lock()
	a.ptyClient = c
	a.mu.Unlock()
}

// ============================================================
// Info / Identity
// ============================================================

func (a *agyAgent) Info() *codeagent.CodeAgentInfo { return &a.info }

// GetUserIdentity checks login status.
// TODO: agy CLI currently does not support an 'auth status' command.
// The below code is commented out to prevent hanging the session by dropping into an interactive REPL.
func (a *agyAgent) GetUserIdentity() codeagent.UserIdentify {
	/*
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

	// `agy auth status` outputs JSON; parse email if present.
	raw := strings.TrimSpace(string(out))
	identity := parseAuthStatus(raw)
	logger.Debug("GetUserIdentity: authenticated", "email", identity.Email)
	return identity
	*/
	return codeagent.UserIdentify{Authenticated: true}
}

// ============================================================
// Capabilities / Defaults / UpdateDefaults
// ============================================================

func (a *agyAgent) Capabilities() (*codeagent.Capabilities, error) {
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

// TODO: read defaults from ~/.agy/settings.json
func (a *agyAgent) Defaults() (*codeagent.Config, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return &codeagent.Config{
		Model:          codeagent.Model{Provider: Agy, Model: a.model},
		PermissionMode: a.permMode,
		Hooks:          nil,
		Sandbox:        a.sbx,
	}, nil
}

// TODO: persist to ~/.agy/settings.json
func (a *agyAgent) UpdateDefaults(cfg *codeagent.Config) error {
	if cfg == nil {
		return errors.New("agy: UpdateDefaults: nil config")
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

func (a *agyAgent) GetUserSettings() (*codeagent.Settings, error) {
	path, err := agyUserSettingsPath()
	if err != nil {
		return nil, err
	}
	return readAgySettings(path)
}

func (a *agyAgent) GetWorkspaceSettings(dir sandbox.WorkspaceDir) (*codeagent.Settings, error) {
	return readAgySettings(agyWorkspaceSettingsPath(string(dir)))
}

func (a *agyAgent) SaveDefaultSettings(s *codeagent.Settings) error {
	path, err := agyUserSettingsPath()
	if err != nil {
		return err
	}
	return writeAgySettings(path, s)
}

func (a *agyAgent) WatchDefaultSettings(fn func(*codeagent.Settings)) error {
	return a.resolver.WatchDefaultSettings(func(_ *codeagent.Settings) {
		cfg, err := a.GetUserSettings()
		if err == nil {
			fn(cfg)
		}
	})
}

// Discover returns the list of available Agy models using a three-tier strategy:
//  1. Run `agy /model` (piped stdin) and parse the interactive model list output.
//  2. Fall back to availableModels in the user settings.json.
//  3. Fall back to StaticModels.
func (a *agyAgent) Discover() (codeagent.DiscoverResult, error) {
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

func (a *agyAgent) syncSandboxRuntimeLocked() error {
	if a.sbx == nil {
		a.sbxRuntime = nil
		return nil
	}

	if a.sbxRuntime != nil {
		return a.sbxRuntime.Sync(a.sbx)
	}

	supported := rootsandbox.SupportedProvisioners(runtime.GOOS)
	if len(supported) == 0 {
		return fmt.Errorf("agy: sandbox runtime: no supported provisioner for %s", runtime.GOOS)
	}

	provisioner, err := rootsandbox.NewProvisioner(supported[0], &sandbox.Sandbox{
		Config: a.sbx,
	}, rootsandbox.ProvisionerOptions{
		WorkDir: a.workDir,
	})
	if err != nil {
		return fmt.Errorf("agy: sandbox runtime: create provisioner: %w", err)
	}

	id := a.sessionID
	if strings.TrimSpace(id) == "" {
		id = "agy-sandbox-" + generateID()
	}
	rt, err := provisioner.Create(rootsandbox.CreateSandboxParams{
		ID:     id,
		Config: a.sbx,
	})
	if err != nil {
		return fmt.Errorf("agy: sandbox runtime: create runtime: %w", err)
	}

	a.sbxRuntime = rt
	return nil
}
