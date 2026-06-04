package internal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// dbgBytes renders a byte slice for debug logs: a Go-quoted form (control and
// escape bytes visible as \x1b, \r, …) plus a raw hex dump. Use to inspect what
// actually lands on the PTY master so a malformed/duplicated chunk is obvious.
func dbgBytes(b []byte) string {
	return fmt.Sprintf("%q hex=% x", b, b)
}

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
	// carrySize must cover the longest escape sequence we detect (7 bytes for CSI-u shift-enter).
	carrySize = 8

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

	// userMu guards the input tracking state below.
	userMu sync.Mutex
	// activeInput is the user's current UNSUBMITTED line — the live mirror of the
	// child TUI's input buffer. trackUserInput appends filtered text and resets it
	// on a submit/clear; readLastInput returns a copy for exec reinjection. No
	// history is kept (only the unsubmitted line is ever read), so there is no
	// slot rotation that could drop the line across a commit boundary.
	activeInput      []byte
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
	drainParked bool // true while the drainer is parked (provably not reading)
	drainClosed bool
}

func (t *PTYTerminal) write(p []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.writeLocked(p)
}

// writeLocked writes to the master assuming the caller already holds t.mu.
// execPrompt uses this to hold t.mu across its ENTIRE clear→paste→submit→reinject
// sequence, so no other writer (writeUser/writePipe) — and crucially no closer
// (closeMaster takes only t.mu, not execMu) — can touch or nil the master between
// the sub-writes. The whole prompt lands as one uninterrupted unit on the fd.
func (t *PTYTerminal) writeLocked(p []byte) error {
	if t.master == nil {
		return errors.New("no pty master: terminal was adopted without a writable fd")
	}
	_, err := t.master.Write(p)
	return err
}

// writePipe writes connector-supplied bytes as one atomic logical unit. It takes
// execMu — the same sequence lock execPrompt/writeUser hold — so a Pipe write can
// never interleave between execPrompt's sub-writes (execPrompt releases the
// low-level t.mu between writes during its retry sleeps, but keeps execMu).
func (t *PTYTerminal) writePipe(data []byte) error {
	t.execMu.Lock()
	defer t.execMu.Unlock()
	return t.write(data)
}

// writeUser forwards a user-typed chunk to the master under execMu — the same
// lock execPrompt holds across its whole clear→paste→submit→reinject sequence.
// This keeps a keystroke from interleaving inside an in-flight exec (which would
// corrupt the prompt line). The keystrokes are deferred while exec runs, not
// dropped: they flush once the lock is released and surface after the prompt.
func (t *PTYTerminal) writeUser(chunk []byte) error {
	t.execMu.Lock()
	defer t.execMu.Unlock()
	return t.write(chunk)
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
			// Announce parked (not reading) and wake any pauseDrain waiter so it
			// can return only once the client is provably the sole reader.
			t.drainParked = true
			t.drainCond.Broadcast()
			t.drainCond.Wait()
		}
		t.drainParked = false
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
		// the child is silent.
		_ = m.SetReadDeadline(time.Now().Add(drainReadTimeout))
		_, err := m.Read(buf)
		if err != nil {
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
	t.drainMu.Unlock()
	ptylog.Debug("ptydaemon: pauseDrain begin (attaching: handing master to client)",
		"session_id", t.SessionID)
	waitStart := time.Now()

	// Interrupt any in-progress read so the drainer returns promptly and parks
	// instead of overwriting this deadline with a fresh future one and reading
	// concurrently with the attaching client.
	t.mu.Lock()
	if t.master != nil {
		_ = t.master.SetReadDeadline(time.Now())
	}
	t.mu.Unlock()

	// Block until the drainer has actually parked (not reading), so the client
	// is guaranteed to be the sole reader of the master on return. Closing the
	// terminal mid-pause also releases us.
	t.drainMu.Lock()
	for !t.drainParked && !t.drainClosed {
		t.drainCond.Wait()
	}
	closed := t.drainClosed
	t.drainMu.Unlock()
	ptylog.Debug("ptydaemon: pauseDrain done (client is now sole reader)",
		"session_id", t.SessionID, "closed", closed,
		"wait_ms", time.Since(waitStart).Milliseconds())
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
	t.drainMu.Unlock()
	ptylog.Debug("ptydaemon: resumeDrain (detached: daemon is sole reader again)",
		"session_id", t.SessionID)
	t.mu.Lock()
	if t.master != nil {
		_ = t.master.SetReadDeadline(time.Time{}) // clear deadline
	}
	t.mu.Unlock()
	// Broadcast: both the drainLoop (waiting for active) and any pauseDrain
	// (waiting for parked) share this cond, so wake all to re-check predicates.
	t.drainMu.Lock()
	t.drainCond.Broadcast()
	t.drainMu.Unlock()
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
	t.drainCond.Broadcast() // wake the drainLoop and any pauseDrain waiter
	t.drainMu.Unlock()
}

// readLastInput returns a copy of the user's current unsubmitted line
// (activeInput). This is what the user is currently typing — never pops, never
// clears.
func (t *PTYTerminal) readLastInput() []byte {
	t.userMu.Lock()
	defer t.userMu.Unlock()
	if len(t.activeInput) == 0 {
		return nil
	}
	return append([]byte(nil), t.activeInput...)
}

// execPrompt sends a bot prompt while preserving the user's partial input.
//
// The clear+paste+submit is a SINGLE atomic write: a PTY is an ordered byte
// stream, so the child reads ctrlU → \x1b[200~ → prompt → \x1b[201~ → submit in
// exactly that order and parses them sequentially (201~ closes the paste before
// the submit byte, so the submit is a real keypress, not pasted text). One write
// means there is no inter-write gap for the child's own render output to
// interleave with the half-injected paste — the split-with-sleeps version let
// the paste land at a stale cursor / in the output area.
//
//  1. ctrlU + \x1b[200~ prompt \x1b[201~ + submit   one atomic write
//  2. submit [+ retries]                            bare resubmit if the TUI swallowed it
//  3. user input (once, no submit)                  restore what the user was typing
//
// paste mode (DECSET 2004) is assumed active — every supported TUI enables it at
// startup. Connectors must send the RAW prompt (no pre-wrap) or it double-frames;
// newline / multi-line intent is the prompt sender's responsibility.
//
// execMu is held across the whole sequence (including the retry sleeps) so
// concurrent exec calls cannot interleave their writes. t.mu is also held across
// the whole sequence so no other writer (writeUser/writePipe) and no closer
// (closeMaster, which takes only t.mu) can touch the master between our
// sub-writes — the prompt lands as one uninterrupted unit on the fd.
func (t *PTYTerminal) execPrompt(prompt string) error {
	// Snapshot the user's partial input up front; restored at the end. Never pops.
	user := t.readLastInput()

	t.execMu.Lock()
	defer t.execMu.Unlock()

	// Acquire the master-fd lock once and hold it for the entire sequence (payload,
	// retry sleeps, reinject). Every sub-write below uses writeLocked, which assumes
	// t.mu is held — calling t.write here would self-deadlock.
	t.mu.Lock()
	defer t.mu.Unlock()

	// The prompt must arrive raw here (no caller-side bracketed-paste/submit
	// framing): this method is the single owner of framing — see handleExec.
	ptylog.Debug("ptydaemon: execPrompt begin", "session_id", t.SessionID,
		"submit_key", t.submitKey, "prompt_len", len(prompt),
		"prompt_prewrapped", strings.Contains(prompt, pasteStart), "user_carry", len(user))

	// Defang the ONLY bytes bracketed-paste wrapping cannot neutralise: an
	// embedded 201~ would terminate our paste early and let the prompt tail run as
	// keystrokes (escaping the input box); an embedded 200~ would open a nested
	// paste. We neutralise them as literal text (drop the ESC, keep "[201~"), not
	// delete. Every other control/escape byte already stays literal inside the
	// wrap, so nothing else is touched. execPrompt is the sole owner of framing.
	if clean := defangPasteMarkers(prompt); clean != prompt {
		ptylog.Debug("ptydaemon: execPrompt defanged paste markers", "session_id", t.SessionID,
			"before_len", len(prompt), "after_len", len(clean))
		prompt = clean
	}

	// 1. One atomic write: close any dangling paste, clear line, bracketed-paste
	//    the prompt, submit. The child reads this in order; the leading 201~
	//    defensively closes a paste a prior sequence may have left open (else our
	//    prompt would be swallowed as paste content) and is a harmless no-op
	//    otherwise; the trailing 201~ closes our paste before the submit byte.
	submit := submitSeq(t.submitKey)
	payload := append([]byte(pasteEnd+ctrlU+pasteStart+prompt+pasteEnd), submit...)
	if err := t.writeLocked(payload); err != nil {
		return err
	}

	// 2. Submit retries: bare submit key only (no ctrlU/paste) so a prompt still
	//    sitting in the buffer is pushed through if the TUI swallowed the first
	//    submit, rather than wiped.
	for i, delay := range submitRetryDelays {
		time.Sleep(delay)
		werr := t.writeLocked(submit)
		ptylog.Debug("ptydaemon: submit-key retry", "attempt", i+2, "session_id", t.SessionID, "submit_key", t.submitKey, "err", werr)
		if werr != nil {
			return werr
		}
	}
     
	// delay to allow the application to process submit 
	time.Sleep(50 * time.Millisecond)
	
	// 3. Restore the user's partial input (no submit) so they see it again.
	if len(user) > 0 {
		ptylog.Debug("ptydaemon: execPrompt reinject user", "session_id", t.SessionID,
			"user", dbgBytes(user), "len", len(user))
		if err := t.writeLocked(user); err != nil {
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

// trackUserInput is called by the stdin relay before forwarding each chunk
// to the PTY master. It maintains currentInput — the always-live active buffer
// the bot reads to reinject user input after sending a prompt.
func (t *PTYTerminal) trackUserInput(chunk []byte) {
	t.userMu.Lock()
	defer t.userMu.Unlock()

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
		// Submit (\r/shift-enter) or clear (\x15): the unsubmitted line is gone
		// from the child's input buffer, so drop our mirror of it. Keep the slice
		// backing array for reuse.
		t.activeInput = t.activeInput[:0]
		return
	}

	// Save the tail as carry for the next read.
	n := len(buf)
	if n > carrySize {
		n = carrySize
	}
	copy(t.carry[:], buf[len(buf)-n:])
	t.carryN = n

	// Write into the active queue top slot. This tracked buffer is SEPARATE from
	// the bytes writeUser already forwarded to the child: reporting escapes (focus
	// in/out, mouse) still reach the child verbatim, but are stripped here so they
	// never get replayed into the user's line by execPrompt's reinject. Skip the
	// strip inside a bracketed paste so literal pasted bytes stay intact.
	text := chunk
	if !t.inBracketedPaste {
		text = stripReportingEscapes(chunk)
	}
	t.activeInput = append(t.activeInput, text...)
	if len(t.activeInput) > maxInputBuf {
		t.activeInput = t.activeInput[len(t.activeInput)-maxInputBuf:]
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

// defangPasteMarkers neutralises bracketed-paste markers (200~ / 201~) embedded
// in s WITHOUT discarding the characters: it drops only the introducing ESC so
// the sequence can no longer end/open a paste, leaving the printable remainder
// ("[201~") as literal text the user still sees. Only these markers escape a
// bracketed-paste wrap — a 201~ ends paste mode early (tail then runs as
// keystrokes), a 200~ opens a nested paste; every other byte stays literal in
// the wrap, so nothing else is touched. The loop guards a doubled ESC
// (\x1b\x1b[201~) that would re-form the marker after a single pass. Used by
// execPrompt, the sole owner of paste framing.
func defangPasteMarkers(s string) string {
	if !strings.Contains(s, "\x1b[2") { // fast path: no candidate marker
		return s
	}
	for strings.Contains(s, pasteStart) {
		s = strings.ReplaceAll(s, pasteStart, pasteStart[1:]) // "\x1b[200~" -> "[200~"
	}
	for strings.Contains(s, pasteEnd) {
		s = strings.ReplaceAll(s, pasteEnd, pasteEnd[1:]) // "\x1b[201~" -> "[201~"
	}
	return s
}

// stripReportingEscapes removes terminal "reporting" escape sequences — focus
// in/out (ESC [ I, ESC [ O) and mouse events (ESC [ M b b b, ESC [ < … M|m) —
// from b. These are forwarded to the child verbatim by writeUser; this only
// keeps them OUT of the reinject buffer, where replaying them would corrupt the
// user's visible input line. The terminal delivers these sequences atomically,
// so per-chunk stripping is sufficient.
func stripReportingEscapes(b []byte) []byte {
	if !bytes.Contains(b, []byte{0x1b}) {
		return b // fast path: no escapes at all
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if b[i] == 0x1b && i+2 < len(b) && b[i+1] == '[' {
			switch {
			case b[i+2] == 'I' || b[i+2] == 'O': // focus in / out
				i += 3
				continue
			case b[i+2] == '<': // mouse SGR: ESC [ < … (M|m)
				j := i + 3
				for j < len(b) && b[j] != 'M' && b[j] != 'm' {
					j++
				}
				if j < len(b) {
					i = j + 1
					continue
				}
			case b[i+2] == 'M': // mouse X10: ESC [ M + 3 bytes
				i += 6
				if i > len(b) {
					i = len(b)
				}
				continue
			}
		}
		out = append(out, b[i])
		i++
	}
	return out
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
