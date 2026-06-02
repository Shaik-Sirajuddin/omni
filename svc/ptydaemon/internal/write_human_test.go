package internal

import (
	"io"
	"os"
	"testing"
	"time"
)

// TestWriteHumanSerialisesAfterExec proves a human keystroke arriving while an
// exec sequence holds execMu is deferred (not dropped, not interleaved): it
// lands on the master only after the exec write, preserving ordering.
func TestWriteHumanSerialisesAfterExec(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	term := &PTYTerminal{master: w}

	// Simulate an in-flight execPrompt: it holds execMu across its whole sequence.
	term.execMu.Lock()

	humanDone := make(chan error, 1)
	go func() { humanDone <- term.writeHuman([]byte("HUMAN")) }()

	// Let the relay goroutine reach (and block on) execMu before exec writes.
	time.Sleep(20 * time.Millisecond)

	if err := term.write([]byte("EXEC")); err != nil {
		t.Fatalf("exec write: %v", err)
	}
	term.execMu.Unlock() // exec done → deferred human input may now proceed

	if herr := <-humanDone; herr != nil {
		t.Fatalf("writeHuman: %v", herr)
	}

	buf := make([]byte, len("EXECHUMAN"))
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "EXECHUMAN" {
		t.Fatalf("expected exec before human (EXECHUMAN), got %q", buf)
	}
}
