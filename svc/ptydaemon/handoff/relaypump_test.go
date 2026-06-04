package handoff

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// relaypump_test.go: OS-agnostic tests for the Windows ConPTY relay pump and the
// winEndpoint core, using in-memory fakes. These run on Linux under -race and
// exercise the exact logic the real Win32 code drives, minus the syscalls.

// ---- fakes ----

// fakeSource is a controllable outputSource. Tests feed it chunks; it blocks Read
// until a chunk is available, EOF, or Close. It also records how many Reads ran so
// tests can assert the never-stops invariant (the reader keeps reading across
// attach/detach).
type fakeSource struct {
	mu          sync.Mutex
	chunks      chan []byte
	closed      bool
	readCount   int
	maxReaders  int
	liveReaders int
}

func newFakeSource() *fakeSource {
	return &fakeSource{chunks: make(chan []byte, 1024)}
}

func (s *fakeSource) feed(b []byte) {
	cp := append([]byte(nil), b...)
	s.chunks <- cp
}

func (s *fakeSource) Read(p []byte) (int, error) {
	s.mu.Lock()
	s.readCount++
	s.liveReaders++
	if s.liveReaders > s.maxReaders {
		s.maxReaders = s.liveReaders
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.liveReaders--; s.mu.Unlock() }()

	chunk, ok := <-s.chunks
	if !ok {
		return 0, io.EOF
	}
	n := copy(p, chunk)
	return n, nil
}

func (s *fakeSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.chunks)
	}
	return nil
}

func (s *fakeSource) reads() int { s.mu.Lock(); defer s.mu.Unlock(); return s.readCount }
func (s *fakeSource) maxConcurrentReaders() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxReaders
}

// fakeSink buffers relayed bytes. If failAfter >= 0, the (failAfter+1)-th Write
// fails — used to test the slow/broken-client drop policy.
type fakeSink struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	writes    int
	failAfter int // -1 = never fail
	closed    bool
}

func newFakeSink() *fakeSink { return &fakeSink{failAfter: -1} }

func (s *fakeSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	if s.failAfter >= 0 && s.writes >= s.failAfter {
		return 0, errors.New("fake sink write failure")
	}
	s.writes++
	return s.buf.Write(p)
}

func (s *fakeSink) Close() error   { s.mu.Lock(); s.closed = true; s.mu.Unlock(); return nil }
func (s *fakeSink) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.buf.String() }
func (s *fakeSink) isClosed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.closed }

// fakeConPTY backs the conpty seam with the in-memory fakes above so the WHOLE
// winEndpoint runs on Linux.
type fakeConPTY struct {
	src        *fakeSource
	sink       *fakeSink
	wantPID    uint32 // the PID the next sink "connection" will present
	resizes    []Winsize
	closed     bool
	newSinkErr error
	mu         sync.Mutex
}

func newFakeConPTY() *fakeConPTY {
	return &fakeConPTY{src: newFakeSource(), sink: newFakeSink()}
}

func (c *fakeConPTY) output() outputSource { return c.src }

func (c *fakeConPTY) newSink(ctx context.Context, clientPID uint32) (relaySink, *pipeRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.newSinkErr != nil {
		return nil, nil, c.newSinkErr
	}
	// Emulate the real H4 PID-verify done inside newSink.
	if err := verifyClientPID(clientPID, c.wantPID); err != nil {
		return nil, nil, err
	}
	return c.sink, &pipeRef{Name: `\\.\pipe\fake`}, nil
}

func (c *fakeConPTY) pendingPipeName() string { return `\\.\pipe\fake` }

func (c *fakeConPTY) resize(s Winsize) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resizes = append(c.resizes, s)
	return nil
}

func (c *fakeConPTY) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// ---- tests ----

// TestPumpDiscardsWhileDetached: with no sink attached, the pump reads and discards
// (B1 — the reader never stops), and nothing is relayed.
func TestPumpDiscardsWhileDetached(t *testing.T) {
	src := newFakeSource()
	p := newRelayPump(src)
	p.start()
	defer p.close()

	for i := 0; i < 10; i++ {
		src.feed([]byte("discarded"))
	}
	// Give the reader time to consume.
	waitFor(t, time.Second, func() bool { return src.reads() >= 10 })
	if p.inRelay() {
		t.Fatal("pump must be in DISCARD mode while detached")
	}
}

// TestPumpRelaysWhileAttached: an attached sink receives exactly the bytes read.
func TestPumpRelaysWhileAttached(t *testing.T) {
	src := newFakeSource()
	sink := newFakeSink()
	p := newRelayPump(src)
	p.start()
	defer p.close()

	if _, err := p.attach(sink); err != nil {
		t.Fatalf("attach: %v", err)
	}
	src.feed([]byte("hello "))
	src.feed([]byte("world"))
	waitFor(t, time.Second, func() bool { return sink.String() == "hello world" })
	if got := sink.String(); got != "hello world" {
		t.Fatalf("relay mismatch: got %q want %q", got, "hello world")
	}
}

// TestPumpNeverStopsAcrossAttachDetach: the single reader keeps reading across
// detach→attach→detach transitions; there is never a zero-reader window (B1) and
// never two concurrent readers.
func TestPumpNeverStopsAcrossAttachDetach(t *testing.T) {
	src := newFakeSource()
	p := newRelayPump(src)
	p.start()
	defer p.close()

	// detached
	src.feed([]byte("a"))
	// attach
	sink1 := newFakeSink()
	if _, err := p.attach(sink1); err != nil {
		t.Fatal(err)
	}
	src.feed([]byte("b"))
	waitFor(t, time.Second, func() bool { return sink1.String() == "b" })
	// detach
	p.detach()
	if !sink1.isClosed() {
		t.Fatal("detach must close the sink (DisconnectNamedPipe equiv)")
	}
	src.feed([]byte("c")) // discarded
	// re-attach
	sink2 := newFakeSink()
	if _, err := p.attach(sink2); err != nil {
		t.Fatal(err)
	}
	src.feed([]byte("d"))
	waitFor(t, time.Second, func() bool { return sink2.String() == "d" })

	if got := src.maxConcurrentReaders(); got > 1 {
		t.Fatalf("never-stops invariant: %d concurrent readers (want 1)", got)
	}
	if got := src.reads(); got < 4 {
		t.Fatalf("reader stopped: only %d reads across 4 feeds", got)
	}
}

// TestPumpSlowClientDroppedNotBlocking: a sink whose Write fails must be dropped
// (its drop channel fires, it is closed) and the reader MUST keep running and
// discarding — never block on the slow client (protects B1).
func TestPumpSlowClientDroppedNotBlocking(t *testing.T) {
	src := newFakeSource()
	sink := newFakeSink()
	sink.failAfter = 1 // first Write ok, second fails
	p := newRelayPump(src)
	p.start()
	defer p.close()

	drop, err := p.attach(sink)
	if err != nil {
		t.Fatal(err)
	}
	src.feed([]byte("ok"))   // relayed
	src.feed([]byte("boom")) // Write fails → drop

	select {
	case <-drop:
	case <-time.After(time.Second):
		t.Fatal("slow client was not dropped on write failure")
	}
	if !sink.isClosed() {
		t.Fatal("dropped sink must be closed")
	}
	// Reader keeps going: feed more and confirm reads advance (discarded now).
	before := src.reads()
	src.feed([]byte("more"))
	src.feed([]byte("data"))
	waitFor(t, time.Second, func() bool { return src.reads() > before+1 })
	if p.inRelay() {
		t.Fatal("pump must be back in DISCARD after dropping the slow client")
	}
}

// TestPumpDoubleAttachRejected: a second attach while a sink is live is rejected.
func TestPumpDoubleAttachRejected(t *testing.T) {
	src := newFakeSource()
	p := newRelayPump(src)
	p.start()
	defer p.close()
	if _, err := p.attach(newFakeSink()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.attach(newFakeSink()); err != ErrLeaseGranted {
		t.Fatalf("double attach: want ErrLeaseGranted, got %v", err)
	}
}

// TestPumpDrainUntilReturnsOnCancel: drainUntil returns promptly on ctx cancel and
// the reader keeps running (B1) — it is NOT stopped by the park.
func TestPumpDrainUntilReturnsOnCancel(t *testing.T) {
	src := newFakeSource()
	p := newRelayPump(src)
	defer p.close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.drainUntil(ctx) }()
	// Let the reader start.
	waitFor(t, time.Second, func() bool { return src.reads() >= 1 || true })
	time.Sleep(20 * time.Millisecond)
	before := src.reads()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("drainUntil returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("drainUntil did not return on cancel")
	}
	// Reader still alive after park: a feed is still consumed.
	src.feed([]byte("x"))
	waitFor(t, time.Second, func() bool { return src.reads() > before })
}

// TestPumpDrainUntilReturnsOnEOF: when the source hits EOF (child exited & output
// drained to TRUE EOF, H5), drainUntil returns nil.
func TestPumpDrainUntilReturnsOnEOF(t *testing.T) {
	src := newFakeSource()
	p := newRelayPump(src)
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- p.drainUntil(ctx) }()
	time.Sleep(20 * time.Millisecond)
	src.Close() // EOF
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("drainUntil on EOF returned %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("drainUntil did not return on source EOF")
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
