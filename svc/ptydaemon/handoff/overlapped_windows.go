//go:build windows

package handoff

import (
	"io"
	"sync"

	"golang.org/x/sys/windows"
)

// overlapped_windows.go: the blockingReader (ConPTY output source) and the relay
// pipe sink.

// blockingReader is the SOLE reader of the ConPTY output ANONYMOUS pipe (B1).
// Anonymous pipes do not support overlapped I/O, and ConPTY does not reliably emit
// to a named-pipe output, so the proven model is a plain blocking ReadFile in the
// always-on pump goroutine, cancelled by CLOSING the handle: a CloseHandle on the
// read end makes the in-flight blocking ReadFile return ERROR_*, which we map to
// io.EOF. This is exactly how battle-tested Go ConPTY libraries do it, and it fits
// the relay design where the reader never pauses mid-session (only at teardown).
//
// Child-reap / EOF (H5): EOF is NOT gated on the process handle. The read returns
// EOF only when the pipe itself ends (ERROR_BROKEN_PIPE / 0-byte), which happens
// after ConPTY has flushed every buffered byte, so final output is never truncated.
type blockingReader struct {
	h windows.Handle

	mu     sync.Mutex
	closed bool
}

func newBlockingReader(h windows.Handle) *blockingReader {
	return &blockingReader{h: h}
}

// Read does one blocking ReadFile. Returns io.EOF at true pipe end or once Close
// has closed the handle (the blocked ReadFile then returns an error we map to EOF).
func (r *blockingReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0, io.EOF
	}
	r.mu.Unlock()

	var done uint32
	err := windows.ReadFile(r.h, p, &done, nil) // blocking

	switch err {
	case nil:
		if done == 0 {
			return 0, io.EOF
		}
		return int(done), nil
	case windows.ERROR_BROKEN_PIPE, windows.ERROR_HANDLE_EOF:
		return int(done), io.EOF // true EOF: ConPTY flushed everything (H5)
	default:
		// ERROR_INVALID_HANDLE / ERROR_OPERATION_ABORTED after Close closed the
		// handle, or any other error: treat a post-close error as EOF.
		r.mu.Lock()
		closed := r.closed
		r.mu.Unlock()
		if closed {
			return int(done), io.EOF
		}
		return int(done), err
	}
}

// Close marks the reader closed and closes the handle, which unblocks any in-flight
// blocking ReadFile so the pump loop exits. Idempotent.
func (r *blockingReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	return windows.CloseHandle(r.h)
}

var _ outputSource = (*blockingReader)(nil)

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
