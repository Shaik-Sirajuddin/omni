package handoff

import (
	"context"
	"errors"
	"sync"
)

// winendpoint_core.go is the PLATFORM-AGNOSTIC body of the Windows ConPTY relay
// Endpoint. It has NO syscall imports, so the discard↔relay toggle, the
// lease/Session integration, drain-to-EOF ordering, resize and close lifecycle are
// all unit-testable on Linux with fakes. The real Win32 mechanism is injected via
// the conpty seam (created in //go:build windows code).

// conpty is the seam over the real ConPTY + relay pipe machinery. The Windows impl
// (winConPTY in conpty_windows.go) backs each method with kernel32/advapi32 calls;
// fakeConPTY in the tests backs them with in-memory channels so the WHOLE endpoint
// runs on Linux.
type conpty interface {
	// output is the always-on ConPTY output reader the pump owns. It MUST keep
	// being read across attach/detach (B1).
	output() outputSource

	// newSink creates the relay named pipe (server side), waits for the client to
	// connect, verifies its PID == clientPID (H4) and returns the connected sink.
	// On PID mismatch it disconnects and returns *ErrPIDMismatch. The returned
	// pipeRef carries the name the client must open. Cancellable via ctx.
	newSink(ctx context.Context, clientPID uint32) (relaySink, *pipeRef, error)

	// resize maps to ResizePseudoConsole (M1).
	resize(size Winsize) error

	// close tears down the ConPTY and kills the child tree via the Job Object
	// (SIGHUP-equiv). Idempotent.
	close() error

	// pendingPipeName returns the relay pipe name currently being served (set by
	// newSink as soon as the pipe is created, BEFORE the client connects). It exists
	// so the connecting client / e2e test can learn the random name while Grant is
	// still blocked on ConnectNamedPipe. Empty when no sink is pending.
	pendingPipeName() string
}

// winEndpoint is the Endpoint implementation for Windows. It satisfies the §18.1
// behavioral contract over a relay rather than a shared fd.
type winEndpoint struct {
	cp   conpty
	pump *relayPump

	// clientPID is the PID the next Grant must PID-verify the relay connection
	// against (H4). It is set by the daemon out-of-band (over the control conn)
	// before Attach. SetClientPID updates it under mu.
	mu        sync.Mutex
	clientPID uint32
	closed    bool
	lastDrop  <-chan struct{}
}

// newWinEndpoint builds the endpoint over a conpty seam. Real callers pass a
// winConPTY; tests pass a fakeConPTY.
func newWinEndpoint(cp conpty) *winEndpoint {
	return &winEndpoint{cp: cp, pump: newRelayPump(cp.output())}
}

// SetClientPID records the PID the next relay connection must match (H4). The
// daemon learns it from the control handshake before attaching.
func (e *winEndpoint) SetClientPID(pid uint32) {
	e.mu.Lock()
	e.clientPID = pid
	e.mu.Unlock()
}

// Drain maps to the DISCARD phase. CRITICAL (B1): it does NOT stop the ConPTY
// reader — it starts (if needed) the always-on pump and blocks until ctx is
// cancelled (the park signal) or the child exits and output drains to TRUE EOF
// (H5). Returns promptly on cancel with no torn byte: the pump is mid-Read on a
// 32 KiB boundary and simply stays in DISCARD mode; no partial chunk is exposed to
// any client because no client is attached while draining.
func (e *winEndpoint) Drain(ctx context.Context) error {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return errors.New("handoff: endpoint closed")
	}
	return e.pump.drainUntil(ctx)
}

// Grant flips the pump into RELAY mode. PRECONDITION (Session guarantees it): Drain
// has returned, i.e. we are parked in DISCARD. On Windows "parked" still means the
// reader is running — Grant just creates the PID-verified relay sink and swaps it
// in. The client opens the returned pipe name by CreateFile.
func (e *winEndpoint) Grant() (HandoffRef, error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, errors.New("handoff: endpoint closed")
	}
	pid := e.clientPID
	e.mu.Unlock()

	// Bound the wait for the client to connect so Grant can never hang the Session.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink, ref, err := e.cp.newSink(ctx, pid)
	if err != nil {
		return nil, err
	}
	drop, err := e.pump.attach(sink)
	if err != nil {
		_ = sink.Close()
		return nil, err
	}
	e.mu.Lock()
	e.lastDrop = drop
	e.mu.Unlock()
	return ref, nil
}

// Revoke forcibly reclaims the read side: it drops the relay sink
// (DisconnectNamedPipe — server-only authority, the steal) and returns the pump to
// DISCARD. The always-on reader is unaffected (B1). Idempotent.
func (e *winEndpoint) Revoke(context.Context) error {
	e.pump.detach()
	return nil
}

// Resize maps to ResizePseudoConsole (M1), independent of who holds the read side.
func (e *winEndpoint) Resize(size Winsize) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return errors.New("handoff: endpoint closed")
	}
	e.mu.Unlock()
	return e.cp.resize(size)
}

// Close stops the pump (and underlying ConPTY reader) and tears down the ConPTY,
// killing the child tree via the Job Object. Idempotent.
func (e *winEndpoint) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.mu.Unlock()

	perr := e.pump.close()
	cerr := e.cp.close()
	if cerr != nil {
		return cerr
	}
	return perr
}

// pendingPipeName exposes the relay pipe name being served (delegates to the seam),
// so the connecting client learns the random name while Grant blocks on connect.
func (e *winEndpoint) pendingPipeName() string { return e.cp.pendingPipeName() }

// dropSignal exposes the current relay drop channel (for the daemon's control
// reader to learn when the client was disconnected by a steal or went away).
func (e *winEndpoint) dropSignal() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastDrop
}

var _ Endpoint = (*winEndpoint)(nil)
