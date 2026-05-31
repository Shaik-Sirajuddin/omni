//go:build !linux && !darwin

package settings

import (
	"context"
	"os"
	"time"
)

// osWatch falls back to 500 ms mtime polling on platforms without native
// inotify/kqueue support (e.g. Windows).
func osWatch(ctx context.Context, path string, onChange func()) error {
	go func() {
		var lastMod time.Time
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				if info.ModTime().After(lastMod) {
					lastMod = info.ModTime()
					onChange()
				}
			}
		}
	}()
	return nil
}
