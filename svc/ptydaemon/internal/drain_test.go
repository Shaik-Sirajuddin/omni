package internal

import (
	"os/exec"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestDrainPreventsStall is the core guarantee: a child that emits far more
// output than the kernel PTY buffer holds must run to completion with no client
// attached, because the idle drainer keeps the buffer empty. Without draining
// the child blocks on write once the buffer fills and Wait() never returns.
func TestDrainPreventsStall(t *testing.T) {
	// ~640 KB of output — well past any kernel PTY buffer.
	cmd := exec.Command("sh", "-c", "i=0; while [ $i -lt 20000 ]; do echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; i=$((i+1)); done")
	m, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	term := &PTYTerminal{master: m, cmd: cmd}
	term.startDrain()
	defer term.closeMaster()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child exited with error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child stalled — idle drain did not keep the PTY buffer empty")
	}
}

// TestDrainPauseResume exercises rapid attach/detach transitions to catch
// deadlocks or races in the pause/resume/stop paths (run under -race).
func TestDrainPauseResume(t *testing.T) {
	cmd := exec.Command("sh", "-c", "while true; do echo x; done")
	m, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	term := &PTYTerminal{master: m, cmd: cmd}
	term.startDrain()

	for i := 0; i < 20; i++ {
		term.pauseDrain()
		time.Sleep(time.Millisecond)
		term.resumeDrain()
		time.Sleep(time.Millisecond)
	}

	term.closeMaster() // stops the drainer and hangs up the child
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

// TestDrainAdoptedNoop verifies all drain controls are safe no-ops when the
// drainer was never started (adopted sessions leave hasDrain=false), in
// particular that they never dereference the nil drainCond.
func TestDrainAdoptedNoop(t *testing.T) {
	term := &PTYTerminal{} // no startDrain
	term.pauseDrain()
	term.resumeDrain()
	term.stopDrain()
	term.closeMaster() // also a no-op: no master, no drainer
}
