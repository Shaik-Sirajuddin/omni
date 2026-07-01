package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"syscall"

	omniconfig "github.com/Shaik-Sirajuddin/memory/config"
	pkglog "github.com/Shaik-Sirajuddin/memory/pkg/log"
	"github.com/Shaik-Sirajuddin/memory/pkg/sockpath"
)

var Version = "dev"

func main() {
	pkglog.UseStderrForAll() // must be before any logger is first written to

	disablePTY        := flag.Bool("disable-ptydaemon", false, "Disable the PTY daemon service")
	disableHook       := flag.Bool("disable-hook-operator", false, "Disable the hook operator service")
	disableMCP        := flag.Bool("disable-axolink-mcp", false, "Disable the Axolink MCP service")
	disableConfigSync := flag.Bool("disable-config-sync", false, "Disable the config sync service")
	printVersion      := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *printVersion {
		fmt.Println(Version)
		os.Exit(0)
	}

	initOtel()
	shutdownPprof := startPprof()
	log := pkglog.NewLogger("component", "svc")
	username := currentUsername()

	resolver := &omniconfig.DefaultOmniConfigResolver{}
	cfg, cfgErr := resolver.GetUserSettings()
	if cfgErr != nil {
		log.Warn("failed to load omni config, using defaults", "err", cfgErr)
		cfg = nil
	}

	axolinkMCPEnabled := resolveAxolinkMCPEnabled(*disableMCP, cfg)

	// When the CLI flag disables MCP, skip the live watcher — the flag is
	// definitive and config changes should not re-enable it.
	var axolinkResolver omniconfig.OmniConfigResolver
	if !*disableMCP {
		axolinkResolver = resolver
	}

	mux := &ServiceMux{
		PTYDaemon: PTYDaemonConfig{
			ServiceConfig: ServiceConfig{Enabled: !*disablePTY},
			SocketPath:    sockpath.PTY(),
			DBPath:        envOr("PTYDAEMON_DB", "/var/lib/omni-"+username+"/ptydaemon.db"),
		},
		HookOperator: HookOperatorConfig{
			ServiceConfig: ServiceConfig{Enabled: !*disableHook},
			SocketPath:    sockpath.HookOperator(),
		},
		AxolinkMCP: AxolinkMCPConfig{
			ServiceConfig: ServiceConfig{Enabled: axolinkMCPEnabled},
		},
		ConfigSync: ConfigSyncConfig{
			ServiceConfig: ServiceConfig{Enabled: !*disableConfigSync},
			WorkspaceDir:  envOr("CONFIG_SYNC_AGY_WORKSPACE_DIR", ""),
			WatchSettings: true,
		},
		ConfigResolver: axolinkResolver,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := mux.Run(ctx, log); err != nil {
		log.Error("service error", "err", err)
		_ = shutdownPprof(context.Background())
		os.Exit(1)
	}
	_ = shutdownPprof(context.Background())
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func currentUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return "omni"
}

// resolveAxolinkMCPEnabled determines whether the Axolink MCP service should
// be enabled. The CLI flag --disable-axolink-mcp always wins (disables the
// service when set). When the flag is not set, the config feature flag is
// consulted; nil (absent from config) is treated as true (default on).
func resolveAxolinkMCPEnabled(cliDisable bool, cfg *omniconfig.OmniConfig) bool {
	if cliDisable {
		return false
	}
	if cfg != nil && cfg.Features != nil && cfg.Features.AxolinkMCP != nil && !*cfg.Features.AxolinkMCP {
		return false
	}
	return true
}
