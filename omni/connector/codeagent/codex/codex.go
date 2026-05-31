package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/codex/settings"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
	connlog "github.com/Shaik-Sirajuddin/memory/connector/codeagent/log"
	rootsandbox "github.com/Shaik-Sirajuddin/memory/sandbox"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

var config codeagent.ConfigPaths = codeagent.ConfigPaths{
	GlobalConfigDirs: []string{
		".codex",
	},
	WorkspaceConfigDirs: []string{
		".codex",
	},
	// Binary lists candidate names/paths for the codex CLI binary.
	// lookPath tries each in order and uses the first one found in PATH.
	Binary: []string{
		"codex",
		"/usr/local/bin/codex",
		"/usr/bin/codex",
	},
}

// logger is the package-level structured logger for the codex connector.
// Level is driven by OmniConfig.Dev.Debug (Debug when true, Info otherwise).
var logger = connlog.NewLogger("codex")

// ============================================================
// Available models
// ============================================================

// ModelID is a Codex-compatible OpenAI model identifier.
type ModelID = string

const (
	ModelO4Mini    ModelID = "o4-mini"
	ModelO3        ModelID = "o3"
	ModelGPT41     ModelID = "gpt-4.1"
	ModelGPT41Mini ModelID = "gpt-4.1-mini"
	ModelGPT4o     ModelID = "gpt-4o"
)

// StaticModels is the built-in list of models known to work with Codex.
// Returned by FetchModels when the CLI cannot provide a dynamic list.
var StaticModels = []ModelID{ModelO4Mini, ModelO3, ModelGPT41, ModelGPT41Mini, ModelGPT4o}

// DefaultModel is used when the caller does not specify a model.
const DefaultModel = ModelO4Mini

// ============================================================
// PTY client
// ============================================================

// Option configures a codexAgent at construction time.
type Option func(*codexAgent)

// WithPTYClient attaches a PTY daemon client so that ExecInSession can write
// prompts into an active interactive session. It overrides the c param of New.
func WithPTYClient(c codeagent.PTYClient) Option {
	return func(a *codexAgent) { a.ptyClient = c }
}

// ============================================================
// Agent struct
// ============================================================

type codexAgent struct {
	mu              sync.RWMutex
	binPath         string
	workDir         string
	model           string
	sessionID       string
	activeCmd       *exec.Cmd
	masterPTY       *os.File
	writeCh         chan []byte
	sbx             *sandbox.Config
	sbxRuntime      sandbox.SandboxRuntime
	info            codeagent.CodeAgentInfo
	registeredHooks []*hooks.HookData
	resolver        *settings.Resolver
	ptyClient       codeagent.PTYClient
}

// New returns a CodeAgent backed by the local codex CLI binary.
// c is the PTY daemon client used by ExecInSession; pass nil if not needed.
// Optional Option values (e.g. WithPTYClient) may override c.
func New(workDir, model string, c codeagent.PTYClient, opts ...Option) (codeagent.CodeAgent, error) {
	binPath, err := resolveBinary(config.Binary)
	if err != nil {
		return nil, fmt.Errorf("codex: binary not found: %w", err)
	}
	logger.Debug("resolved binary", "binary", "codex", "path", binPath)

	if workDir == "" {
		workDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("codex: resolve workdir: %w", err)
		}
	}
	// let codex default to a model
	// if model == "" {
	// 	model = DefaultModel
	// }

	ver, _ := captureOutput(workDir, nil, binPath, "--version")
	ver = trimSpace(ver)
	logger.Info("codex agent initialised", "workDir", workDir, "model", model, "version", ver)

	a := &codexAgent{
		binPath:   binPath,
		workDir:   workDir,
		model:     model,
		ptyClient: c,
		info:      codeagent.CodeAgentInfo{Provider: Codex, Name: "codex", Version: ver},
		resolver:  settings.New(Codex),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a, nil
}

// SetPTYClient replaces the PTY daemon client after construction.
// Safe for concurrent use; opts passed to New may also set this via WithPTYClient.
func (a *codexAgent) SetPTYClient(c codeagent.PTYClient) {
	a.mu.Lock()
	a.ptyClient = c
	a.mu.Unlock()
}

// ============================================================
// Info / Identity
// ============================================================

func (a *codexAgent) Info() *codeagent.CodeAgentInfo { return &a.info }

// GetUserIdentity verifies login status by running `codex auth status`.
// Exit code 0 means the user is authenticated; any non-zero exit means not logged in.
// No API keys or credential files are read.
func (a *codexAgent) GetUserIdentity() codeagent.UserIdentify {
	a.mu.RLock()
	binPath := a.binPath
	workDir := a.workDir
	a.mu.RUnlock()

	cmd := exec.Command(binPath, "login", "status")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		logger.Debug("GetUserIdentity: not authenticated", "err", err)
		return codeagent.UserIdentify{Authenticated: false}
	}

	displayName := strings.TrimSpace(string(out))
	logger.Debug("GetUserIdentity: authenticated", "status", displayName)
	return codeagent.UserIdentify{Authenticated: true, DisplayName: displayName}
}

// ============================================================
// Capabilities / Defaults / UpdateDefaults
// ============================================================

func (a *codexAgent) Capabilities() (*codeagent.Capabilities, error) {
	return &codeagent.Capabilities{
		Hooks: &hooks.Capabilities{
			PreToolUse: true, PostToolUse: true, PostToolUseFailure: true,
			SessionStart: true, SessionEnd: false,
			PrePrompt: true, PostPrompt: false,
		},
		Streaming: true, MCPSupport: false, Worktrees: false, Subagents: false,
	}, nil
}

// Defaults reads the current defaults from ~/.codex/config.toml via the
// SettingsResolver, falling back to in-memory values when the file is absent
// or a key is missing.
func (a *codexAgent) Defaults() (*codeagent.Config, error) {
	a.mu.RLock()
	model := a.model
	sbx := a.sbx
	a.mu.RUnlock()

	s, err := a.resolver.GetUserSettings()
	if err != nil {
		logger.Warn("Defaults: could not read user settings, using in-memory values", "err", err)
	} else {
		if s.Config.Model.Model != "" {
			model = s.Config.Model.Model
		}
		if s.Config.Sandbox != nil {
			sbx = s.Config.Sandbox
		}
	}

	return &codeagent.Config{
		Model:          codeagent.Model{Provider: Codex, Model: model},
		PermissionMode: codeagent.PermissionDefault,
		Sandbox:        sbx,
	}, nil
}

// UpdateDefaults applies cfg to in-memory state and persists the changes to
// ~/.codex/config.toml via the SettingsResolver.
func (a *codexAgent) UpdateDefaults(cfg *codeagent.Config) error {
	if cfg == nil {
		return fmt.Errorf("codex: UpdateDefaults: nil config")
	}
	a.mu.Lock()
	if cfg.Model.Model != "" {
		a.model = cfg.Model.Model
	}
	if cfg.Sandbox != nil {
		a.sbx = cfg.Sandbox
	}
	snap := &codeagent.Settings{
		Provider: Codex,
		Config: codeagent.Config{
			Model:   codeagent.Model{Provider: Codex, Model: a.model},
			Sandbox: a.sbx,
		},
	}
	a.mu.Unlock()

	if err := a.resolver.SaveDefaultSettings(snap); err != nil {
		return fmt.Errorf("codex: UpdateDefaults: %w", err)
	}
	logger.Info("UpdateDefaults applied", "model", snap.Config.Model.Model)
	return nil
}

// ============================================================
// FetchModels — dynamic model list via codex CLI
//
// Codex CLI does not expose a dedicated models list command.
// We attempt `codex models --json` and `codex model list`; on any
// failure we return the curated StaticModels list.
// No HTTP calls are made.
// ============================================================

func (a *codexAgent) FetchModels(ctx context.Context) ([]ModelID, error) {
	a.mu.RLock()
	binPath := a.binPath
	workDir := a.workDir
	a.mu.RUnlock()

	// Attempt 1: codex models --json
	if ids, err := fetchModelsViaCmd(ctx, workDir, binPath, "models", "--json"); err == nil {
		logger.Info("FetchModels: resolved via 'codex models --json'", "count", len(ids))
		return ids, nil
	}

	// Attempt 2: codex model list
	if ids, err := fetchModelsViaCmd(ctx, workDir, binPath, "model", "list"); err == nil {
		logger.Info("FetchModels: resolved via 'codex model list'", "count", len(ids))
		return ids, nil
	}

	// Attempt 3: interactive `/models` output capture and parse.
	if ids, err := fetchModelsViaInteractive(ctx, workDir, binPath); err == nil && len(ids) > 0 {
		logger.Info("FetchModels: resolved via interactive '/models'", "count", len(ids))
		return ids, nil
	}

	logger.Warn("FetchModels: CLI model listing unavailable, using static list")
	return StaticModels, nil
}

// fetchModelsViaCmd runs the given CLI command and parses model IDs from its
// output. Accepts both JSON array `["m1","m2"]` and plain newline-delimited output.
func fetchModelsViaCmd(ctx context.Context, workDir string, name string, args ...string) ([]ModelID, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("codex: fetch models cmd: %w", err)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, errors.New("codex: fetch models cmd: empty output")
	}

	// Try JSON array first.
	if strings.HasPrefix(raw, "[") {
		if ids := parseJSONStringSlice(raw); len(ids) > 0 {
			return ids, nil
		}
	}

	// Fall back to one model ID per line.
	var ids []ModelID
	for _, line := range strings.Split(raw, "\n") {
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("codex: fetch models cmd: no models in output")
	}
	return ids, nil
}

// fetchModelsViaInteractive runs interactive Codex, sends `/models`, pipes the
// captured terminal output to a temp file, and parses model IDs from that log.
func fetchModelsViaInteractive(ctx context.Context, workDir, binPath string) ([]ModelID, error) {
	ictx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	// Try common exit slash commands after /models so the process can terminate
	// naturally on CLIs that support one of them.
	stdinScript := "/models\n/exit\n/quit\n"
	cmd := exec.CommandContext(ictx, binPath)
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(stdinScript)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("codex: interactive /models: %w", err)
	}

	raw := string(out)
	// Pipe output to a file first so parsing logic can consume deterministic input.
	if f, ferr := os.CreateTemp("", "codex-models-*.log"); ferr == nil {
		_, _ = f.Write(out)
		_ = f.Close()
		if data, rerr := os.ReadFile(f.Name()); rerr == nil {
			raw = string(data)
		}
		_ = os.Remove(f.Name())
	}

	ids := extractModelIDs(raw)
	if len(ids) == 0 {
		return nil, errors.New("codex: interactive /models: no model ids parsed")
	}
	return ids, nil
}

func extractModelIDs(raw string) []ModelID {
	seen := map[string]struct{}{}
	var ids []ModelID
	tokens := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ' ', '\n', '\r', '\t', ',', ';', ':', '"', '\'', '`', '(', ')', '[', ']', '{', '}', '<', '>':
			return true
		default:
			return false
		}
	})
	for _, t := range tokens {
		token := strings.TrimSpace(strings.ToLower(t))
		if !looksLikeModelID(token) {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		ids = append(ids, ModelID(token))
	}
	return ids
}

func looksLikeModelID(token string) bool {
	if strings.HasPrefix(token, "gpt-") && len(token) > len("gpt-") {
		return true
	}
	// Covers common `o`-series model ids like `o3`, `o4-mini`, `o4-mini-high`.
	if strings.HasPrefix(token, "o") && len(token) > 1 {
		c := token[1]
		if c >= '0' && c <= '9' {
			return true
		}
	}
	return false
}

// ============================================================
// HookManager
// ============================================================

func (a *codexAgent) SupportedHooks() (*hooks.Capabilities, error) {
	return &hooks.Capabilities{
		PreToolUse: true, PostToolUse: true, PostToolUseFailure: true,
		SessionStart: true, SessionEnd: false,
		PrePrompt: true, PostPrompt: false,
	}, nil
}

// Register adds the hook to the in-memory list and appends it to the
// appropriate .codex/hooks.json file (global or workspace).
func (a *codexAgent) Register(p hooks.RegisterHookParams) error {
	if p.Data == nil {
		return errors.New("codex: register hook: nil HookData")
	}
	a.mu.Lock()
	a.registeredHooks = append(a.registeredHooks, p.Data)
	workDir := a.workDir
	a.mu.Unlock()

	filePath, err := hooksFilePath(p.Data.Path, workDir)
	if err != nil {
		return fmt.Errorf("codex: register hook: resolve path: %w", err)
	}
	hf, err := readHooksFile(filePath)
	if err != nil {
		return fmt.Errorf("codex: register hook: read hooks file: %w", err)
	}
	hf.Hooks = append(hf.Hooks, hookDataToEntry(p.Data))
	if err := writeHooksFile(filePath, hf); err != nil {
		return fmt.Errorf("codex: register hook: write hooks file: %w", err)
	}

	logger.Info("Register: hook registered", "uid", p.Data.UID, "id", p.Data.Info.ID, "file", filePath)
	return nil
}

// GetRegisteredHooks reads hooks from both the global ~/.codex/hooks.json and
// the workspace .codex/hooks.json, merges them with the in-memory list, and
// returns a deduplicated slice (by UID). File entries take precedence so that
// hooks registered outside this agent instance are also visible.
func (a *codexAgent) GetRegisteredHooks() []*hooks.HookData {
	a.mu.RLock()
	workDir := a.workDir
	inMem := make([]*hooks.HookData, len(a.registeredHooks))
	copy(inMem, a.registeredHooks)
	a.mu.RUnlock()

	seen := map[string]struct{}{}
	var merged []*hooks.HookData

	// Helper: append entries from a hooks file under a given path tag.
	addFromFile := func(filePath string, path hooks.HookPath) {
		hf, err := readHooksFile(filePath)
		if err != nil {
			logger.Warn("GetRegisteredHooks: could not read hooks file", "path", filePath, "err", err)
			return
		}
		for _, e := range hf.Hooks {
			if _, dup := seen[e.UID]; dup {
				continue
			}
			seen[e.UID] = struct{}{}
			merged = append(merged, entryToHookData(e, path))
		}
	}

	// 1. Global hooks.
	if globalDir, err := globalCodexDir(); err == nil {
		addFromFile(filepath.Join(globalDir, "hooks.json"), hooks.HookPath{Global: true})
	}

	// 2. Workspace hooks.
	addFromFile(filepath.Join(workDir, ".codex", "hooks.json"), hooks.HookPath{WorkspaceDir: &workDir})

	// 3. In-memory hooks not already found in files.
	for _, h := range inMem {
		if _, dup := seen[h.UID]; dup {
			continue
		}
		seen[h.UID] = struct{}{}
		merged = append(merged, h)
	}

	return merged
}

// DeleteHook removes the hook matching p.UID from the in-memory list and from
// the appropriate .codex/hooks.json file (global or workspace per p flags).
func (a *codexAgent) DeleteHook(p hooks.DeleteHookParams) (bool, error) {
	a.mu.Lock()
	workDir := a.workDir
	found := false
	for i, h := range a.registeredHooks {
		if h.UID == p.UID {
			a.registeredHooks = append(a.registeredHooks[:i], a.registeredHooks[i+1:]...)
			found = true
			break
		}
	}
	a.mu.Unlock()

	// Always attempt to remove from the hooks file regardless of in-memory state,
	// so hooks registered outside this process are also cleaned up.
	path := hooks.HookPath{Global: p.Global, WorkspaceDir: p.WorkspaceDir}
	filePath, err := hooksFilePath(path, workDir)
	if err != nil {
		logger.Warn("DeleteHook: could not resolve hooks file path", "err", err)
	} else {
		hf, readErr := readHooksFile(filePath)
		if readErr == nil {
			before := len(hf.Hooks)
			filtered := hf.Hooks[:0]
			for _, e := range hf.Hooks {
				if e.UID != p.UID {
					filtered = append(filtered, e)
				}
			}
			hf.Hooks = filtered
			if len(hf.Hooks) < before {
				found = true
				if writeErr := writeHooksFile(filePath, hf); writeErr != nil {
					logger.Error("DeleteHook: could not write hooks file", "err", writeErr)
				}
			}
		}
	}

	if !found {
		logger.Warn("DeleteHook: hook not found", "uid", p.UID)
		return false, fmt.Errorf("codex: delete hook: uid %q not found", p.UID)
	}
	logger.Info("DeleteHook: removed", "uid", p.UID)
	return true, nil
}

// ============================================================
// SettingsResolver — delegates to the embedded settings.Resolver
// ============================================================

func (a *codexAgent) GetUserSettings() (*codeagent.Settings, error) {
	return a.resolver.GetUserSettings()
}

func (a *codexAgent) GetWorkspaceSettings(dir sandbox.WorkspaceDir) (*codeagent.Settings, error) {
	return a.resolver.GetWorkspaceSettings(dir)
}

func (a *codexAgent) SaveDefaultSettings(s *codeagent.Settings) error {
	return a.resolver.SaveDefaultSettings(s)
}

func (a *codexAgent) WatchDefaultSettings(cb func(*codeagent.Settings)) error {
	return a.resolver.WatchDefaultSettings(cb)
}

func (a *codexAgent) Discover() (codeagent.DiscoverResult, error) {
	models, err := a.FetchModels(context.Background())
	if err != nil {
		return codeagent.DiscoverResult{
			Models: []codeagent.ModelID{codeagent.ModelID(DefaultModel)},
		}, nil
	}
	if len(models) == 0 {
		return codeagent.DiscoverResult{
			Models: []codeagent.ModelID{codeagent.ModelID(DefaultModel)},
		}, nil
	}
	out := make([]codeagent.ModelID, 0, len(models))
	for _, m := range models {
		out = append(out, codeagent.ModelID(m))
	}
	return codeagent.DiscoverResult{Models: out}, nil
}

func (a *codexAgent) syncSandboxRuntimeLocked() error {
	if a.sbx == nil {
		a.sbxRuntime = nil
		return nil
	}

	if a.sbxRuntime != nil {
		return a.sbxRuntime.Sync(a.sbx)
	}

	supported := rootsandbox.SupportedProvisioners(runtime.GOOS)
	if len(supported) == 0 {
		return fmt.Errorf("codex: sandbox runtime: no supported provisioner for %s", runtime.GOOS)
	}

	provisioner, err := rootsandbox.NewProvisioner(supported[0], &sandbox.Sandbox{
		Config: a.sbx,
	}, rootsandbox.ProvisionerOptions{
		WorkDir: a.workDir,
	})
	if err != nil {
		return fmt.Errorf("codex: sandbox runtime: create provisioner: %w", err)
	}

	id := a.sessionID
	if strings.TrimSpace(id) == "" {
		id = "codex-sandbox-" + generateID()
	}
	rt, err := provisioner.Create(rootsandbox.CreateSandboxParams{
		ID:     id,
		Config: a.sbx,
	})
	if err != nil {
		return fmt.Errorf("codex: sandbox runtime: create runtime: %w", err)
	}

	a.sbxRuntime = rt
	return nil
}
