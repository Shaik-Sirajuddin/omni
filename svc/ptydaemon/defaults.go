package ptydaemon

import "github.com/Shaik-Sirajuddin/memory/pkg/sockpath"

// DefaultSocketPath returns the per-user Unix socket path for the PTY daemon.
func DefaultSocketPath() string {
	return sockpath.PTY()
}
