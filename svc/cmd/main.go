package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"syscall"

	pkglog "github.com/Shaik-Sirajuddin/memory/pkg/log"
	"github.com/Shaik-Sirajuddin/memory/pkg/sockpath"
	operatorimpl "github.com/Shaik-Sirajuddin/memory/operator/impl"
	agentpoolclient "github.com/Shaik-Sirajuddin/memory/svc/agentpool/client"
)

var Version = "dev"

func main() {
	pkglog.UseStderrForAll() // must be before any logger is first written to

	disablePTY        := flag.Bool("disable-ptydaemon", false, "Disable the PTY daemon service")
	disableHook       := flag.Bool("disable-hook-operator", false, "Disable the hook operator service")
	disableMCP        := flag.Bool("disable-axolink-mcp", false, "Disable the Axolink MCP service")
	disableConfigSync := flag.Bool("disable-config-sync", false, "Disable the config sync service")
	disableAgentPool  := flag.Bool("disable-agent-pool", false, "Disable the agent pool daemon service")
	printVersion      := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *printVersion {
		fmt.Println(Version)
		os.Exit(0)
	}

	initOtel()
	log := pkglog.NewLogger("component", "svc")
	username := currentUsername()

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
			ServiceConfig: ServiceConfig{Enabled: !*disableMCP},
		},
		ConfigSync: ConfigSyncConfig{
			ServiceConfig: ServiceConfig{Enabled: !*disableConfigSync},
			WorkspaceDir:  envOr("CONFIG_SYNC_AGY_WORKSPACE_DIR", ""),
			WatchSettings: true,
		},
	}

	if !*disableAgentPool {
		poolSocketPath := sockpath.AgentPool()
		op, err := operatorimpl.NewDefault()
		if err != nil {
			log.Error("agent-pool: operator init failed", "err", err)
			os.Exit(1)
		}
		op.SetPoolClient(agentpoolclient.New(poolSocketPath))
		mux.AgentPool = AgentPoolConfig{
			ServiceConfig: ServiceConfig{Enabled: true},
			SocketPath:    poolSocketPath,
			CreateAgent:   op.CreateAgentForPool,
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := mux.Run(ctx, log); err != nil {
		log.Error("service error", "err", err)
		os.Exit(1)
	}
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
