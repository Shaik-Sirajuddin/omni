//go:build windows

package handoff

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// conpty_windows_test.go: REAL ConPTY/named-pipe end-to-end tests. They run only on
// a Windows runner (none in CI yet) but are complete, not skips: they spawn a child,
// relay its output over the named pipe, and assert the marker bytes, plus resize,
// steal=DisconnectNamedPipe, PID-verify reject, and Job-Object kill-on-close.

// openRelayClient opens the relay pipe by name as the client would (CreateFile),
// returning a handle the test reads from.
func openRelayClient(t *testing.T, name string) windows.Handle {
	t.Helper()
	name16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	// Retry briefly: the server may not have reached ConnectNamedPipe yet.
	deadline := time.Now().Add(3 * time.Second)
	for {
		h, err := windows.CreateFile(
			name16,
			windows.GENERIC_READ,
			0, nil,
			windows.OPEN_EXISTING,
			0, 0,
		)
		if err == nil {
			return h
		}
		if time.Now().After(deadline) {
			t.Fatalf("CreateFile(relay pipe %q): %v", name, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readAll(t *testing.T, h windows.Handle, want []byte, d time.Duration) []byte {
	t.Helper()
	// ReadFile on a synchronous handle blocks and cannot be interrupted, so the
	// deadline must be enforced OUT of band: read in a goroutine, select on a timer.
	// On timeout we return what we have (caller asserts Contains(want) and fails)
	// instead of hanging to the test binary's global -timeout.
	ch := make(chan []byte, 1)
	go func() {
		var got []byte
		buf := make([]byte, 4096)
		for {
			var done uint32
			err := windows.ReadFile(h, buf, &done, nil)
			if done > 0 {
				got = append(got, buf[:done]...)
				if bytes.Contains(got, want) {
					ch <- got
					return
				}
			}
			if err != nil {
				ch <- got
				return
			}
		}
	}()
	select {
	case got := <-ch:
		return got
	case <-time.After(d):
		return nil
	}
}

// TestConPTYEmitsOutput is a DIRECT diagnostic: it reads the ConPTY output source
// itself (no relay, no Grant) and asserts the child's marker appears. If this fails
// but the relay test also fails, the problem is ConPTY setup, not the relay.
func TestConPTYEmitsOutput(t *testing.T) {
	probe := "PROBE-CONPTY-7f3a9c"
	cp, err := NewWinConPTY(Winsize{Cols: 80, Rows: 24}, `cmd.exe /k echo `+probe)
	if err != nil {
		t.Fatalf("NewWinConPTY: %v", err)
	}
	defer func() { _ = cp.close() }()

	src := cp.output()
	got := make(chan []byte, 1)
	go func() {
		var acc []byte
		buf := make([]byte, 4096)
		for {
			n, rerr := src.Read(buf)
			if n > 0 {
				acc = append(acc, buf[:n]...)
				if bytes.Contains(acc, []byte(probe)) {
					got <- acc
					return
				}
			}
			if rerr != nil {
				got <- acc
				return
			}
		}
	}()
	select {
	case b := <-got:
		if !bytes.Contains(b, []byte(probe)) {
			t.Fatalf("ConPTY emitted %d bytes but no probe marker: %q", len(b), string(b))
		}
		t.Logf("ConPTY emitted %d bytes including the probe marker", len(b))
	case <-time.After(8 * time.Second):
		t.Fatal("ConPTY emitted NOTHING in 8s — conhost/child not producing output")
	}
}

// TestConPTYRelayEndToEnd spawns a keep-alive child that echoes MARKER, attaches a
// client, and asserts the client reads the marker bytes relayed over the named pipe.
func TestConPTYRelayEndToEnd(t *testing.T) {
	marker := "HANDOFF-CONPTY-MARKER-7f3a9c"
	cp, err := NewWinConPTY(Winsize{Cols: 80, Rows: 24}, `cmd.exe /k echo `+marker)
	if err != nil {
		t.Fatalf("NewWinConPTY: %v", err)
	}
	ep := newWinEndpoint(cp)
	defer ep.Close()

	// The relay PID-verifies against THIS test process (we are the client).
	ep.SetClientPID(uint32(os.Getpid()))

	// Grant blocks on ConnectNamedPipe until the client opens the pipe. The endpoint
	// publishes the random pipe name BEFORE the blocking connect (pendingPipeName),
	// so we learn it and CreateFile the client end to unblock Grant.
	grantErr := make(chan error, 1)
	go func() { _, gerr := ep.Grant(); grantErr <- gerr }()

	name := waitPipeName(t, ep)
	h := openRelayClient(t, name)
	defer windows.CloseHandle(h)

	select {
	case gerr := <-grantErr:
		if gerr != nil {
			t.Fatalf("Grant: %v", gerr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Grant did not complete after client connect")
	}

	// Drive the pump so it relays (Grant already attached the sink).
	go func() { _ = ep.Drain(context.Background()) }()

	got := readAll(t, h, []byte(marker), 5*time.Second)
	if !bytes.Contains(got, []byte(marker)) {
		t.Fatalf("relay client did not receive marker.\n want: %q\n  got: %q", marker, string(got))
	}
}

// TestConPTYResize spawns a long-running child and asserts ResizePseudoConsole does
// not error across several sizes.
func TestConPTYResize(t *testing.T) {
	cp, err := NewWinConPTY(Winsize{Cols: 80, Rows: 24}, `cmd.exe /k`)
	if err != nil {
		t.Fatalf("NewWinConPTY: %v", err)
	}
	ep := newWinEndpoint(cp)
	defer ep.Close()
	for _, sz := range []Winsize{{Cols: 100, Rows: 40}, {Cols: 132, Rows: 50}, {Cols: 40, Rows: 10}} {
		if err := ep.Resize(sz); err != nil {
			t.Fatalf("Resize(%v): %v", sz, err)
		}
	}
}

// TestConPTYStealDisconnects asserts Revoke (steal) = DisconnectNamedPipe drops the
// client's reader: a subsequent ReadFile returns an error or zero bytes.
func TestConPTYStealDisconnects(t *testing.T) {
	cp, err := NewWinConPTY(Winsize{Cols: 80, Rows: 24}, `cmd.exe /k`)
	if err != nil {
		t.Fatalf("NewWinConPTY: %v", err)
	}
	ep := newWinEndpoint(cp)
	defer ep.Close()
	ep.SetClientPID(uint32(os.Getpid()))

	// Wait for Grant to FINISH attaching the sink before stealing — otherwise the
	// Revoke races ahead of attach (a no-op steal) and the client then reads a
	// still-connected pipe forever.
	grantErr := make(chan error, 1)
	go func() { _, gerr := ep.Grant(); grantErr <- gerr }()
	name := waitPipeName(t, ep)
	h := openRelayClient(t, name)
	defer windows.CloseHandle(h)
	select {
	case gerr := <-grantErr:
		if gerr != nil {
			t.Fatalf("Grant: %v", gerr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Grant did not complete after client connect")
	}

	// Steal: disconnect the relay.
	_ = ep.Revoke(context.Background())

	// The client read must now fail (pipe disconnected) or return zero bytes.
	// Bound it so a regression can't hang the suite.
	type readRes struct {
		n   uint32
		err error
	}
	res := make(chan readRes, 1)
	go func() {
		buf := make([]byte, 64)
		var done uint32
		rerr := windows.ReadFile(h, buf, &done, nil)
		res <- readRes{done, rerr}
	}()
	select {
	case r := <-res:
		if r.err == nil && r.n > 0 {
			t.Fatalf("client still read %d bytes after steal/disconnect", r.n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client read did not return after steal/disconnect (pipe still connected?)")
	}
}

// TestConPTYPIDVerifyRejectsWrongPID asserts a client whose PID != expected is
// disconnected (H4): Grant returns *ErrPIDMismatch.
func TestConPTYPIDVerifyRejectsWrongPID(t *testing.T) {
	cp, err := NewWinConPTY(Winsize{Cols: 80, Rows: 24}, `cmd.exe /k`)
	if err != nil {
		t.Fatalf("NewWinConPTY: %v", err)
	}
	ep := newWinEndpoint(cp)
	defer ep.Close()
	ep.SetClientPID(0xFFFFFFFE) // a PID our test process will not have

	errCh := make(chan error, 1)
	go func() { _, gerr := ep.Grant(); errCh <- gerr }()
	name := waitPipeName(t, ep)
	h := openRelayClient(t, name)
	defer windows.CloseHandle(h)

	select {
	case gerr := <-errCh:
		if gerr == nil || !strings.Contains(gerr.Error(), "PID verify") {
			t.Fatalf("Grant must reject wrong PID with *ErrPIDMismatch, got %v", gerr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Grant did not reject the wrong-PID client")
	}
}

// TestConPTYJobObjectKillsTree asserts Close() kills the child tree via the Job
// Object: after Close the child process handle is signalled (exited).
func TestConPTYJobObjectKillsTree(t *testing.T) {
	cp, err := NewWinConPTY(Winsize{Cols: 80, Rows: 24}, `cmd.exe /k`)
	if err != nil {
		t.Fatalf("NewWinConPTY: %v", err)
	}
	// Close() closes cp.proc, so the test must hold its OWN handle to wait on after
	// Close — otherwise WaitForSingleObject runs on a closed handle and WAIT_FAILEDs.
	cur := windows.CurrentProcess()
	var proc windows.Handle
	if err := windows.DuplicateHandle(cur, cp.proc, cur, &proc, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatalf("DuplicateHandle(proc): %v", err)
	}
	defer windows.CloseHandle(proc)
	ep := newWinEndpoint(cp)

	// Child is alive: waiting on it briefly times out (still running).
	ev, _ := windows.WaitForSingleObject(proc, 200)
	if ev == windows.WAIT_OBJECT_0 {
		t.Fatal("child already exited before Close")
	}

	if err := ep.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close the Job Object kill must have terminated the child.
	ev, _ = windows.WaitForSingleObject(proc, 3000)
	if ev != windows.WAIT_OBJECT_0 {
		t.Fatalf("child not killed by Job Object close (WaitForSingleObject=%#x)", ev)
	}
}

// waitPipeName retrieves the relay pipe name the endpoint is currently serving.
func waitPipeName(t *testing.T, ep *winEndpoint) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n := ep.pendingPipeName(); n != "" {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("relay pipe name was not published in time")
	return ""
}
