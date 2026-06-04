//go:build linux || darwin

package handoff

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/pkg/pty"
	"golang.org/x/sys/unix"
)

// recvFD reads one message from a *net.UnixConn and extracts a single fd passed
// via SCM_RIGHTS. It mirrors what a real client control-reader does.
func recvFD(t *testing.T, uc *net.UnixConn) (int, []byte) {
	t.Helper()
	buf := make([]byte, 64)
	oob := make([]byte, 64)
	n, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
	if err != nil {
		t.Fatalf("ReadMsgUnix: %v", err)
	}
	scms, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		t.Fatalf("ParseSocketControlMessage: %v", err)
	}
	if len(scms) != 1 {
		t.Fatalf("expected exactly 1 control message, got %d", len(scms))
	}
	fds, err := unix.ParseUnixRights(&scms[0])
	if err != nil {
		t.Fatalf("ParseUnixRights: %v", err)
	}
	if len(fds) != 1 {
		t.Fatalf("expected exactly 1 fd, got %d", len(fds))
	}
	return fds[0], buf[:n]
}

// readUntil reads from f until it has seen want as a substring or the deadline
// elapses, returning everything read so far.
func readUntil(t *testing.T, f *os.File, want []byte, deadline time.Duration) []byte {
	t.Helper()
	var got []byte
	end := time.Now().Add(deadline)
	tmp := make([]byte, 4096)
	for time.Now().Before(end) {
		_ = f.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := f.Read(tmp)
		if n > 0 {
			got = append(got, tmp[:n]...)
			if bytes.Contains(got, want) {
				return got
			}
		}
		if err != nil {
			if os.IsTimeout(err) {
				continue
			}
			// EIO on a master after slave close is expected; stop reading.
			break
		}
	}
	return got
}

// TestUnixEndpointSCMRightsEndToEnd is the strict end-to-end verification of
// invariant 5: a REAL PTY master is lent to a client over a socketpair via
// SCM_RIGHTS, and the client reads the SAME bytes the child wrote.
func TestUnixEndpointSCMRightsEndToEnd(t *testing.T) {
	// Real PTY with a child that writes a known marker to its stdout (the slave).
	marker := "HANDOFF-MARKER-7f3a9c-" + t.Name()
	cmd := exec.Command("/bin/sh", "-c", "printf '%s\\n' '"+marker+"'; sleep 5")
	p, err := pty.StartCmd(cmd)
	if err != nil {
		t.Fatalf("StartCmd: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	defer p.Close()

	ep := NewUnixEndpoint(p.Master())
	sender := NewSender()
	s := NewSession(ep, sender)
	s.Start() // daemon drains (discards) while detached — child never blocks

	// Build a real socketpair as the client control connection.
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	srvFile := os.NewFile(uintptr(fds[0]), "srv")
	cliFile := os.NewFile(uintptr(fds[1]), "cli")
	srvConn, err := net.FileConn(srvFile)
	if err != nil {
		t.Fatalf("FileConn srv: %v", err)
	}
	_ = srvFile.Close()
	cliConn, err := net.FileConn(cliFile)
	if err != nil {
		t.Fatalf("FileConn cli: %v", err)
	}
	_ = cliFile.Close()
	defer srvConn.Close()
	defer cliConn.Close()

	// Attach: parks the drainer, dups the master, sends it over SCM_RIGHTS.
	revoke, err := s.Attach(srvConn)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	_ = revoke

	// Client receives the lent fd and reads the child's output directly.
	rawFD, ctrl := recvFD(t, cliConn.(*net.UnixConn))
	if string(ctrl) != "ok\n" {
		t.Fatalf("control payload: got %q want %q", ctrl, "ok\n")
	}
	lent := os.NewFile(uintptr(rawFD), "lent-master")
	defer lent.Close()

	got := readUntil(t, lent, []byte(marker), 4*time.Second)

	// STRICT byte comparison: the bytes the child wrote must appear verbatim in
	// what the lent fd read.
	want := []byte(marker)
	if !bytes.Contains(got, want) {
		t.Fatalf("lent fd did NOT receive the child's bytes.\n  child wrote: %q\n  fd received: %q", want, got)
	}
	t.Logf("SCM_RIGHTS e2e OK: child wrote %q; lent fd received %q", marker, string(bytes.TrimRight(got, "\x00")))
}

// TestUnixSendClosesFD asserts the sender does not leak the dup'd fd: after Send
// the daemon-side fd number must be closed (a subsequent Close on it fails).
func TestUnixSendClosesFD(t *testing.T) {
	p, err := pty.Open(80, 24)
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer p.Close()
	ep := NewUnixEndpoint(p.Master())

	ref, err := ep.Grant()
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	sr := ref.(*scmRef)
	dupFD := sr.fd

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	srvFile := os.NewFile(uintptr(fds[0]), "srv")
	cliFile := os.NewFile(uintptr(fds[1]), "cli")
	srvConn, _ := net.FileConn(srvFile)
	_ = srvFile.Close()
	cliConn, _ := net.FileConn(cliFile)
	_ = cliFile.Close()
	defer srvConn.Close()
	defer cliConn.Close()

	if err := NewSender().Send(srvConn, ref); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Drain the receiver so the kernel actually transfers (and we don't leak the
	// received fd).
	rawFD, _ := recvFD(t, cliConn.(*net.UnixConn))
	_ = unix.Close(rawFD)

	// The daemon-side dup must have been closed by Send. Closing it again errors.
	if err := unix.Close(dupFD); err == nil {
		t.Fatalf("sender leaked fd %d: it was still open after Send", dupFD)
	}
}

// TestUnixDrainCancellationPromptness asserts invariant: Drain returns promptly
// on ctx cancel (the park signal) via the self-pipe, even with no PTY activity.
func TestUnixDrainCancellationPromptness(t *testing.T) {
	p, err := pty.Open(80, 24)
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer p.Close()
	ep := NewUnixEndpoint(p.Master())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ep.Drain(ctx) }()

	// Let the drainer settle into poll().
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if el := time.Since(start); el > 500*time.Millisecond {
			t.Fatalf("Drain took %v to cancel; expected prompt", el)
		}
		if err != context.Canceled {
			t.Fatalf("Drain returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not return after cancel (self-pipe not promptly wired)")
	}
}

// TestUnixDrainDiscardsWhileDetached asserts the child does not block on a full
// PTY buffer while detached: the drainer keeps reading-and-discarding so a child
// that writes far more than a tty buffer still makes progress and exits.
func TestUnixDrainDiscardsWhileDetached(t *testing.T) {
	// Write ~1 MiB to the slave then exit. Without an active drainer this would
	// block on the tty buffer; with Drain discarding it must complete.
	cmd := exec.Command("/bin/sh", "-c", "i=0; while [ $i -lt 16384 ]; do printf 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\\n'; i=$((i+1)); done")
	p, err := pty.StartCmd(cmd)
	if err != nil {
		t.Fatalf("StartCmd: %v", err)
	}
	defer p.Close()
	ep := NewUnixEndpoint(p.Master())

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = ep.Drain(ctx) }()

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case <-waitErr: // child finished writing 1MiB → drainer kept it unblocked
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child blocked on full PTY buffer; drainer did not discard")
	}
	cancel()
	wg.Wait()
}

// TestUnixResizeTIOCSWINSZ asserts invariant 6: Resize maps to TIOCSWINSZ and is
// observable via GetWinsize on the master.
func TestUnixResizeTIOCSWINSZ(t *testing.T) {
	p, err := pty.Open(80, 24)
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer p.Close()
	ep := NewUnixEndpoint(p.Master())

	cases := []Winsize{
		{Cols: 100, Rows: 40},
		{Cols: 132, Rows: 50},
		{Cols: 1, Rows: 1},
	}
	for _, want := range cases {
		if err := ep.Resize(want); err != nil {
			t.Fatalf("Resize(%v): %v", want, err)
		}
		got, err := pty.GetWinsize(p.Master())
		if err != nil {
			t.Fatalf("GetWinsize: %v", err)
		}
		if got.Cols != want.Cols || got.Rows != want.Rows {
			t.Fatalf("after Resize(%v): GetWinsize=%dx%d (cols x rows), want %dx%d",
				want, got.Cols, got.Rows, want.Cols, want.Rows)
		}
	}
}

// TestEndpointErrorsAfterClose verifies lifecycle: Grant/Resize fail after Close.
func TestEndpointErrorsAfterClose(t *testing.T) {
	p, err := pty.Open(80, 24)
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	ep := NewUnixEndpoint(p.Master())
	if err := ep.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := ep.Grant(); err == nil {
		t.Fatal("Grant after Close should error")
	}
	if err := ep.Resize(Winsize{Cols: 80, Rows: 24}); err == nil {
		t.Fatal("Resize after Close should error")
	}
	if err := ep.Close(); err != nil {
		t.Fatalf("second Close should be nil, got %v", err)
	}
}
