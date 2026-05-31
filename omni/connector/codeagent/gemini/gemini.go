package gemini

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
	codeagentlog "github.com/Shaik-Sirajuddin/memory/connector/codeagent/log"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

var Config codeagent.ConfigPaths = codeagent.ConfigPaths{
	GlobalConfigDirs: []string{
		".gemini",
		".config/gemini",
	},
	WorkspaceConfigDirs: []string{
		".gemini",
		".config/gemini",
	},
	Binary: []string{
		"gemini",
		"/usr/local/bin/gemini",
		"/opt/homebrew/bin/gemini",
	},
}

// logger is the package-level structured logger for the gemini connector.
var logger = codeagentlog.NewLogger("gemini")

// ModelID is a Gemini model identifier.
type ModelID = string

const (
	ModelGemini25Pro   ModelID = "gemini-2.5-pro"
	ModelGemini25Flash ModelID = "gemini-2.5-flash"
	ModelGemini20Flash ModelID = "gemini-2.0-flash"
)

// StaticModels is the curated list of Gemini models.
var StaticModels = []ModelID{ModelGemini25Pro, ModelGemini25Flash, ModelGemini20Flash}

// DefaultModel is used when caller does not specify a model.
const DefaultModel = ModelGemini25Flash

type geminiAgent struct {
	mu              sync.RWMutex
	workDir         string
	model           string
	permMode        codeagent.PermissionMode
	systemPrompt    string
	sessionID       string
	sbx             *sandbox.Config
	ptyClient       PTYClient
	masterPTY       *os.File
	info            codeagent.CodeAgentInfo
	settings        *settingsResolver
	activeCmd       *exec.Cmd
	registeredHooks []*hooks.HookData
}

type PTYClient = codeagent.PTYClient

// New returns a CodeAgent backed by the local gemini CLI binary.
func New(workDir, model string, c PTYClient) (codeagent.CodeAgent, error) {
	binPath, err := lookPath("gemini")
	if err != nil {
		return nil, fmt.Errorf("gemini: binary not found in PATH: %w", err)
	}
	logger.Debug("gemini binary located", "path", binPath)

	if workDir == "" {
		workDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("gemini: resolve workdir: %w", err)
		}
	}
	if model == "" {
		model = DefaultModel
	}

	ver, _ := captureOutput(workDir, "gemini", "--version")
	ver = trimSpace(ver)
	logger.Info("gemini agent initialised", "workDir", workDir, "model", model, "version", ver)

	return &geminiAgent{
		workDir:   workDir,
		model:     model,
		permMode:  codeagent.PermissionAcceptEdits,
		ptyClient: c,
		info:      codeagent.CodeAgentInfo{Provider: Gemini, Name: "gemini", Version: ver},
		settings:  newSettingsResolver(),
	}, nil
}

func (a *geminiAgent) SetPTYClient(c PTYClient) {
	a.mu.Lock()
	a.ptyClient = c
	a.mu.Unlock()
}

func (a *geminiAgent) Info() *codeagent.CodeAgentInfo { return &a.info }

// GetUserIdentity uses `gemini --help` reachability as a lightweight auth/availability probe.
func (a *geminiAgent) GetUserIdentity() codeagent.UserIdentify {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()

	cmd := exec.Command("gemini", "--help")
	cmd.Dir = workDir
	if err := cmd.Run(); err != nil {
		logger.Debug("GetUserIdentity: gemini command unavailable", "err", err)
		return codeagent.UserIdentify{Authenticated: false}
	}

	return codeagent.UserIdentify{Authenticated: true}
}

func (a *geminiAgent) Capabilities() (*codeagent.Capabilities, error) {
	return &codeagent.Capabilities{
		Hooks: &hooks.Capabilities{
			PreToolUse:         true,
			PostToolUse:        true,
			PostToolUseFailure: true,
			SessionStart:    true,
			SessionEnd:      true,
			PrePrompt:          true,
			PostPrompt:         true,
		},
		Streaming:  true,
		MCPSupport: true,
		Worktrees:  true,
		Subagents:  true,
	}, nil
}

// Defaults reads ~/.gemini/settings.json and falls back to in-memory values.
func (a *geminiAgent) Defaults() (*codeagent.Config, error) {
	a.mu.RLock()
	model := a.model
	permMode := a.permMode
	sbx := a.sbx
	a.mu.RUnlock()

	cfg, err := readGlobalSettings()
	if err != nil {
		logger.Warn("Defaults: could not read global settings, using in-memory values", "err", err)
	} else {
		if m, ok := cfg["model"]; ok && m != "" {
			model = m
		}
		if pm, ok := cfg["defaultApprovalMode"]; ok && pm != "" {
			if mapped := permissionFromApprovalMode(pm); mapped != "" {
				permMode = mapped
			}
		} else if pm, ok := cfg["approvalMode"]; ok {
			if mapped := permissionFromApprovalMode(pm); mapped != "" {
				permMode = mapped
			}
		}
		if s, ok := cfg["sandbox"]; ok {
			sbx = sandboxFromFlag(s)
		}
	}

	return &codeagent.Config{
		Model:          codeagent.Model{Provider: Gemini, Model: model},
		PermissionMode: permMode,
		Sandbox:        sbx,
	}, nil
}

// UpdateDefaults applies cfg in-memory and writes ~/.gemini/settings.json.
func (a *geminiAgent) UpdateDefaults(cfg *codeagent.Config) error {
	if cfg == nil {
		return fmt.Errorf("gemini: UpdateDefaults: nil config")
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
	model := a.model
	permMode := a.permMode
	sbx := a.sbx
	a.mu.Unlock()

	settings, err := readGlobalSettings()
	if err != nil {
		settings = map[string]string{}
		logger.Warn("UpdateDefaults: could not read global settings, will overwrite", "err", err)
	}
	if model != "" {
		settings["model"] = model
	}
	if am := approvalModeFlag(permMode); am != "" {
		settings["approvalMode"] = am
		settings["defaultApprovalMode"] = am
	}
	if sf := sandboxFlagValue(sbx); sf != "" {
		settings["sandbox"] = sf
	} else {
		delete(settings, "sandbox")
	}

	if err := writeGlobalSettings(settings); err != nil {
		return fmt.Errorf("gemini: UpdateDefaults: persist settings: %w", err)
	}

	logger.Info("UpdateDefaults applied", "model", model, "permissionMode", permMode)
	return nil
}

// FetchModels tries Gemini CLI model listing commands before falling back to StaticModels.
func (a *geminiAgent) FetchModels(ctx context.Context) ([]ModelID, error) {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()

	if ids, err := fetchModelsViaCmd(ctx, workDir, "gemini", "models", "--json"); err == nil {
		logger.Info("FetchModels: resolved via 'gemini models --json'", "count", len(ids))
		return ids, nil
	}

	if ids, err := fetchModelsViaCmd(ctx, workDir, "gemini", "model", "list"); err == nil {
		logger.Info("FetchModels: resolved via 'gemini model list'", "count", len(ids))
		return ids, nil
	}

	logger.Warn("FetchModels: CLI model listing unavailable, using static list")
	return StaticModels, nil
}

func fetchModelsViaCmd(ctx context.Context, workDir string, name string, args ...string) ([]ModelID, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gemini: fetch models cmd: %w", err)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, errors.New("gemini: fetch models cmd: empty output")
	}

	if strings.HasPrefix(raw, "[") {
		if ids := parseJSONStringSlice(raw); len(ids) > 0 {
			return ids, nil
		}
	}

	var ids []ModelID
	for _, line := range strings.Split(raw, "\n") {
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("gemini: fetch models cmd: no models in output")
	}
	return ids, nil
}

// Discover returns available models via Gemini CLI discovery and falls back to static models.
func (a *geminiAgent) Discover() (codeagent.DiscoverResult, error) {
	models, err := a.FetchModels(context.Background())
	if err != nil {
		logger.Warn("Discover: could not fetch models, using static models", "err", err)
		models = StaticModels
	}
	ids := make([]codeagent.ModelID, len(models))
	for i, m := range models {
		ids[i] = codeagent.ModelID(m)
	}
	return codeagent.DiscoverResult{Models: ids}, nil
}

func (a *geminiAgent) SupportedHooks() (*hooks.Capabilities, error) {
	return &hooks.Capabilities{
		PreToolUse: true, PostToolUse: true, PostToolUseFailure: true,
		SessionStart:    true, SessionEnd:      true,
		PrePrompt: true, PostPrompt: true,
	}, nil
}

// Register adds the hook to in-memory list and syncs to settings.json hooks.
func (a *geminiAgent) Register(p hooks.RegisterHookParams) error {
	if p.Data == nil {
		return errors.New("gemini: register hook: nil HookData")
	}
	a.mu.Lock()
	a.registeredHooks = append(a.registeredHooks, p.Data)
	all := copyHooks(a.registeredHooks)
	workDir := a.workDir
	a.mu.Unlock()

	if err := syncHooksToSettings(workDir, all); err != nil {
		return fmt.Errorf("gemini: register hook: sync settings: %w", err)
	}
	logger.Info("Register: hook registered", "uid", p.Data.UID, "id", p.Data.Info.ID)
	return nil
}

func (a *geminiAgent) GetRegisteredHooks() []*hooks.HookData {
	a.mu.RLock()
	workDir := a.workDir
	inMem := make([]*hooks.HookData, len(a.registeredHooks))
	copy(inMem, a.registeredHooks)
	a.mu.RUnlock()

	seen := map[string]struct{}{}
	var merged []*hooks.HookData

	addFromPath := func(settingsPath string, path hooks.HookPath) {
		f, err := readSettingsFile(settingsPath)
		if err != nil {
			logger.Warn("GetRegisteredHooks: could not read settings", "path", settingsPath, "err", err)
			return
		}
		if f.Hooks == nil {
			return
		}
		for eventName, matchers := range hookArraysByEvent(f.Hooks) {
			hookID, ok := hookIDFromEvent(eventName)
			if !ok {
				continue
			}
			for _, m := range matchers {
				for _, h := range m.Hooks {
					uid := ""
					if h.Name != nil {
						uid = *h.Name
					}
					command := ""
					if h.Command != nil {
						command = *h.Command
					}
					if uid == "" {
						uid = fmt.Sprintf("%s:%s", eventName, command)
					}
					if _, dup := seen[uid]; dup {
						continue
					}
					seen[uid] = struct{}{}
					merged = append(merged, hookDefinitionToData(uid, hookID, h, path))
				}
			}
		}
	}

	if gp, err := globalSettingsPath(); err == nil {
		addFromPath(gp, hooks.HookPath{Global: true})
	}
	addFromPath(filepath.Join(workDir, ".gemini", "settings.json"), hooks.HookPath{WorkspaceDir: &workDir})

	for _, h := range inMem {
		if _, dup := seen[h.UID]; dup {
			continue
		}
		seen[h.UID] = struct{}{}
		merged = append(merged, h)
	}
	return merged
}

func (a *geminiAgent) DeleteHook(p hooks.DeleteHookParams) (bool, error) {
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

	path := settingsPathForHookPath(hooks.HookPath{Global: p.Global, WorkspaceDir: p.WorkspaceDir}, workDir)
	f, err := readSettingsFile(path)
	if err == nil {
		before := countHooks(f.Hooks)
		if f.Hooks != nil {
			filterHookArray := func(arr HookDefinitionArray) HookDefinitionArray {
				filteredMatchers := make(HookDefinitionArray, 0, len(arr))
				for _, m := range arr {
					filteredDefs := m.Hooks[:0]
					for _, def := range m.Hooks {
						if def.Name == nil || *def.Name != p.UID {
							filteredDefs = append(filteredDefs, def)
						}
					}
					if len(filteredDefs) == 0 {
						continue
					}
					m.Hooks = filteredDefs
					filteredMatchers = append(filteredMatchers, m)
				}
				return filteredMatchers
			}
			f.Hooks.BeforeTool = filterHookArray(f.Hooks.BeforeTool)
			f.Hooks.AfterTool = filterHookArray(f.Hooks.AfterTool)
			f.Hooks.SessionStart = filterHookArray(f.Hooks.SessionStart)
			f.Hooks.BeforeAgent = filterHookArray(f.Hooks.BeforeAgent)
			f.Hooks.AfterAgent = filterHookArray(f.Hooks.AfterAgent)
		}
		after := countHooks(f.Hooks)
		if after < before {
			found = true
			if writeErr := writeSettingsFile(path, f); writeErr != nil {
				return true, fmt.Errorf("gemini: delete hook: write settings: %w", writeErr)
			}
		}
	}

	if !found {
		return false, fmt.Errorf("gemini: delete hook: uid %q not found", p.UID)
	}
	logger.Info("DeleteHook: removed", "uid", p.UID)
	return true, nil
}

func countHooks(h *SettingsSchemaJsonHooks) int {
	if h == nil {
		return 0
	}
	total := 0
	for _, matchers := range hookArraysByEvent(h) {
		for _, matcher := range matchers {
			total += len(matcher.Hooks)
		}
	}
	return total
}

func copyHooks(src []*hooks.HookData) []*hooks.HookData {
	out := make([]*hooks.HookData, len(src))
	copy(out, src)
	return out
}

var _ codeagent.CodeAgent = &geminiAgent{}
