package internal

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type Status string

const (
	StatusActive  Status = "active"
	StatusStopped Status = "stopped"
	StatusCrashed Status = "crashed"
)

const (
	ctrlU      = "\x15"
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"

	enterKey              = "\r"
	csiUShiftEnter        = "\x1b[13;2u"
	modifyOtherShiftEnter = "\x1b[27;2;13~"

	maxInputBuf = 4096
	// inputQueueCap history slots + 1 active slot at index queueLen.
	inputQueueCap = 2
	// carrySize must cover the longest escape sequence we detect (7 bytes for CSI-u shift-enter).
	carrySize = 8

	// pasteSettle is the pause between clear, paste, and submit so the TUI
	// applies each step in order. Without it the submit key races ahead of the
	// bracketed paste and lands on stale human input.
	pasteSettle = 40 * time.Millisecond

	// drainReadTimeout bounds each idle-drain read so the drainer periodically
	// re-checks pause/close state even when the child is silent.
	drainReadTimeout = 200 * time.Millisecond
)

// submitRetryDelays are the delays before each bare resubmit of an already-pasted
// prompt — used to push a prompt through when the TUI swallowed the first submit.
var submitRetryDelays = []time.Duration{100 * time.Millisecond}

type PTYCreateParams struct {
	AgentID   string   `json:"agent_id"`
	SessionID string   `json:"session_id"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	SubmitKey string   `json:"submit_key"`
	Env       []string `json:"env"`
	Dir       string   `json:"dir"` // working directory for the spawned process
}

type PTYTerminalInfo struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	Status    Status `json:"status"`
}

type PTYTerminal struct {
	PTYTerminalInfo
	master *os.File
	cmd    *exec.Cmd
	proc   *os.Process // set for adopted processes (cmd == nil)
	// submitKey is set once at Create/Adopt and never modified; safe to read
	// under execMu without holding t.mu.
	submitKey string
	mu        sync.Mutex

	// execMu serialises concurrent ExecInSession calls so their writes on the
	// PTY master cannot interleave.
	execMu sync.Mutex

	// humanMu guards the input tracking state below.
	humanMu sync.Mutex
	// inputQueue[0..queueLen-1] = committed history (oldest→newest).
	// inputQueue[queueLen]      = active top slot; trackHumanInput writes here directly.
	// On enter: active slot becomes history (queueLen++, drop oldest if full), new active = nil.
	// Bot reads inputQueue[queueLen] (active top) via readLastInput — no pop.
	inputQueue       [inputQueueCap + 1][]byte
	queueLen         int
	inBracketedPaste bool
	// carry holds the tail of the last relay chunk to detect escape sequences
	// that span two consecutive reads.
	carry  [carrySize]byte
	carryN int

	// --- idle output drain (created sessions only) ---
	// While no fd-passing client holds the master, drainLoop reads and discards
	// output so the kernel PTY buffer never fills and the child never blocks on
	// write. It is paused while a client is attached (one reader at a time) and
	// resumed on detach. Adopted sessions leave hasDrain=false (their master fd
	// is externally owned), so all drain calls are no-ops for them.
	drainMu     sync.Mutex
	drainCond   *sync.Cond
	hasDrain    bool
	drainActive bool
	drainClosed bool
}

func (t *PTYTerminal) write(p []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.master == nil {
		return errors.New("no pty master: terminal was adopted without a writable fd")
	}
	_, err := t.master.Write(p)
	return err
}

func (t *PTYTerminal) kill() error {
	if t.cmd != nil {
		return t.cmd.Process.Kill()
	}
	if t.proc != nil {
		return t.proc.Kill()
	}
	return nil
}

// closeMaster releases the PTY master fd and nils it so any later write fails
// cleanly ("no pty master") instead of operating on a dead descriptor. Called
// on every session-exit path so the fd is freed immediately rather than at GC.
// Idempotent and safe to call more than once.
func (t *PTYTerminal) closeMaster() {
	t.stopDrain() // stop the drainer before the fd disappears
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.master != nil {
		_ = t.master.SetReadDeadline(time.Now()) // interrupt any in-progress drain read
		_ = t.master.Close()
		t.master = nil
	}
}

// startDrain launches the idle output-drain goroutine for a created session.
// The session begins detached, so draining starts active. Call once per
// terminal, after master/cmd are set.
func (t *PTYTerminal) startDrain() {
	t.drainMu.Lock()
	if t.hasDrain {
		t.drainMu.Unlock()
		return
	}
	t.drainCond = sync.NewCond(&t.drainMu)
	t.hasDrain = true
	t.drainActive = true
	t.drainMu.Unlock()
	go t.drainLoop()
}

// drainLoop reads the master and discards the bytes, keeping the kernel PTY
// output buffer empty so the child never blocks on write. It parks while paused
// (a client is attached) and exits when the master is closed. This is the
// "0-buffer" drain: nothing is retained — a late client repaints from the child
// (see repaint), it does not replay history.
func (t *PTYTerminal) drainLoop() {
	buf := make([]byte, 4096)
	for {
		t.drainMu.Lock()
		for !t.drainActive && !t.drainClosed {
			t.drainCond.Wait()
		}
		closed := t.drainClosed
		t.drainMu.Unlock()
		if closed {
			return
		}

		t.mu.Lock()
		m := t.master
		t.mu.Unlock()
		if m == nil {
			return
		}

		// Bounded deadline so a paused/closed transition is noticed even when
		// the child is silent, and so a paused drainer instant-timeouts instead
		// of stealing the attached client's bytes.
		_ = m.SetReadDeadline(time.Now().Add(drainReadTimeout))
		if _, err := m.Read(buf); err != nil {
			if os.IsTimeout(err) {
				continue
			}
			return // EOF / EIO / closed — master is gone
		}
		// bytes discarded; keep draining
	}
}

// pauseDrain stops the drainer before a client takes over reading the master,
// enforcing one-reader-at-a-time. No-op for adopted sessions.
func (t *PTYTerminal) pauseDrain() {
	t.drainMu.Lock()
	if !t.hasDrain {
		t.drainMu.Unlock()
		return
	}
	t.drainActive = false
	cond := t.drainCond
	t.drainMu.Unlock()
	// Keep a past deadline so any in-progress or subsequent drain read returns
	// immediately and parks rather than consuming the client's output.
	t.mu.Lock()
	if t.master != nil {
		_ = t.master.SetReadDeadline(time.Now())
	}
	t.mu.Unlock()
	cond.Signal()
}

// resumeDrain restarts draining after a client detaches. No-op for adopted
// sessions.
func (t *PTYTerminal) resumeDrain() {
	t.drainMu.Lock()
	if !t.hasDrain {
		t.drainMu.Unlock()
		return
	}
	t.drainActive = true
	cond := t.drainCond
	t.drainMu.Unlock()
	t.mu.Lock()
	if t.master != nil {
		_ = t.master.SetReadDeadline(time.Time{}) // clear deadline
	}
	t.mu.Unlock()
	cond.Signal()
}

// stopDrain permanently stops the drainer (called from closeMaster). No-op for
// adopted sessions or if already stopped.
func (t *PTYTerminal) stopDrain() {
	t.drainMu.Lock()
	if !t.hasDrain {
		t.drainMu.Unlock()
		return
	}
	t.drainClosed = true
	cond := t.drainCond
	t.drainMu.Unlock()
	cond.Signal()
}

// repaint nudges the PTY window size to force the child to redraw its full
// current screen for a freshly-attached client. Full-screen TUIs (claude,
// codex) repaint on SIGWINCH; line-oriented programs are unaffected. No
// retained buffer is needed — the child is the source of truth for the screen.
func (t *PTYTerminal) repaint() {
	t.mu.Lock()
	m := t.master
	t.mu.Unlock()
	if m == nil {
		return
	}
	fd := int(m.Fd())
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil || ws.Row <= 1 || ws.Col == 0 {
		return // no usable size yet; nothing to force a redraw against
	}
	nudged := *ws
	nudged.Row = ws.Row - 1
	_ = unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &nudged)
	_ = unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, ws) // restore → SIGWINCH → full redraw
}

// readLastInput returns a full copy of the active queue top (inputQueue[queueLen]).
// This is what the human is currently typing — never pops, never clears.
func (t *PTYTerminal) readLastInput() []byte {
	t.humanMu.Lock()
	defer t.humanMu.Unlock()
	active := t.inputQueue[t.queueLen]
	if len(active) == 0 {
		return nil
	}
	return append([]byte(nil), active...)
}

// execPrompt sends a bot prompt while preserving the human's partial input.
// Each step is a separate PTY write so the TUI applies them in order — a single
// concatenated write lets the submit key race ahead of the paste and submit
// stale human input instead of the prompt.
//
//  1. ctrlU                          clear the human's partial line
//  2. paste(prompt)                  bracketed-paste the prompt (no submit yet)
//  3. submitKey [+ retries]          submit; bare resubmit if the TUI swallowed it
//  4. human input (once, no submit)  restore what the human was typing
//
// execMu is held across the whole sequence (including the settle/retry sleeps)
// so concurrent exec calls cannot interleave their writes.
func (t *PTYTerminal) execPrompt(prompt string) error {
	// Snapshot the human's partial input up front; restored at the end. Never pops.
	human := t.readLastInput()

	t.execMu.Lock()
	defer t.execMu.Unlock()

	// 1. Clear the human's partial line, then let the TUI apply it before we
	//    paste — otherwise the clear races behind the paste/submit.
	if err := t.write([]byte(ctrlU)); err != nil {
		return err
	}
	time.Sleep(pasteSettle)

	// 2. Bracketed-paste the prompt as its own write and let it settle. The
	//    submit must not share this write or it races the paste.
	if err := t.write([]byte(pasteStart + prompt + pasteEnd)); err != nil {
		return err
	}
	time.Sleep(pasteSettle)

	// 3. Submit. Retries send the bare submit key only (no ctrlU) so a prompt
	//    still sitting in the buffer is pushed through rather than wiped.
	submit := submitSeq(t.submitKey)
	if err := t.write(submit); err != nil {
		return err
	}
	for i, delay := range submitRetryDelays {
		time.Sleep(delay)
		werr := t.write(submit)
		ptylog.Debug("ptydaemon: submit-key retry", "attempt", i+2, "session_id", t.SessionID, "submit_key", t.submitKey, "err", werr)
		if werr != nil {
			return werr
		}
	}

	// 4. Restore the human's partial input (no submit) so they see it again.
	if len(human) > 0 {
		if err := t.write(human); err != nil {
			return err
		}
	}
	return nil
}

// retrySubmitKey sends only the bare submit key at fixed intervals.
// Used by the pipe/connector path (handleExec) where the connector already
// pre-formats the full payload — no ctrlU, no reinject.
func (t *PTYTerminal) retrySubmitKey() error {
	submitKey := submitSeq(t.submitKey)
	retryDelays := []time.Duration{100 * time.Millisecond}
	for i, delay := range retryDelays {
		attempt := i + 2
		time.Sleep(delay)
		t.execMu.Lock()
		werr := t.write(submitKey)
		t.execMu.Unlock()
		ptylog.Debug("ptydaemon: submit-key retry", "attempt", attempt, "session_id", t.SessionID, "submit_key", t.submitKey, "err", werr)
		if werr != nil {
			return werr
		}
	}
	return nil
}

func (t *PTYTerminal) setStatus(s Status) {
	t.mu.Lock()
	t.Status = s
	t.mu.Unlock()
}

// trackHumanInput is called by the stdin relay before forwarding each chunk
// to the PTY master. It maintains currentInput — the always-live active buffer
// the bot reads to reinject human input after sending a prompt.
func (t *PTYTerminal) trackHumanInput(chunk []byte) {
	t.humanMu.Lock()
	defer t.humanMu.Unlock()

	// Prepend carry bytes to detect sequences split across chunk boundaries.
	var buf []byte
	if t.carryN > 0 {
		buf = make([]byte, t.carryN+len(chunk))
		copy(buf, t.carry[:t.carryN])
		copy(buf[t.carryN:], chunk)
		t.carryN = 0
	} else {
		buf = chunk
	}

	// Update bracketed-paste state so we don't mistake embedded \r for submit.
	if bytes.Contains(buf, []byte(pasteStart)) {
		t.inBracketedPaste = true
	}
	if bytes.Contains(buf, []byte(pasteEnd)) {
		t.inBracketedPaste = false
	}

	if !t.inBracketedPaste && isSubmitOrClear(buf) {
		// Commit active slot (inputQueue[queueLen]) to history by advancing queueLen.
		// Drop oldest history entry if at capacity.
		if len(t.inputQueue[t.queueLen]) > 0 {
			if t.queueLen == inputQueueCap {
				copy(t.inputQueue[:], t.inputQueue[1:])
				t.inputQueue[inputQueueCap] = nil
			} else {
				t.queueLen++
			}
		}
		// New active slot is now inputQueue[queueLen] — start fresh.
		t.inputQueue[t.queueLen] = nil
		return
	}

	// Save the tail as carry for the next read.
	n := len(buf)
	if n > carrySize {
		n = carrySize
	}
	copy(t.carry[:], buf[len(buf)-n:])
	t.carryN = n

	// Write directly into the active queue top slot.
	t.inputQueue[t.queueLen] = append(t.inputQueue[t.queueLen], chunk...)
	if len(t.inputQueue[t.queueLen]) > maxInputBuf {
		t.inputQueue[t.queueLen] = t.inputQueue[t.queueLen][len(t.inputQueue[t.queueLen])-maxInputBuf:]
	}
}

// isSubmitOrClear returns true when b contains a line-submit or clear-line
// sequence outside of a bracketed paste. Call only when inBracketedPaste is false.
func isSubmitOrClear(b []byte) bool {
	s := string(b)
	return strings.ContainsAny(s, "\r\x15") ||
		strings.Contains(s, csiUShiftEnter) ||
		strings.Contains(s, modifyOtherShiftEnter)
}

func submitSeq(name string) []byte {
	switch strings.ToLower(name) {
	case "shift-enter", "shift_enter", "csi-u-shift-enter":
		return []byte(csiUShiftEnter)
	case "modify-other-keys-shift-enter", "modify_other_keys_shift_enter":
		return []byte(modifyOtherShiftEnter)
	default:
		return []byte(enterKey)
	}
}
