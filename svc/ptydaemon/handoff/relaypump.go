package handoff

import (
	"context"
	"errors"
	"io"
	"sync"
)

// relaypump.go holds the PLATFORM-AGNOSTIC core of the Windows ConPTY relay (the
// B1 fix). It has NO syscall imports so it compiles and unit-tests on Linux. The
// raw Win32 mechanism (ConPTY, overlapped pipe I/O, SDDL, Job Object) lives behind
// the tiny seams declared here and is implemented only in //go:build windows files.
//
// THE B1 INVARIANT: the pump is the SOLE, ALWAYS-ON reader of the ConPTY output.
// It NEVER stops reading. A zero-reader moment deadlocks ConPTY (its render is
// synchronous; a full output pipe stalls input processing → the child hangs). So
// the pump toggles between two sinks but never pauses the read:
//
//	DISCARD  — detached: bytes are read and thrown away (the daemon "drains").
//	RELAY    — attached: bytes are read and copied to the connected client.
//
// "Park" (what Session.Drain(ctx)+cancel does before a Grant) must NOT stop the
// reader on Windows. Drain(ctx) here simply represents "be in DISCARD mode until
// ctx is cancelled"; the underlying read loop keeps running across Drain↔Grant.

// outputSource is the seam over the ConPTY output pipe. The real Windows impl does
// overlapped reads with the CancelIoEx + GetOverlappedResult(bWait=TRUE) park
// discipline (M3); fakes implement it with channels for Linux unit tests.
//
// Read blocks until at least one byte is available, EOF, or error. It MUST honour
// the never-stops invariant: the pump calls Read in a tight loop regardless of
// attach state.
type outputSource interface {
	io.Reader
	// Close unblocks any in-flight Read and makes subsequent Reads return EOF/err.
	Close() error
}

// relaySink is the seam over the connected client. On Windows it is the relay
// named pipe (server side) AFTER a PID-verified ConnectNamedPipe; fakes implement
// it with a buffer for tests. Write copies one chunk to the client.
type relaySink interface {
	io.Writer
	// Close disconnects the client (Windows: DisconnectNamedPipe). Idempotent.
	Close() error
}

// errSlowClient is recorded (not returned to the caller) when the relay sink can't
// keep up and the slow-client policy drops the client rather than ever blocking the
// ConPTY reader (which would re-introduce B1 from the other side).
var errSlowClient = errors.New("handoff: relay client too slow; dropped to protect ConPTY reader")

// relayPump owns the single always-on reader and the discard↔relay toggle. It is
// the testable heart of winEndpoint.
type relayPump struct {
	src outputSource

	mu       sync.Mutex
	sink     relaySink     // non-nil ⇒ RELAY mode; nil ⇒ DISCARD mode
	sinkDrop chan struct{} // closed when the current sink is dropped (slow/steal/EOF)
	closed   bool
	loopDone chan struct{}
	started  bool

	// readSize bounds a single Read/Write chunk. Exported-ish for tests.
	readSize int
}

func newRelayPump(src outputSource) *relayPump {
	return &relayPump{src: src, readSize: 32 * 1024}
}

// start launches the single always-on read loop. Idempotent.
func (p *relayPump) start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started || p.closed {
		return
	}
	p.started = true
	p.loopDone = make(chan struct{})
	go p.loop()
}

// loop is the SOLE ConPTY reader. It never pauses: every iteration reads, then
// either discards (detached) or relays (attached). On client write error it drops
// the client (slow-client policy) but KEEPS reading.
func (p *relayPump) loop() {
	defer close(p.loopDone)
	buf := make([]byte, p.readSize)
	for {
		n, rerr := p.src.Read(buf)
		if n > 0 {
			p.dispatch(buf[:n])
		}
		if rerr != nil {
			// Source EOF/closed: the child reaper drains to TRUE EOF (H5) before
			// closing src, so reaching here means all output has been relayed.
			p.dropSink(nil)
			return
		}
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return
		}
	}
}

// dispatch routes one chunk to the active sink, or discards it. It NEVER blocks the
// reader on a slow client: a Write error drops the client and the bytes are
// discarded from here on until the next attach.
func (p *relayPump) dispatch(chunk []byte) {
	p.mu.Lock()
	sink := p.sink
	p.mu.Unlock()
	if sink == nil {
		return // DISCARD
	}
	if _, err := sink.Write(chunk); err != nil {
		// Slow/broken client → drop it (protects the always-on reader, B1).
		p.dropSink(errSlowClient)
	}
}

// attach puts the pump into RELAY mode with the given sink. PRECONDITION: the
// Session has parked (Drain returned) — but on Windows "park" never stopped the
// reader, so attach only swaps the sink pointer. Returns a drop channel the caller
// can watch to learn when the sink is revoked/broken.
func (p *relayPump) attach(sink relaySink) (<-chan struct{}, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errors.New("handoff: relay pump closed")
	}
	if p.sink != nil {
		return nil, ErrLeaseGranted
	}
	p.sink = sink
	p.sinkDrop = make(chan struct{})
	return p.sinkDrop, nil
}

// detach returns the pump to DISCARD mode and drops the current sink. Idempotent.
func (p *relayPump) detach() { p.dropSink(nil) }

// dropSink closes & clears the active sink and signals its drop channel. Idempotent
// and safe to call from both the reader loop and the control path. cause is for
// observability only; it is not surfaced to callers (the drop channel is the
// signal).
func (p *relayPump) dropSink(_ error) {
	p.mu.Lock()
	sink := p.sink
	drop := p.sinkDrop
	p.sink = nil
	p.sinkDrop = nil
	p.mu.Unlock()
	if sink != nil {
		_ = sink.Close()
	}
	if drop != nil {
		select {
		case <-drop:
		default:
			close(drop)
		}
	}
}

// inRelay reports whether the pump is currently relaying (a client is attached).
func (p *relayPump) inRelay() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sink != nil
}

// drainUntil implements the DISCARD phase that Session.Drain(ctx) maps to. It does
// NOT stop the reader (B1) — it just blocks until ctx is cancelled (the park
// signal) or the pump closes. While here the reader is in DISCARD mode (no sink),
// so bytes are read-and-thrown-away exactly as the Endpoint contract requires.
func (p *relayPump) drainUntil(ctx context.Context) error {
	p.start() // ensure the always-on reader is running
	// If the pump was already closed, start() returned without setting loopDone.
	// Reading a nil channel blocks forever, so take a closed fast-path instead of
	// relying on ctx to rescue us. (Through Session, which parks before closing,
	// this is unreachable — but drainUntil must not deadlock if called directly.)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	done := p.loopDone
	p.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil // source hit EOF (child exited and output drained, H5)
	}
}

// close stops the pump and the underlying source. Idempotent.
func (p *relayPump) close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	started := p.started
	done := p.loopDone
	p.mu.Unlock()

	p.dropSink(nil)
	err := p.src.Close() // unblocks the in-flight Read so loop() returns
	if started && done != nil {
		<-done
	}
	return err
}
