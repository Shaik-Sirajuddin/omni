// Package handoff implements the single-client PTY output handoff fast path:
// the daemon owns the output stream and LENDS it to at most one client for
// zero-copy direct reads (Unix: SCM_RIGHTS fd; Windows: relay over a named pipe).
//
// The concurrency core (Lease, Session) is platform-agnostic and lives in
// untagged files. Platform mechanism lives behind the Endpoint interface in
// build-tagged files (endpoint_unix.go / endpoint_windows.go) so a Linux binary
// never compiles ConPTY/x/sys/windows and vice versa.
//
// See generated/handoff_fastpath_plan.md for the full design and the OS-compat
// review (§19) that shaped it.
package handoff

import (
	"context"
	"net"
)

// Winsize is the shared terminal size type (chars).
type Winsize struct {
	Cols, Rows uint16
}

// Transport tells the client how to open the lent output stream.
type Transport int

const (
	// TransportSCMFD: the client receives a dup'd PTY master fd via ancillary
	// data and reads it directly (Unix, zero-copy).
	TransportSCMFD Transport = iota
	// TransportNamedPipe: the client opens a named pipe by name and reads bytes
	// the daemon relays from the ConPTY (Windows).
	TransportNamedPipe
)

func (t Transport) String() string {
	switch t {
	case TransportSCMFD:
		return "scm-fd"
	case TransportNamedPipe:
		return "named-pipe"
	default:
		return "unknown"
	}
}

// Endpoint is the OS-specific readable output the daemon OWNS and lends to a
// single client. The daemon is always the authority; clients only borrow.
//
// Behavioral contract (every impl MUST uphold it; see plan §18.1):
//   - Drain(ctx) returns promptly on cancel with NO torn byte at the boundary.
//   - Grant yields a client-openable ref; precondition: Drain has returned.
//   - Revoke forcibly reclaims the read side ("steal back"); idempotent.
//   - Read paths surface EOF only after all output has drained.
//   - Resize/Close manage size and lifecycle.
type Endpoint interface {
	// Drain reads-and-discards on the daemon side so the child never blocks on a
	// full buffer while detached. It MUST return promptly when ctx is cancelled —
	// that cancellation is how Session parks the reader before a Grant.
	Drain(ctx context.Context) error

	// Grant yields the read side and returns an OS-specific ref for the client.
	// PRECONDITION: Drain has returned (reader parked).
	Grant() (HandoffRef, error)

	// Revoke forcibly reclaims the read side. Only the daemon ever calls this.
	Revoke(ctx context.Context) error

	// Resize sets the terminal size; independent of who holds the read side.
	Resize(size Winsize) error

	// Close tears down the endpoint (and, per contract, the child tree).
	Close() error
}

// HandoffRef is the OS-specific token handed to the client.
type HandoffRef interface {
	Transport() Transport
}

// HandoffSender writes a HandoffRef to a client control connection. Unix uses
// SCM_RIGHTS ancillary data; Windows sends the relay pipe name.
type HandoffSender interface {
	Send(conn net.Conn, ref HandoffRef) error
}
