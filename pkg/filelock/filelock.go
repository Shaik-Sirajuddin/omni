package filelock

// Locker provides cross-platform advisory file locking and atomic writes.
// All methods operate on a sidecar lock file (lockPath), not on the config file itself.
type Locker interface {
	// Lock acquires an exclusive lock on lockPath, blocking until available.
	// Returns an unlock function the caller must invoke when done.
	Lock(lockPath string) (unlock func(), err error)

	// RLock acquires a shared lock on lockPath (multiple readers, one writer).
	RLock(lockPath string) (unlock func(), err error)

	// WriteAtomic writes data to the config file at path atomically (temp file +
	// Sync + rename). path is the config file (e.g. config.toml); lockPath passed
	// to Lock/RLock is the sidecar lock file (e.g. .config.toml.lock). Must be
	// called while holding Lock.
	WriteAtomic(path string, data []byte) error
}

// New returns a platform-appropriate Locker (selected at compile time via build tags).
// Declared in unix.go and windows.go respectively.
