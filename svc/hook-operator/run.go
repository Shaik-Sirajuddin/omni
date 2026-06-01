package hookoperator

import (
	"context"
	"fmt"

	"github.com/Shaik-Sirajuddin/omni/config"
	"github.com/Shaik-Sirajuddin/omni/store/codesession"
	"github.com/Shaik-Sirajuddin/omni/store/database"
	opstore "github.com/Shaik-Sirajuddin/omni/store/operator"
)

// RunOptions configures a Run call. All fields are optional — zero values apply
// the same defaults as the standalone cmd/main.go binary.
type RunOptions struct {
	// UnixPath is the unix socket path. Defaults to HOOK_OPERATOR_SOCKET env → /run/omni-<user>/hook-operator.sock.
	UnixPath string
	// BinaryPath is the omni binary used in hook entries. Defaults to exec.LookPath("omni").
	BinaryPath string
}

// Run starts the hook-operator service and blocks until ctx is cancelled.
// It wires the session store, agent store, config resolver, and unix socket
// exactly as cmd/main.go does, so callers in svc/cmd can embed it without
// forking a separate process.
func Run(ctx context.Context, opts RunOptions) error {
	var sessions SessionLookup
	var agents AgentLookup

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

	svc, err := New(ServiceOptions{
		Resolver:   &config.DefaultOmniConfigResolver{},
		UnixPath:   opts.UnixPath,
		BinaryPath: opts.BinaryPath,
		Sessions:   sessions,
		Agents:     agents,
	})
	if err != nil {
		return fmt.Errorf("hook-operator: init: %w", err)
	}

	if err := svc.Start(ctx); err != nil {
		return fmt.Errorf("hook-operator: start: %w", err)
	}

	logger.Info("hook-operator running", "socket", resolveUnixPath(opts.UnixPath))
	<-ctx.Done()

	svc.Stop()
	logger.Info("hook-operator stopped")
	return nil
}
