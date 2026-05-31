package hookoperator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"

	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/pkg/sockpath"
)

var (
	ErrMissingResolver = errors.New("hook-operator: resolver is required")
)

// HookPayload is the inbound event delivered by the codeagent.
// AgentID is NOT present — it is resolved internally from session_id in Body.
type HookPayload struct {
	EventName string `json:"event_name"`
	Body      []byte `json:"body"` // raw JSON from codeagent stdin
}

// Result is the aggregated response returned to the codeagent.
type Result struct {
	Continue       bool    `json:"continue"`
	SuppressOutput bool    `json:"suppress_output"`
	StopReason     *string `json:"stop_reason,omitempty"`
	SystemMessage  *string `json:"system_message,omitempty"`
}

// HookProcessor is the narrow single-method interface for the default operator.
type HookProcessor interface {
	Process(HookPayload) (Result, error)
}

// HookEntryPoint executes all configured hooks for an event and returns the
// aggregated result.
type HookEntryPoint interface {
	Hook(HookPayload) (Result, error)
}

// HookOperatorService is the full lifecycle interface.
type HookOperatorService interface {
	HookEntryPoint
	HookRegistrar
	Start(ctx context.Context) error
	Stop()
}

// ServiceOptions configures a HookOperatorService.
type ServiceOptions struct {
	// Resolver is required — used to read and watch omni config.
	Resolver config.OmniConfigResolver
	// UnixPath is the unix socket path for the hook-operator server.
	// Falls back to HOOK_OPERATOR_SOCKET env var, then /tmp/hook-operator.sock.
	UnixPath string
	// BinaryPath is the omni binary used in default hook entries.
	// Defaults to os.Executable() when empty.
	BinaryPath string
	// CacheTTL controls how long cached hook entries remain valid without a
	// config-change notification. Defaults to 30s when zero.
	CacheTTL int // seconds
	// Sessions resolves active code sessions for payload enrichment. Optional.
	Sessions SessionLookup
	// Agents resolves agent info (name, workspace) for payload enrichment. Optional.
	Agents AgentLookup
}

type operatorService struct {
	resolver config.OmniConfigResolver
	cache    *entryCache
	ep       *entryPoint
	srv      *hookServer
	ar       *agentRegistry
	unixPath string
}

// New constructs a HookOperatorService. Call Start to begin watching config and serving.
func New(opts ServiceOptions) (HookOperatorService, error) {
	if opts.Resolver == nil {
		return nil, ErrMissingResolver
	}

	ttlSec := opts.CacheTTL
	if ttlSec <= 0 {
		ttlSec = 30
	}

	binaryPath, err := resolveOmniBinaryPath(opts.BinaryPath)
	if err != nil {
		return nil, err
	}

	unixPath := resolveUnixPath(opts.UnixPath)

	reg, err := NewRegistrar(binaryPath)
	if err != nil {
		return nil, err
	}

	cache := newEntryCache(ttlSec)
	runner := newExecutor()
	enr := newEnricher(opts.Sessions, opts.Agents)
	ep := newEntryPoint(cache, runner, enr)
	ar := newAgentRegistry(reg)
	srv := newServer(ep, ar)

	return &operatorService{
		resolver: opts.Resolver,
		cache:    cache,
		ep:       ep,
		srv:      srv,
		ar:       ar,
		unixPath: unixPath,
	}, nil
}

func (s *operatorService) Hook(payload HookPayload) (Result, error) {
	return s.ep.Hook(payload)
}

func (s *operatorService) Verify(provider codeagent.Provider) (bool, []string, error) {
	return s.ar.Verify(provider)
}

func (s *operatorService) Status() []ProviderHookStatus {
	return s.ar.Status()
}

func (s *operatorService) Start(ctx context.Context) error {
	logger.Info("hook-operator: starting", "socket", s.unixPath)

	cfg, err := s.resolver.GetUserSettings()
	if err != nil {
		return fmt.Errorf("hook-operator: load initial config: %w", err)
	}
	logger.Debug("hook-operator: initial config loaded", "hooks", len(hookEntriesFromConfig(cfg)))
	s.cache.set(hookEntriesFromConfig(cfg))

	if err := s.resolver.WatchSettings(func(cfg *config.OmniConfig) {
		entries := hookEntriesFromConfig(cfg)
		logger.Debug("hook-operator: config reloaded", "hooks", len(entries))
		s.cache.set(entries)
	}); err != nil {
		return fmt.Errorf("hook-operator: watch config: %w", err)
	}

	ln, err := net.Listen("unix", s.unixPath)
	if err != nil {
		return fmt.Errorf("hook-operator: listen on %s: %w", s.unixPath, err)
	}

	return s.srv.start(ctx, []net.Listener{ln})
}

func (s *operatorService) Stop() {
	s.resolver.Unwatch()
	s.srv.stop()
}

// resolveOmniBinaryPath resolves the omni CLI binary used in hook entries.
// Priority: opts.BinaryPath → HOOK_OPERATOR_BINARY env → "omni" in PATH.
func resolveOmniBinaryPath(optPath string) (string, error) {
	if optPath != "" {
		return optPath, nil
	}
	if v := os.Getenv("HOOK_OPERATOR_BINARY"); v != "" {
		return v, nil
	}
	path, err := exec.LookPath("omni")
	if err != nil {
		return "", fmt.Errorf("hook-operator: omni binary not found in PATH (set HOOK_OPERATOR_BINARY or BinaryPath): %w", err)
	}
	return path, nil
}

// SocketPath returns the hook-operator unix socket path.
func SocketPath() string {
	return sockpath.HookOperator()
}

// resolveUnixPath picks the socket path for the service itself.
// Caller-supplied path (from svc/cmd or setup) takes highest priority.
func resolveUnixPath(optPath string) string {
	if optPath != "" {
		return optPath
	}
	return SocketPath()
}
