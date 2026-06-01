//go:build !windows

package filelock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type unixLocker struct{}

// New returns a Locker backed by syscall.Flock.
func New() Locker { return &unixLocker{} }

func (u *unixLocker) Lock(lockPath string) (func(), error) {
	return u.flock(lockPath, syscall.LOCK_EX)
}

func (u *unixLocker) RLock(lockPath string) (func(), error) {
	return u.flock(lockPath, syscall.LOCK_SH)
}

func (u *unixLocker) flock(lockPath string, how int) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("filelock: open %s: %w", lockPath, err)
	}
	// Re-stat after open to guard against lock-file deletion/recreation race.
	fi1, _ := f.Stat()
	fi2, _ := os.Stat(lockPath)
	if fi1 == nil || fi2 == nil || !os.SameFile(fi1, fi2) {
		f.Close()
		return nil, fmt.Errorf("filelock: lock file replaced during open, retry")
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, fmt.Errorf("filelock: flock: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
		f.Close()
	}, nil
}

func (u *unixLocker) WriteAtomic(path string, data []byte) error {
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
	return os.Rename(name, path)
}
