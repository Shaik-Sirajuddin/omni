//go:build linux

package ptydaemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Shaik-Sirajuddin/memory/svc/ptydaemon/internal"
	"github.com/Shaik-Sirajuddin/memory/svc/ptydaemon/ptyunix"
)

// Run wires the store, daemon, and unix socket server, then blocks until ctx
// is cancelled. A 10-second graceful shutdown is attempted before returning.
func Run(ctx context.Context, socketPath, dbPath string) error {
	// Create the socket directory if it doesn't exist. In systemd installs
	// RuntimeDirectory= handles this; in non-systemd environments (Coder workspaces,
	// containers) it must be created explicitly.
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return fmt.Errorf("ptydaemon: create socket dir: %w", err)
	}
	store, err := internal.NewStore(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	daemon := internal.NewDaemon(store)
	unixDaemon := ptyunix.NewDaemonWithInner(daemon)

	errCh := make(chan error, 1)
	go func() {
		if err := unixDaemon.ListenAndServe(ctx, socketPath); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		ptylog.Info("ptydaemon: shutdown signal received")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := daemon.Shutdown(shutdownCtx); err != nil {
		ptylog.Error("ptydaemon: shutdown error", "err", err)
	}
	return nil
}
