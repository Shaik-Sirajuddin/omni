//go:build linux

package settings

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// osWatch uses Linux inotify to detect changes to path.
// onChange is fired on IN_CLOSE_WRITE, IN_MOVED_TO, or IN_CREATE events that
// match the watched file's base name. The goroutine exits when ctx is done.
func osWatch(ctx context.Context, path string, onChange func()) error {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		return fmt.Errorf("settings watch: inotify_init1: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		unix.Close(fd) //nolint:errcheck
		return fmt.Errorf("settings watch: mkdir %s: %w", dir, err)
	}

	const mask = unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_CREATE
	wd, err := unix.InotifyAddWatch(fd, dir, mask)
	if err != nil {
		unix.Close(fd) //nolint:errcheck
		return fmt.Errorf("settings watch: inotify_add_watch %s: %w", dir, err)
	}

	base := filepath.Base(path)

	go func() {
		defer unix.InotifyRmWatch(fd, uint32(wd)) //nolint:errcheck
		defer unix.Close(fd)                       //nolint:errcheck

		buf := make([]byte, (unix.SizeofInotifyEvent+unix.NAME_MAX+1)*4)

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Poll the fd with a short timeout so ctx cancellation is responsive.
			fds := unix.FdSet{}
			fds.Set(fd)
			tv := unix.NsecToTimeval(int64(200 * time.Millisecond))
			n, err := unix.Select(fd+1, &fds, nil, nil, &tv)
			if err != nil || n == 0 {
				continue
			}

			n, err = unix.Read(fd, buf)
			if err != nil || n < unix.SizeofInotifyEvent {
				continue
			}

			offset := 0
			for offset+unix.SizeofInotifyEvent <= n {
				ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
				nameLen := int(ev.Len)
				evName := ""
				if nameLen > 0 {
					nameBytes := buf[offset+unix.SizeofInotifyEvent : offset+unix.SizeofInotifyEvent+nameLen]
					// Name is a null-padded C string.
					for i, b := range nameBytes {
						if b == 0 {
							evName = string(nameBytes[:i])
							break
						}
					}
				}

				if evName == base {
					select {
					case <-ctx.Done():
						return
					default:
						onChange()
					}
				}
				offset += unix.SizeofInotifyEvent + nameLen
			}
		}
	}()

	return nil
}
