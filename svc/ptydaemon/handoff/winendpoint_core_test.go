package handoff

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// winendpoint_core_test.go: OS-agnostic tests for the winEndpoint core driven
// through the real Session/Lease concurrency contract, plus the PID-verify decision
// and the handoff-message wire protocol. All run on Linux with fakeConPTY.

// TestWinEndpointSatisfiesSessionContract drives the winEndpoint through the SAME
// Session that the unix endpoint uses: Start (drain) → Attach (grant relay) →
// Steal (disconnect) → back to drain. It proves the Windows relay reconciles with
// the park-before-grant / asymmetric-lease core without any Win32 calls.
func TestWinEndpointSatisfiesSessionContract(t *testing.T) {
	fc := newFakeConPTY()
	fc.wantPID = 4242
	ep := newWinEndpoint(fc)
	ep.SetClientPID(4242)

	s := NewSession(ep, pipeSender{})
	s.Start()
	defer s.Close()

	// While detached, feed bytes — they must be discarded (no client), reader alive.
	fc.src.feed([]byte("detached-bytes"))
	waitFor(t, time.Second, func() bool { return fc.src.reads() >= 1 })

	// Attach: parks the drain (DISCARD) and grants the relay (RELAY).
	revoke, err := s.Attach(newPipeConn(t))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if s.Owner() {
		t.Fatal("must not be daemon-owned while granted")
	}
	// Bytes now relay to the client sink.
	fc.src.feed([]byte("live"))
	waitFor(t, time.Second, func() bool { return fc.sink.String() == "live" })
	if got := fc.sink.String(); got != "live" {
		t.Fatalf("relay after attach: got %q want %q", got, "live")
	}

	// Steal back: Session revokes the lease and the endpoint disconnects the relay.
	go func() { <-revoke; s.AckYield() }()
	s.Steal(context.Background())
	if !s.Owner() {
		t.Fatal("daemon must own after steal")
	}
	if !fc.sink.isClosed() {
		t.Fatal("steal must disconnect the relay sink (DisconnectNamedPipe equiv)")
	}
}

// TestWinEndpointPIDVerifyRejects: when the connecting PID doesn't match, Grant
// (via newSink) fails and the lease is NOT held — the daemon resumes draining.
func TestWinEndpointPIDVerifyRejects(t *testing.T) {
	fc := newFakeConPTY()
	fc.wantPID = 9999 // the process that will "connect"
	ep := newWinEndpoint(fc)
	ep.SetClientPID(1234) // but we expected a different client

	s := NewSession(ep, pipeSender{})
	s.Start()
	defer s.Close()

	_, err := s.Attach(newPipeConn(t))
	if err == nil {
		t.Fatal("Attach must fail when the relay PID does not match the expected client")
	}
	var mism *ErrPIDMismatch
	if !errors.As(err, &mism) {
		t.Fatalf("want *ErrPIDMismatch, got %T: %v", err, err)
	}
	// Lease was not granted; daemon keeps the read side and resumes draining.
	if !s.Owner() {
		t.Fatal("daemon must remain owner after a rejected (PID-mismatch) attach")
	}
	fc.src.feed([]byte("still-draining"))
	waitFor(t, time.Second, func() bool { return fc.src.reads() >= 1 })
}

// TestVerifyClientPID covers the pure decision directly.
func TestVerifyClientPID(t *testing.T) {
	if err := verifyClientPID(100, 100); err != nil {
		t.Fatalf("matching PIDs must pass, got %v", err)
	}
	if err := verifyClientPID(100, 101); err == nil {
		t.Fatal("mismatched PIDs must be rejected")
	}
	if err := verifyClientPID(0, 0); err == nil {
		t.Fatal("zero expected PID must be rejected (unverifiable)")
	}
	var mism *ErrPIDMismatch
	if err := verifyClientPID(7, 8); !errors.As(err, &mism) || mism.Want != 7 || mism.Got != 8 {
		t.Fatalf("error must carry want/got: %v", err)
	}
}

// TestWinEndpointResizeAndClose: Resize forwards to the seam (ResizePseudoConsole
// equiv) regardless of lease state; Close is idempotent and tears down the ConPTY.
func TestWinEndpointResizeAndClose(t *testing.T) {
	fc := newFakeConPTY()
	ep := newWinEndpoint(fc)

	want := Winsize{Cols: 120, Rows: 40}
	if err := ep.Resize(want); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if len(fc.resizes) != 1 || fc.resizes[0] != want {
		t.Fatalf("resize not forwarded: %v", fc.resizes)
	}
	if err := ep.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !fc.closed {
		t.Fatal("Close must tear down the ConPTY seam (Job Object kill-tree equiv)")
	}
	if err := ep.Close(); err != nil {
		t.Fatalf("second Close must be nil, got %v", err)
	}
	if err := ep.Resize(want); err == nil {
		t.Fatal("Resize after Close should error")
	}
}

// TestWinEndpointDrainToEOFOrdering: bytes fed BEFORE the source EOF must all be
// relayed to an attached client before Drain observes EOF — the daemon never gates
// EOF on the proc handle and never truncates the final output (H5).
func TestWinEndpointDrainToEOFOrdering(t *testing.T) {
	fc := newFakeConPTY()
	fc.wantPID = 5
	ep := newWinEndpoint(fc)
	ep.SetClientPID(5)

	// Attach a client directly (bypass Session to focus on read→relay→EOF order).
	ref, err := ep.Grant()
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if ref.Transport() != TransportNamedPipe {
		t.Fatalf("ref transport: got %v want named-pipe", ref.Transport())
	}

	// Run Drain; it should return nil only AFTER the final bytes have been relayed.
	drainErr := make(chan error, 1)
	go func() { drainErr <- ep.Drain(context.Background()) }()

	fc.src.feed([]byte("final-output-tail"))
	// Now signal TRUE EOF.
	fc.src.Close()

	select {
	case err := <-drainErr:
		if err != nil {
			t.Fatalf("Drain at EOF returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not return at source EOF")
	}
	// The pre-EOF bytes must have reached the client (no truncation, H5).
	if got := fc.sink.String(); got != "final-output-tail" {
		t.Fatalf("final output truncated: got %q want %q", got, "final-output-tail")
	}
}

// TestHandoffMsgRoundTrip: winSender.Send emits a pipe-name JSON the client decodes
// back to the same name. This is the Windows analog of the SCM_RIGHTS send.
func TestHandoffMsgRoundTrip(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()

	ref := &pipeRef{Name: `\\.\pipe\axo-handoff-deadbeef`}
	go func() { _ = pipeSender{}.Send(srv, ref) }()

	buf := make([]byte, 256)
	_ = cli.SetReadDeadline(time.Now().Add(time.Second))
	n, err := cli.Read(buf)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	line := buf[:n]
	if line[len(line)-1] != '\n' {
		t.Fatalf("handoff message must be newline-terminated: %q", line)
	}
	got, err := decodeHandoffMsg(line[:len(line)-1])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != ref.Name {
		t.Fatalf("round-trip name: got %q want %q", got.Name, ref.Name)
	}
	if got.Transport() != TransportNamedPipe {
		t.Fatalf("round-trip transport: got %v", got.Transport())
	}
}

// TestWinSenderRejectsWrongRef: the sender refuses a non-pipeRef (e.g. an scm ref).
func TestWinSenderRejectsWrongRef(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()
	if err := (pipeSender{}).Send(srv, fakeRef{}); err == nil {
		t.Fatal("winSender must reject a non-pipeRef HandoffRef")
	}
}

// newPipeConn returns one end of a net.Pipe as the control conn for Attach. The
// fake sender doesn't actually write to it in these tests (fakeConPTY.newSink
// short-circuits), but Session.Attach needs a non-nil conn to pass to Send.
func newPipeConn(t *testing.T) net.Conn {
	t.Helper()
	srv, cli := net.Pipe()
	t.Cleanup(func() { srv.Close(); cli.Close() })
	// Drain the client side so a Send never blocks on the unbuffered net.Pipe.
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := cli.Read(buf); err != nil {
				return
			}
		}
	}()
	return srv
}
