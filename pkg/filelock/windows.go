//go:build windows

package filelock

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type windowsLocker struct{}

// New returns a Locker backed by windows.LockFileEx.
func New() Locker { return &windowsLocker{} }

func (w *windowsLocker) Lock(lockPath string) (func(), error) {
	return w.lockEx(lockPath, windows.LOCKFILE_EXCLUSIVE_LOCK)
}

func (w *windowsLocker) RLock(lockPath string) (func(), error) {
	return w.lockEx(lockPath, 0) // 0 = shared
}

func (w *windowsLocker) lockEx(lockPath string, flags uint32) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("filelock: open %s: %w", lockPath, err)
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ol); err != nil {
		f.Close()
		return nil, fmt.Errorf("filelock: LockFileEx: %w", err)
	}
	return func() {
		ol2 := new(windows.Overlapped)
		windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol2) //nolint:errcheck
		f.Close()
	}, nil
}

func (w *windowsLocker) WriteAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	from, _ := windows.UTF16PtrFromString(name)
	to, _ := windows.UTF16PtrFromString(path)
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING)
}
