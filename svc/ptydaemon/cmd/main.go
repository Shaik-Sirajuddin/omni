//go:build linux

package main

import (
	"context"
	"os"
	"os/signal"
	"os/user"
	"syscall"

	ptydaemon "github.com/Shaik-Sirajuddin/memory/svc/ptydaemon"
	pkglog "github.com/Shaik-Sirajuddin/memory/pkg/log"
)

var logger = pkglog.NewLogger("component", "ptydaemon")

func main() {
	// Redirect all loggers in this process to stderr so journald captures them.
	// Must be called before any logger emits its first record.
	pkglog.UseStderrForAll()

	socketPath := ptydaemon.DefaultSocketPath()
	dbPath := envOr("PTYDAEMON_DB", "/var/lib/omni-"+currentUsername()+"/ptydaemon.db")

	logger.Info("ptydaemon starting", "socket", socketPath, "db", dbPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := ptydaemon.Run(ctx, socketPath, dbPath); err != nil {
		logger.Error("ptydaemon error", "err", err)
		os.Exit(1)
	}

	logger.Info("ptydaemon stopped")
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
	return "pty"
}
