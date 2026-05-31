package configsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	codehooks "github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
)

var (
	ErrMissingResolver = errors.New("configsync: missing config resolver")
	ErrMissingAgentID  = errors.New("configsync: missing agent id")
	ErrMissingHook     = errors.New("configsync: missing hook transformer")
)

// Agent describes an active agent that can receive synchronized hooks.
type Agent struct {
	ID          string
	Transformer codeagent.HookTransformer
	Settings    *SettingsSyncTarget
}

// AgentRegistry lists active agents owned by the operator.
type AgentRegistry interface {
	ListActiveAgents(context.Context) ([]Agent, error)
}

// ConfigSyncService watches omni config and keeps each agent's hooks in sync.
type ConfigSyncService interface {
	Start(ctx context.Context) error
	Stop()
	RegisterAgent(agentID string, t codeagent.HookTransformer) error
	UnregisterAgent(agentID string)
	Transformer(agentID string) (codeagent.HookTransformer, bool)
	RegisterSettingsTarget(SettingsSyncTarget) error
	UnregisterSettingsTarget(agentID string)
}

type ServiceOptions struct {
	Resolver      config.OmniConfigResolver
	Agents        AgentRegistry
	Registry      HookParserRegistry
	BinaryPath    string
	WatchSettings bool
}

type service struct {
	resolver      config.OmniConfigResolver
	agents        AgentRegistry
	registry      HookParserRegistry
	settings      map[string]*settingsSync
	binaryPath    string
	watchSettings bool

	mu      sync.Mutex
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewService creates a config sync service.
func NewService(opts ServiceOptions) (ConfigSyncService, error) {
	if opts.Resolver == nil {
		return nil, ErrMissingResolver
	}
	reg := opts.Registry
	if reg == nil {
		reg = NewRegistry()
	}
	binaryPath := opts.BinaryPath
	if binaryPath == "" {
		path, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("configsync: resolve executable: %w", err)
		}
		binaryPath = path
	}
	return &service{
		resolver:      opts.Resolver,
		agents:        opts.Agents,
		registry:      reg,
		settings:      map[string]*settingsSync{},
		binaryPath:    binaryPath,
		watchSettings: opts.WatchSettings,
	}, nil
}

func (s *service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.ctx = runCtx
	s.cancel = cancel
	s.started = true
	s.mu.Unlock()

	if err := s.registerActiveAgents(runCtx); err != nil {
		s.Stop()
		return err
	}
	if err := s.syncRegisteredAgents(); err != nil {
		s.Stop()
		return err
	}
	if s.watchSettings {
		if err := s.resolver.WatchSettings(func(*config.OmniConfig) {
			_ = s.syncRegisteredAgents()
		}); err != nil {
			s.Stop()
			return err
		}
	}
	if err := s.startSettingsSyncs(runCtx); err != nil {
		s.Stop()
		return err
	}

	go func() {
		<-runCtx.Done()
		s.Stop()
	}()

	return nil
}

func (s *service) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.ctx = nil
	wasStarted := s.started
	s.started = false
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if wasStarted && s.watchSettings {
		s.resolver.Unwatch()
	}
	if wasStarted {
		s.stopSettingsSyncs()
	}
}

func (s *service) RegisterAgent(agentID string, t codeagent.HookTransformer) error {
	if agentID == "" {
		return ErrMissingAgentID
	}
	if t == nil {
		return ErrMissingHook
	}
	s.registry.Register(agentID, t)
	return s.syncAgent(agentID, t)
}

func (s *service) UnregisterAgent(agentID string) {
	s.registry.Unregister(agentID)
}

func (s *service) Transformer(agentID string) (codeagent.HookTransformer, bool) {
	return s.registry.Get(agentID)
}

func (s *service) RegisterSettingsTarget(target SettingsSyncTarget) error {
	if err := target.validate(); err != nil {
		return err
	}

	s.mu.Lock()
	syncer, ok := s.settings[target.AgentID]
	if ok {
		syncer.stop()
	}
	syncer = newSettingsSync(target)
	s.settings[target.AgentID] = syncer
	started := s.started
	ctx := s.ctx
	s.mu.Unlock()

	if err := syncer.syncDefaultToWorkspace(); err != nil {
		return err
	}
	if started {
		if ctx == nil {
			ctx = context.Background()
		}
		return syncer.start(ctx)
	}
	return nil
}

func (s *service) UnregisterSettingsTarget(agentID string) {
	s.mu.Lock()
	syncer := s.settings[agentID]
	delete(s.settings, agentID)
	s.mu.Unlock()
	if syncer != nil {
		syncer.stop()
	}
}

func (s *service) registerActiveAgents(ctx context.Context) error {
	if s.agents == nil {
		return nil
	}
	agents, err := s.agents.ListActiveAgents(ctx)
	if err != nil {
		return fmt.Errorf("configsync: list active agents: %w", err)
	}
	for _, agent := range agents {
		if agent.ID != "" && agent.Transformer != nil {
			s.registry.Register(agent.ID, agent.Transformer)
		}
		if agent.Settings != nil {
			target := *agent.Settings
			if target.AgentID == "" {
				target.AgentID = agent.ID
			}
			if err := s.registerSettingsTargetLocked(target); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *service) registerSettingsTargetLocked(target SettingsSyncTarget) error {
	if err := target.validate(); err != nil {
		return err
	}
	if existing := s.settings[target.AgentID]; existing != nil {
		existing.stop()
	}
	syncer := newSettingsSync(target)
	s.settings[target.AgentID] = syncer
	return syncer.syncDefaultToWorkspace()
}

func (s *service) startSettingsSyncs(ctx context.Context) error {
	s.mu.Lock()
	syncers := make([]*settingsSync, 0, len(s.settings))
	for _, syncer := range s.settings {
		syncers = append(syncers, syncer)
	}
	s.mu.Unlock()

	var errs []error
	for _, syncer := range syncers {
		if err := syncer.start(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *service) stopSettingsSyncs() {
	s.mu.Lock()
	syncers := make([]*settingsSync, 0, len(s.settings))
	for _, syncer := range s.settings {
		syncers = append(syncers, syncer)
	}
	s.mu.Unlock()

	for _, syncer := range syncers {
		syncer.stop()
	}
}

func (s *service) syncRegisteredAgents() error {
	var errs []error
	for agentID, transformer := range s.registry.List() {
		if err := s.syncAgent(agentID, transformer); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *service) syncAgent(agentID string, t codeagent.HookTransformer) error {
	cfg, err := s.resolver.GetUserSettings()
	if err != nil {
		return fmt.Errorf("configsync: read user settings: %w", err)
	}
	for _, hook := range s.defaultHooks(agentID) {
		t.Add(hook.Name, hook.Entry)
	}
	for name, entry := range configuredHooks(cfg) {
		t.Add(name, entry)
	}
	return nil
}

func (s *service) defaultHooks(agentID string) []defaultHook {
	hooks := defaultHooks(s.binaryPath)
	for i := range hooks {
		hooks[i].Entry.Args = withAgentFlag(hooks[i].Entry.Args, agentID)
	}
	return hooks
}

type defaultHook struct {
	Name  string
	Event codehooks.HookID
	Entry config.HookEntry
}

func defaultHooks(binaryPath string) []defaultHook {
	cmd := func(event codehooks.HookID) config.HookEntry {
		eventName := string(event)
		return config.HookEntry{
			Command: &binaryPath,
			Args:    []string{"hook", "--event", eventName},
		}
	}

	return []defaultHook{
		{Name: "omni." + string(codehooks.PreToolUse), Event: codehooks.PreToolUse, Entry: cmd(codehooks.PreToolUse)},
		{Name: "omni." + string(codehooks.PostToolUse), Event: codehooks.PostToolUse, Entry: cmd(codehooks.PostToolUse)},
		{Name: "omni." + string(codehooks.PostToolUseFailure), Event: codehooks.PostToolUseFailure, Entry: cmd(codehooks.PostToolUseFailure)},
		{Name: "omni." + string(codehooks.SessionStart), Event: codehooks.SessionStart, Entry: cmd(codehooks.SessionStart)},
		{Name: "omni." + string(codehooks.PrePrompt), Event: codehooks.PrePrompt, Entry: cmd(codehooks.PrePrompt)},
		{Name: "omni." + string(codehooks.PostPrompt), Event: codehooks.PostPrompt, Entry: cmd(codehooks.PostPrompt)},
	}
}

func configuredHooks(cfg *config.OmniConfig) map[string]config.HookEntry {
	out := map[string]config.HookEntry{}
	if cfg == nil || cfg.Agent == nil {
		return out
	}
	for eventName, entries := range cfg.Agent.Hooks {
		for i, entry := range entries {
			name := fmt.Sprintf("omni.config.%s.%d", eventName, i)
			entry.Args = withEventFlag(entry.Args, eventName)
			out[name] = entry
		}
	}
	return out
}

func withAgentFlag(args []string, agentID string) []string {
	if agentID == "" || flagValue(args, "--agent") != "" {
		return slices.Clone(args)
	}
	out := slices.Clone(args)
	return append(out, "--agent", agentID)
}

func withEventFlag(args []string, eventName string) []string {
	if eventName == "" || flagValue(args, "--event") != "" {
		return slices.Clone(args)
	}
	out := slices.Clone(args)
	return append(out, "--event", eventName)
}

func flagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
		if len(arg) > len(flag)+1 && arg[:len(flag)+1] == flag+"=" {
			return arg[len(flag)+1:]
		}
	}
	return ""
}
