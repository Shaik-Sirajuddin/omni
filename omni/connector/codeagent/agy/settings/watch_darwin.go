//go:build darwin

package settings

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// osWatch uses BSD kqueue to detect changes to path on macOS.
// NOTE_WRITE fires when the file's content changes; NOTE_ATTRIB when its
// metadata (mtime) changes. The goroutine exits when ctx is done.
func osWatch(ctx context.Context, path string, onChange func()) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("settings watch: mkdir %s: %w", dir, err)
	}

	// Open the directory to catch atomic replaces (write-to-tmp + rename).
	// O_EVTONLY avoids preventing unmount of the volume.
	dirFd, err := unix.Open(dir, unix.O_RDONLY|unix.O_EVTONLY, 0)
	if err != nil {
		return fmt.Errorf("settings watch: open dir %s: %w", dir, err)
	}

	kq, err := unix.Kqueue()
	if err != nil {
		unix.Close(dirFd) //nolint:errcheck
		return fmt.Errorf("settings watch: kqueue: %w", err)
	}

	const fflags = unix.NOTE_WRITE | unix.NOTE_ATTRIB | unix.NOTE_RENAME | unix.NOTE_DELETE
	change := unix.Kevent_t{
		Filter: unix.EVFILT_VNODE,
		Flags:  unix.EV_ADD | unix.EV_ENABLE | unix.EV_CLEAR,
		Fflags: fflags,
	}
	unix.SetKevent(&change, dirFd, unix.EVFILT_VNODE, unix.EV_ADD|unix.EV_ENABLE|unix.EV_CLEAR)

	if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, nil, nil); err != nil {
		unix.Close(kq)    //nolint:errcheck
		unix.Close(dirFd) //nolint:errcheck
		return fmt.Errorf("settings watch: kevent register: %w", err)
	}

	base := filepath.Base(path)

	go func() {
		defer unix.Close(kq)    //nolint:errcheck
		defer unix.Close(dirFd) //nolint:errcheck

		events := make([]unix.Kevent_t, 4)
		timeout := unix.NsecToTimespec(int64(200 * time.Millisecond))

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, err := unix.Kevent(kq, nil, events, &timeout)
			if err != nil || n == 0 {
				continue
			}

			// A directory-level event fired; check if it's our file.
			if _, statErr := os.Stat(filepath.Join(dir, base)); statErr == nil {
				select {
				case <-ctx.Done():
					return
				default:
					onChange()
				}
			}
		}
	}()

	return nil
}
