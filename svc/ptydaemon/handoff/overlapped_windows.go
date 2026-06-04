//go:build windows

package handoff

import (
	"io"
	"sync"

	"golang.org/x/sys/windows"
)

// overlapped_windows.go: the overlappedReader (outputSource impl) and the relay
// pipe sink. Both use overlapped I/O so the daemon's always-on ConPTY reader is
// cancellable (M3) without ever stopping reading mid-session (B1).

// overlappedReader reads the ConPTY output pipe with overlapped I/O. It is the
// SOLE reader (B1). Cancellation discipline (M3): Close() calls CancelIoEx THEN
// GetOverlappedResult(bWait=TRUE) so any byte that raced the cancel is accounted
// for before we declare the read parked/closed — never leave an overlapped op in
// flight.
//
// Child-reap / EOF (H5): the reader does NOT gate EOF on the process handle. It
// keeps reading until the pipe itself returns ERROR_BROKEN_PIPE / 0-byte EOF (which
// only happens AFTER the ConPTY has flushed every buffered byte), so the agent's
// final output is never truncated. The proc handle is held only for kill/reap by
// the Job Object, not consulted to decide EOF.
type overlappedReader struct {
	h    windows.Handle
	proc windows.Handle
	ev   windows.Handle

	mu     sync.Mutex
	ov     windows.Overlapped
	closed bool
	inFlt  bool // an overlapped read is in flight
}

func newOverlappedReader(h, proc windows.Handle) *overlappedReader {
	ev, _ := windows.CreateEvent(nil, 1, 0, nil) // manual-reset
	r := &overlappedReader{h: h, proc: proc, ev: ev}
	r.ov.HEvent = ev
	return r
}

// Read performs one overlapped ReadFile and blocks (via WaitForSingleObject on the
// event) until bytes arrive, EOF, or Close cancels it. Returns io.EOF at true pipe
// end (H5).
func (r *overlappedReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0, io.EOF
	}
	windows.ResetEvent(r.ev)
	r.inFlt = true
	r.mu.Unlock()

	var done uint32
	err := windows.ReadFile(r.h, p, &done, &r.ov)
	if err == windows.ERROR_IO_PENDING {
		// Wait for completion or cancellation.
		_, _ = windows.WaitForSingleObject(r.ev, windows.INFINITE)
		err = windows.GetOverlappedResult(r.h, &r.ov, &done, true)
	}

	r.mu.Lock()
	r.inFlt = false
	closed := r.closed
	r.mu.Unlock()

	switch err {
	case nil:
		if done == 0 {
			return 0, io.EOF
		}
		return int(done), nil
	case windows.ERROR_BROKEN_PIPE, windows.ERROR_HANDLE_EOF:
		return int(done), io.EOF // true EOF: ConPTY flushed everything (H5)
	case windows.ERROR_OPERATION_ABORTED:
		if closed {
			return int(done), io.EOF
		}
		return int(done), nil // cancelled but not closed; caller may retry
	default:
		return int(done), err
	}
}

// Close cancels any in-flight read with the M3 discipline (CancelIoEx THEN
// GetOverlappedResult(bWait=TRUE)) and closes the handle. Idempotent.
func (r *overlappedReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	inFlt := r.inFlt
	r.mu.Unlock()

	if inFlt {
		// M3: cancel, THEN wait for the cancellation to land before proceeding.
		windows.CancelIoEx(r.h, &r.ov)
		var done uint32
		_ = windows.GetOverlappedResult(r.h, &r.ov, &done, true)
	}
	windows.SetEvent(r.ev) // wake any waiter
	if r.ev != 0 {
		windows.CloseHandle(r.ev)
	}
	// Note: r.h (outRead) is owned/closed by winConPTY.close(); we don't close it
	// here to avoid a double close.
	return nil
}

var _ outputSource = (*overlappedReader)(nil)

// pipeSink writes relayed bytes to the connected relay named pipe (overlapped).
// Close = DisconnectNamedPipe + CloseHandle (the server-only "steal").
type pipeSink struct {
	h  windows.Handle
	ev windows.Handle

	mu     sync.Mutex
	ov     windows.Overlapped
	closed bool
}

func newPipeSink(h windows.Handle) *pipeSink {
	ev, _ := windows.CreateEvent(nil, 1, 0, nil)
	s := &pipeSink{h: h, ev: ev}
	s.ov.HEvent = ev
	return s
}

func (s *pipeSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	windows.ResetEvent(s.ev)
	s.mu.Unlock()

	var done uint32
	err := windows.WriteFile(s.h, p, &done, &s.ov)
	if err == windows.ERROR_IO_PENDING {
		_, _ = windows.WaitForSingleObject(s.ev, windows.INFINITE)
		err = windows.GetOverlappedResult(s.h, &s.ov, &done, true)
	}
	if err != nil {
		return int(done), err
	}
	return int(done), nil
}

// Close disconnects and closes the relay pipe. This is the steal mechanism
// (DisconnectNamedPipe drops the client's reader). Idempotent.
func (s *pipeSink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	windows.DisconnectNamedPipe(s.h)
	windows.CloseHandle(s.h)
	if s.ev != 0 {
		windows.CloseHandle(s.ev)
	}
	return nil
}

var _ relaySink = (*pipeSink)(nil)
