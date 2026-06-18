package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"

	omniconfig "github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/agy"
	agysettings "github.com/Shaik-Sirajuddin/memory/connector/codeagent/agy/settings"
	_ "github.com/Shaik-Sirajuddin/memory/connector/codeagent/claude"
	_ "github.com/Shaik-Sirajuddin/memory/connector/codeagent/codex"
	"github.com/Shaik-Sirajuddin/memory/mcp/mcp/runner"
	configsync "github.com/Shaik-Sirajuddin/memory/svc/config_sync"
	hookoperator "github.com/Shaik-Sirajuddin/memory/svc/hook-operator"
	"github.com/Shaik-Sirajuddin/memory/svc/ptydaemon"
)

// ServiceConfig holds the enable flag shared by all service entries.
type ServiceConfig struct {
	Enabled bool
}

// PTYDaemonConfig configures the ptydaemon service.
type PTYDaemonConfig struct {
	ServiceConfig
	SocketPath string
	DBPath     string
}

// HookOperatorConfig configures the hook-operator service.
type HookOperatorConfig struct {
	ServiceConfig
	SocketPath string
	BinaryPath string // omni binary used in hook entries; resolved by service if empty
}

// AxolinkMCPConfig configures the tunnel MCP service.
type AxolinkMCPConfig struct {
	ServiceConfig
}

// ConfigSyncConfig configures the config sync service.
type ConfigSyncConfig struct {
	ServiceConfig
	// WorkspaceDir is the workspace directory for agy settings sync. When set,
	// a SettingsSyncTarget is registered to keep ~/.agy/settings.json and
	// <WorkspaceDir>/.agy/settings.json in sync. When empty, settings sync is
	// skipped but hook sync still runs.
	WorkspaceDir  string
	WatchSettings bool
}

// ServiceMux runs ptydaemon, hook-operator, axolink-mcp, and config-sync as
// in-process goroutines under a shared context. Stopping the context is the
// only shutdown signal needed.
type ServiceMux struct {
	PTYDaemon    PTYDaemonConfig
	HookOperator HookOperatorConfig
	AxolinkMCP   AxolinkMCPConfig
	ConfigSync   ConfigSyncConfig

	// ConfigResolver is used to watch for live config changes. When non-nil,
	// changes to axolink_mcp in the config file are applied without restart.
	// When nil, live watching is skipped.
	ConfigResolver omniconfig.OmniConfigResolver
}

// axolinkEnabled returns true when the *bool config field is nil (absent →
// default on) or points to true.
func axolinkEnabled(f *bool) bool {
	return f == nil || *f
}

// startAxolinkMCP registers axolink with all providers in the global MCP
// registry and launches the MCP runner goroutine under axoCtx. It adds 1 to wg
// and calls onErr with any fatal error, then calls onDone regardless of exit
// reason. The caller is responsible for cancelling axoCtx to stop the goroutine.
func startAxolinkMCP(
	axoCtx context.Context,
	log *slog.Logger,
	onErr func(error),
	onDone func(),
) {
	cfg := runner.DefaultConfig()
	omniPath, lookErr := exec.LookPath("omni")
	if lookErr != nil {
		log.Warn("axolink-mcp: omni binary not found in PATH, skipping connector registration", "err", lookErr)
	} else {
		for provider, mgr := range codeagent.GlobalMCPRegistry.All() {
			if _, err := mgr.AddMCP(codeagent.AddMCPParams{
				Server: codeagent.MCPServer{
					Name:      "axolink",
					Transport: codeagent.MCPTransportStdio,
					Command:   omniPath,
					Args:      []string{"axolink"},
					// Codex clears the parent env before spawning stdio MCP subprocesses
					// (env_clear in codex-rs). AXO_LINK_MCP_TRANSPORT must be set as a
					// literal since it is not present in the parent env.
					Env: map[string]string{
						"AXO_LINK_MCP_TRANSPORT": "stdio",
					},
					// Forward session-specific vars from the codex PTY process env.
					// The operator injects these via p.Envs on every session; codex
					// merges them into the PTY env. env_vars tells codex to re-forward
					// those names into the omni axolink stdio subprocess.
					EnvVars: []string{
						"AXO_LINK_MCP_AUTH_TOKEN",
						"AXO_LINK_MCP_SENDER_ID",
						"AXO_LINK_MCP_SENDER_TYPE",
						"AXO_LINK_MCP_AGENT_WORKSPACE",
					},
				},
				Global: true,
			}); err != nil {
				if errors.Is(err, codeagent.ErrMCPNotSupported) {
					log.Warn("axolink-mcp: connector does not support MCP", "provider", provider)
				} else {
					log.Error("axolink-mcp: register with connector failed", "provider", provider, "err", err)
				}
			} else {
				log.Info("axolink-mcp: registered with connector", "provider", provider, "command", omniPath, "args", []string{"axolink"})
			}
		}
	}
	go func() {
		defer onDone()
		if err := runner.Run(axoCtx, cfg); err != nil && !errors.Is(err, context.Canceled) {
			onErr(fmt.Errorf("axolink-mcp: %w", err))
		}
	}()
}

// stopAxolinkMCP cancels the axolink goroutine and deregisters axolink from
// all providers in the global MCP registry.
func stopAxolinkMCP(cancel context.CancelFunc, log *slog.Logger) {
	cancel()
	for provider, mgr := range codeagent.GlobalMCPRegistry.All() {
		if _, err := mgr.DeleteMCP(codeagent.DeleteMCPParams{Name: "axolink", Global: true}); err != nil {
			log.Warn("axolink-mcp: deregister from connector failed", "provider", provider, "err", err)
		} else {
			log.Info("axolink-mcp: deregistered from connector", "provider", provider)
		}
	}
}

// Run starts all enabled services and blocks until all have exited. The first
// service error is returned; context cancellation is not treated as an error.
func (m *ServiceMux) Run(ctx context.Context, log *slog.Logger) error {
	type result struct{ err error }
	ch := make(chan result, 4)
	var wg sync.WaitGroup

	if m.PTYDaemon.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("ptydaemon starting", "socket", m.PTYDaemon.SocketPath)
			err := ptydaemon.Run(ctx, m.PTYDaemon.SocketPath, m.PTYDaemon.DBPath)
			if err != nil {
				ch <- result{fmt.Errorf("ptydaemon: %w", err)}
			}
		}()
	}

	if m.HookOperator.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := hookoperator.Run(ctx, hookoperator.RunOptions{
				UnixPath:   m.HookOperator.SocketPath,
				BinaryPath: m.HookOperator.BinaryPath,
			})
			if err != nil {
				ch <- result{fmt.Errorf("hook-operator: %w", err)}
			}
		}()
	}

	// axoMu guards axoCancel and axoRunning so the config watcher goroutine
	// and the main Run goroutine don't race.
	//
	// axoRunning is only cleared by the goroutine's onDone callback (not by
	// stopAxo) so a rapid disable→enable toggle can't spawn a second runner
	// while the first is still winding down.
	var axoMu sync.Mutex
	var axoWg sync.WaitGroup
	var axoCancel context.CancelFunc
	axoRunning := false

	startAxo := func() {
		axoCtx, cancel := context.WithCancel(ctx)
		axoCancel = cancel
		axoRunning = true
		startAxolinkMCP(axoCtx, log,
			func(err error) { ch <- result{err} },
			func() {
				// Reset axoRunning under the mutex whenever the goroutine
				// exits — whether via error, intentional cancel, or parent
				// context cancellation — so the watcher never sees a stale
				// "running" state after an unplanned exit.
				axoMu.Lock()
				axoRunning = false
				axoMu.Unlock()
			},
		)
	}
	stopAxo := func() {
		if axoCancel != nil {
			stopAxolinkMCP(axoCancel, log)
			axoCancel = nil
		}
		// axoRunning is cleared by the goroutine's onDone callback, not here.
		// Clearing it immediately would allow a second runner to start before
		// the first has actually exited.
	}

	if m.AxolinkMCP.Enabled {
		axoMu.Lock()
		startAxo()
		axoMu.Unlock()
	}

	// axoWg.Wait() must run regardless of whether a live watcher is installed,
	// because the initial axolink goroutine (started above) uses axoWg, not
	// the shared wg. Register the drain defer unconditionally; if a config
	// resolver is also present, register Unwatch right after so it runs first
	// (LIFO: last-registered defer runs first).
	defer axoWg.Wait()

	// Install a live config watcher if a resolver was provided. Config changes
	// to axolink_mcp take effect immediately without restarting the daemon.
	if m.ConfigResolver != nil {
		if err := m.ConfigResolver.WatchSettings(func(cfg *omniconfig.OmniConfig) {
			want := cfg == nil || cfg.Features == nil || axolinkEnabled(cfg.Features.AxolinkMCP)

			axoMu.Lock()
			defer axoMu.Unlock()

			switch {
			case want && !axoRunning:
				log.Info("axolink-mcp: enabled via config change")
				startAxo()
			case !want && axoRunning:
				log.Info("axolink-mcp: disabled via config change")
				stopAxo()
			}
		}); err != nil {
			log.Warn("axolink-mcp: failed to install config watcher", "err", err)
		}
		// Unwatch is deferred after axoWg.Wait(), so it runs first (LIFO).
		// This ensures no new goroutines can be added to axoWg after Unwatch
		// returns, before axoWg.Wait() drains the last one.
		defer m.ConfigResolver.Unwatch()
	}

	if m.ConfigSync.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc, err := configsync.NewService(configsync.ServiceOptions{
				Resolver:      &omniconfig.DefaultOmniConfigResolver{},
				WatchSettings: m.ConfigSync.WatchSettings,
			})
			if err != nil {
				ch <- result{fmt.Errorf("config-sync: create: %w", err)}
				return
			}
			if m.ConfigSync.WorkspaceDir != "" {
				if err := svc.RegisterSettingsTarget(configsync.SettingsSyncTarget{
					AgentID:      string(agy.Agy),
					Provider:     configsync.ProviderAgy,
					Resolver:     agysettings.New(agy.Agy),
					WorkspaceDir: m.ConfigSync.WorkspaceDir,
				}); err != nil {
					ch <- result{fmt.Errorf("config-sync: register settings target: %w", err)}
					return
				}
			}
			if err := svc.Start(ctx); err != nil {
				ch <- result{fmt.Errorf("config-sync: start: %w", err)}
				return
			}
			<-ctx.Done()
		}()
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	for r := range ch {
		if r.err != nil {
			return r.err
		}
	}
	return nil
}
