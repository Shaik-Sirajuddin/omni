package internal

import (
	"io"
	"os"
	"testing"
	"time"
)

// TestWriteUserSerialisesAfterExec proves a user keystroke arriving while an
// exec sequence holds execMu is deferred (not dropped, not interleaved): it
// lands on the master only after the exec write, preserving ordering.
func TestWriteUserSerialisesAfterExec(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	term := &PTYTerminal{master: w}

	// Simulate an in-flight execPrompt: it holds execMu across its whole sequence.
	term.execMu.Lock()

	userDone := make(chan error, 1)
	go func() { userDone <- term.writeUser([]byte("USER")) }()

	// Let the relay goroutine reach (and block on) execMu before exec writes.
	time.Sleep(20 * time.Millisecond)

	if err := term.write([]byte("EXEC")); err != nil {
		t.Fatalf("exec write: %v", err)
	}
	term.execMu.Unlock() // exec done → deferred user input may now proceed

	if herr := <-userDone; herr != nil {
		t.Fatalf("writeUser: %v", herr)
	}

	buf := make([]byte, len("EXECUSER"))
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "EXECUSER" {
		t.Fatalf("expected exec before user (EXECUSER), got %q", buf)
	}
}
