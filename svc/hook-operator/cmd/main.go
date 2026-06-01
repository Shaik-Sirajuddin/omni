package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	applog "github.com/Shaik-Sirajuddin/omni/pkg/log"
	"github.com/Shaik-Sirajuddin/omni/config"
	"github.com/Shaik-Sirajuddin/omni/store/codesession"
	"github.com/Shaik-Sirajuddin/omni/store/database"
	opstore "github.com/Shaik-Sirajuddin/omni/store/operator"
	hookoperator "github.com/Shaik-Sirajuddin/omni/svc/hook-operator"
)

func main() {
	logger := applog.NewLogger("component", "hook-operator")

	var sessions hookoperator.SessionLookup
	var agents hookoperator.AgentLookup

	if sessionStore, err := codesession.GetReadOnlyCodeSessionStore(); err != nil {
		logger.Warn("session store unavailable — omni.agent enrichment omitted", "err", err)
	} else {
		sessions = sessionStore
	}

	if db, err := database.GetDB(); err != nil {
		logger.Warn("operator store unavailable — omni.workspace enrichment omitted", "err", err)
	} else {
		agents = opstore.NewWithDB(db)
	}

	svc, err := hookoperator.New(hookoperator.ServiceOptions{
		Resolver: &config.DefaultOmniConfigResolver{},
		Sessions: sessions,
		Agents:   agents,
	})
	if err != nil {
		logger.Error("failed to create hook-operator", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := svc.Start(ctx); err != nil {
		logger.Error("failed to start hook-operator", "err", err)
		os.Exit(1)
	}

	logger.Info("hook-operator running", "socket", hookoperator.SocketPath())
	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = shutdownCtx

	svc.Stop()
	logger.Info("hook-operator stopped")
}
