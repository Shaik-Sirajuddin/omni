package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	omniconfig "github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/agy"
	agysettings "github.com/Shaik-Sirajuddin/memory/connector/codeagent/agy/settings"
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

	if m.AxolinkMCP.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runner.Run(ctx, runner.DefaultConfig()); err != nil {
				ch <- result{fmt.Errorf("axolink-mcp: %w", err)}
			}
		}()
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
