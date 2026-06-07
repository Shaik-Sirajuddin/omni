//go:build linux || darwin

package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	pkgpty "github.com/Shaik-Sirajuddin/memory/pkg/pty"
	ptydaemon "github.com/Shaik-Sirajuddin/memory/svc/ptydaemon"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type UnixSocketClient struct {
	socketPath string
}

type unixRequest struct {
	Op        string   `json:"op"`
	AgentID   string   `json:"agent_id,omitempty"`
	SessionID string   `json:"session_id"`
	Command   []string `json:"command,omitempty"`
	Env       []string `json:"env,omitempty"`
	Dir       string   `json:"dir,omitempty"`
	Input     string   `json:"input,omitempty"`
	Key       string   `json:"key,omitempty"`
	Data      []byte   `json:"data,omitempty"`
	PID       int      `json:"pid,omitempty"`
	SubmitKey string   `json:"submit_key,omitempty"`
	Safe      bool     `json:"safe,omitempty"`
	Force     bool     `json:"force,omitempty"`
}

type unixResponse struct {
	OK        bool               `json:"ok"`
	SessionID string             `json:"session_id,omitempty"`
	Error     string             `json:"error,omitempty"`
	Sessions  []*PTYTerminalInfo `json:"sessions,omitempty"`
	Count     int                `json:"count,omitempty"`
	Processes []AttachedProcess  `json:"processes,omitempty"`
}

// NewUnixSocketClient returns a raw Unix-socket PTY daemon client.
// Empty socketPath falls back to ptydaemon.DefaultSocketPath().
func NewUnixSocketClient(socketPath string) *UnixSocketClient {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		socketPath = ptydaemon.DefaultSocketPath()
	}
	return &UnixSocketClient{socketPath: socketPath}
}

func newUnixClient() *UnixSocketClient {
	return NewUnixSocketClient(ptydaemon.DefaultSocketPath())
}

// Pipe sends raw bytes to the PTY master of the given session.
func (c *UnixSocketClient) Pipe(agentID, sessionID string, data []byte) error {
	ptylog.Debug("client: pipe", "agent_id", agentID, "session_id", sessionID, "bytes", len(data))
	if err := c.do(unixRequest{Op: "pipe", AgentID: agentID, SessionID: sessionID, Data: data}); err != nil {
		ptylog.Error("client: pipe failed", "err", err, "agent_id", agentID, "session_id", sessionID)
		return err
	}
	return nil
}

// Register adopts an externally started process into the daemon as a managed session.
func (c *UnixSocketClient) Register(agentID, sessionID, processID string) error {
	ptylog.Debug("client: register", "agent_id", agentID, "session_id", sessionID, "pid", processID)
	if agentID == "" || sessionID == "" || processID == "" {
		ptylog.Debug("client: register skipped — empty field", "agent_id", agentID, "session_id", sessionID, "pid", processID)
		return nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(processID))
	if err != nil {
		return fmt.Errorf("ptydaemon/clients: register: invalid pid %q: %w", processID, err)
	}
	if err := c.do(unixRequest{Op: "adopt", AgentID: agentID, SessionID: sessionID, PID: pid}); err != nil {
		ptylog.Error("client: register failed", "err", err, "agent_id", agentID, "session_id", sessionID, "pid", pid)
		return err
	}
	ptylog.Debug("client: register ok", "agent_id", agentID, "session_id", sessionID, "pid", pid)
	return nil
}

func (c *UnixSocketClient) List(agentID string) ([]*PTYTerminalInfo, error) {
	ptylog.Debug("client: list", "agent_id", agentID)
	resp, conn, err := c.roundtrip(unixRequest{Op: "list"})
	if conn != nil {
		conn.Close()
	}
	if err != nil {
		ptylog.Error("client: list failed", "err", err, "agent_id", agentID)
		return nil, err
	}
	if !resp.OK {
		ptylog.Error("client: list error response", "error", resp.Error, "agent_id", agentID)
		return nil, errors.New(resp.Error)
	}
	if agentID == "" {
		ptylog.Debug("client: list ok", "count", len(resp.Sessions))
		return resp.Sessions, nil
	}
	// Apply the SAME leniency as the daemon's get()/ListSessions and the server
	// side: a terminal created by the Start path carries agent_id="" until adopt
	// rewrites it ~ms later. Filtering by an EXACT agent_id match here would strip
	// that still-un-adopted terminal back out of the response, re-introducing the
	// Start→adopt loop the server fix exists to prevent. The caller disambiguates
	// by SessionID (globally unique UUID), so including agent_id="" entries cannot
	// cross-contaminate a different agent's guard check.
	var filtered []*PTYTerminalInfo
	for _, info := range resp.Sessions {
		if info != nil && (info.AgentID == agentID || info.AgentID == "") {
			filtered = append(filtered, info)
		}
	}
	ptylog.Debug("client: list ok", "agent_id", agentID, "count", len(filtered))
	return filtered, nil
}

func (c *UnixSocketClient) Get(_, sessionID string) (*PTYTerminalInfo, error) {
	ptylog.Debug("client: get", "session_id", sessionID)
	resp, conn, err := c.roundtrip(unixRequest{Op: "get", SessionID: sessionID})
	if conn != nil {
		conn.Close()
	}
	if err != nil {
		ptylog.Error("client: get failed", "err", err, "session_id", sessionID)
		return nil, err
	}
	if !resp.OK {
		ptylog.Debug("client: get not found", "session_id", sessionID)
		return nil, nil // not found
	}
	if len(resp.Sessions) > 0 {
		ptylog.Debug("client: get ok", "session_id", sessionID, "status", resp.Sessions[0].Status)
		return resp.Sessions[0], nil
	}
	return nil, nil
}

// Start spawns a PTY session via the daemon.
// It resolves command[0] to an absolute path via exec.LookPath so the daemon
// can exec binaries installed in the caller's PATH.
// dir sets the working directory for the spawned process; empty means inherit daemon cwd.
func (c *UnixSocketClient) Start(sessionID string, command []string, env []string, dir, submitKey string) error {
	ptylog.Debug("client: start", "session_id", sessionID, "command", command, "dir", dir)
	if len(command) > 0 {
		original := command[0]
		if abs, err := exec.LookPath(command[0]); err == nil {
			command = append([]string{abs}, command[1:]...)
			ptylog.Debug("client: start resolved binary", "original", original, "resolved", abs)
		} else {
			ptylog.Debug("client: start LookPath failed, using original", "command", original, "err", err)
		}
	}
	if err := c.do(unixRequest{Op: "start", SessionID: sessionID, Command: command, Env: env, Dir: dir, SubmitKey: submitKey}); err != nil {
		ptylog.Error("client: start failed", "err", err, "session_id", sessionID, "command", command)
		return err
	}
	ptylog.Debug("client: start ok", "session_id", sessionID, "command", command, "dir", dir)
	return nil
}

func (c *UnixSocketClient) Attach(ctx context.Context, sessionID string) error {
	ptylog.Debug("client: attach", "session_id", sessionID, "socket", c.socketPath)

	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		ptylog.Error("client: attach dial failed", "err", err, "session_id", sessionID)
		return err
	}
	uc := conn.(*net.UnixConn)
	defer uc.Close()

	if err := json.NewEncoder(uc).Encode(unixRequest{Op: "attach", SessionID: sessionID}); err != nil {
		ptylog.Error("client: attach request failed", "err", err, "session_id", sessionID)
		return err
	}

	buf := make([]byte, 4096)
	oob := make([]byte, unix.CmsgSpace(4))
	n, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
	if err != nil {
		ptylog.Error("client: attach ReadMsgUnix failed", "err", err, "session_id", sessionID)
		return err
	}

	var resp unixResponse
	if n == 0 {
		ptylog.Error("client: attach empty response", "session_id", sessionID)
		return errors.New("ptydaemon/clients: attach: empty daemon response")
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf[:n]), &resp); err != nil {
		ptylog.Error("client: attach decode failed", "err", err, "session_id", sessionID, "bytes", n)
		return err
	}
	if !resp.OK {
		ptylog.Error("client: attach denied by daemon", "error", resp.Error, "session_id", sessionID)
		return errors.New(resp.Error)
	}
	ptylog.Debug("client: attach accepted by daemon, reading SCM_RIGHTS fd", "session_id", sessionID)

	// Clear the daemon's attachment record when we leave (agent exits or the
	// user hits the detach key) so a later resume is not rejected as
	// "already attached". Best-effort; the daemon also reclaims stale PIDs.
	defer func() {
		if derr := c.do(unixRequest{Op: "detach", SessionID: sessionID}); derr != nil {
			ptylog.Debug("client: detach-on-exit failed (daemon will reclaim stale pid)", "err", derr, "session_id", sessionID)
		}
	}()

	scms, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(scms) == 0 {
		ptylog.Error("client: attach no control message", "err", err, "session_id", sessionID)
		return errors.New("ptydaemon/clients: attach: no control message received")
	}
	fds, err := unix.ParseUnixRights(&scms[0])
	if err != nil || len(fds) == 0 {
		ptylog.Error("client: attach no fd in control message", "err", err, "session_id", sessionID)
		return errors.New("ptydaemon/clients: attach: no fd in control message")
	}

	ptylog.Debug("client: attach received master fd, entering raw terminal", "session_id", sessionID, "fd", fds[0])
	ptmx := os.NewFile(uintptr(fds[0]), "ptmx")

	// Open a second connection for stdin relay so the daemon can track user
	// input and serialise it against ExecInSession writes.
	var stdinDst io.Writer = ptmx // fallback: write stdin directly to ptmx
	relayConn, relayErr := c.openStdinRelay(sessionID)
	if relayErr != nil {
		ptylog.Warn("client: stdin-relay unavailable, falling back to direct ptmx write", "err", relayErr, "session_id", sessionID)
	} else {
		defer relayConn.Close()
		stdinDst = relayConn
	}

	if err := attachToTerminal(ctx, ptmx, stdinDst); err != nil {
		ptylog.Error("client: attach terminal setup failed", "err", err, "session_id", sessionID)
		return err
	}
	ptylog.Debug("client: attach terminal io running", "session_id", sessionID)
	return nil
}

// openStdinRelay dials the daemon, sends a stdin-relay handshake, and returns
// the open connection ready for raw stdin bytes. The caller must close it.
func (c *UnixSocketClient) openStdinRelay(sessionID string) (net.Conn, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return nil, err
	}
	if err := json.NewEncoder(conn).Encode(unixRequest{Op: "stdin-relay", SessionID: sessionID}); err != nil {
		conn.Close()
		return nil, err
	}
	var resp unixResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		conn.Close()
		return nil, err
	}
	if !resp.OK {
		conn.Close()
		return nil, errors.New(resp.Error)
	}
	return conn, nil
}

// ListAttached returns all processes holding the PTY master fd of the session.
func (c *UnixSocketClient) ListAttached(sessionID string) ([]AttachedProcess, error) {
	ptylog.Debug("client: list-attached", "session_id", sessionID)
	resp, conn, err := c.roundtrip(unixRequest{Op: "list-attached", SessionID: sessionID})
	if conn != nil {
		conn.Close()
	}
	if err != nil {
		ptylog.Error("client: list-attached failed", "err", err, "session_id", sessionID)
		return nil, err
	}
	if !resp.OK {
		ptylog.Error("client: list-attached error response", "error", resp.Error, "session_id", sessionID)
		return nil, errors.New(resp.Error)
	}
	procs := make([]AttachedProcess, 0, len(resp.Processes))
	for _, p := range resp.Processes {
		procs = append(procs, AttachedProcess{PID: p.PID, Comm: p.Comm, Fd: p.Fd})
	}
	ptylog.Debug("client: list-attached ok", "session_id", sessionID, "count", len(procs))
	return procs, nil
}

// MetaAttached returns the count of processes holding the PTY master fd.
func (c *UnixSocketClient) MetaAttached(sessionID string) (int, error) {
	ptylog.Debug("client: meta-attached", "session_id", sessionID)
	resp, conn, err := c.roundtrip(unixRequest{Op: "meta-attached", SessionID: sessionID})
	if conn != nil {
		conn.Close()
	}
	if err != nil {
		ptylog.Error("client: meta-attached failed", "err", err, "session_id", sessionID)
		return 0, err
	}
	if !resp.OK {
		ptylog.Error("client: meta-attached error response", "error", resp.Error, "session_id", sessionID)
		return 0, errors.New(resp.Error)
	}
	ptylog.Debug("client: meta-attached ok", "session_id", sessionID, "count", resp.Count)
	return resp.Count, nil
}

// Exec sends a pre-formatted payload to the PTY master. The daemon pipes it as-is and adds submit-key retries.
func (c *UnixSocketClient) Exec(sessionID, input string) error {
	ptylog.Debug("client: exec", "session_id", sessionID, "input_len", len(input))
	if err := c.do(unixRequest{Op: "exec", SessionID: sessionID, Input: input}); err != nil {
		ptylog.Error("client: exec failed", "err", err, "session_id", sessionID)
		return err
	}
	return nil
}

func (c *UnixSocketClient) Stop(sessionID string) error {
	ptylog.Debug("client: stop", "session_id", sessionID)
	if err := c.do(unixRequest{Op: "stop", SessionID: sessionID}); err != nil {
		ptylog.Error("client: stop failed", "err", err, "session_id", sessionID)
		return err
	}
	ptylog.Debug("client: stop ok", "session_id", sessionID)
	return nil
}

// StopSafe stops the session only if no client is currently attached.
// Pass force=true to kill even when a client holds the PTY master fd.
// Use Stop for the original unconditional-kill behaviour.
func (c *UnixSocketClient) StopSafe(sessionID string, force bool) error {
	ptylog.Debug("client: stop-safe", "session_id", sessionID, "force", force)
	if err := c.do(unixRequest{Op: "stop", SessionID: sessionID, Safe: true, Force: force}); err != nil {
		ptylog.Error("client: stop-safe failed", "err", err, "session_id", sessionID)
		return err
	}
	ptylog.Debug("client: stop-safe ok", "session_id", sessionID)
	return nil
}

// Detach explicitly clears the attachment record for the session, allowing the
// next caller to attach immediately without waiting for the daemon to reap the
// previous client's process entry.
func (c *UnixSocketClient) Detach(sessionID string) error {
	ptylog.Debug("client: detach", "session_id", sessionID)
	if err := c.do(unixRequest{Op: "detach", SessionID: sessionID}); err != nil {
		ptylog.Error("client: detach failed", "err", err, "session_id", sessionID)
		return err
	}
	ptylog.Debug("client: detach ok", "session_id", sessionID)
	return nil
}

func (c *UnixSocketClient) roundtrip(req unixRequest) (unixResponse, *net.UnixConn, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return unixResponse{}, nil, err
	}
	uc := conn.(*net.UnixConn)
	if err := json.NewEncoder(uc).Encode(req); err != nil {
		uc.Close()
		return unixResponse{}, nil, err
	}
	var resp unixResponse
	if err := json.NewDecoder(uc).Decode(&resp); err != nil {
		uc.Close()
		return unixResponse{}, nil, err
	}
	return resp, uc, nil
}

func (c *UnixSocketClient) do(req unixRequest) error {
	resp, conn, err := c.roundtrip(req)
	if conn != nil {
		conn.Close()
	}
	if err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

func attachToTerminal(ctx context.Context, ptmx *os.File, stdinDst io.Writer) error {
	// Fail loudly before attempting MakeRaw — a non-TTY stdin produces
	// "inappropriate ioctl for device" which gets swallowed and leaves the
	// user with a silently dead session (no output, no input, no error shown).
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		_ = ptmx.Close()
		return fmt.Errorf("attach requires an interactive terminal: stdin is not a TTY (run with a real terminal or use --detach for background mode)")
	}
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		_ = ptmx.Close()
		return err
	}
	ptylog.Debug("client: attach begin (client takes over master, drain pausing)")
	defer func() {
		ptylog.Debug("client: detach (releasing master, writing termResetSeq, drain resuming)")
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
		// Undo the private DEC modes the child may have set (mouse tracking,
		// bracketed paste, alt screen, cursor). MakeRaw restores termios but not
		// these escape-code modes.
		_, _ = os.Stdout.WriteString(termResetSeq)
		_ = ptmx.Close()
	}()

	// Sync PTY size so the child lays out at the real terminal size as it boots.
	inheritSize(ptmx)

	// Repaint pump. A resumed TUI (e.g. claude) can come up SILENT — it paints
	// nothing until it processes a real SIGWINCH after its resize handler is
	// installed, and it gives no signal of when that is. Firing once on attach
	// is too early (handler not up); waiting for first output deadlocks (no
	// output until poked). So poke it with a forced repaint on a few beats and
	// stop as soon as it responds with output (painted).
	painted := make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(redrawPumpInterval)
		defer ticker.Stop()
		for i := 0; i < redrawPumpBeats; i++ {
			select {
			case <-painted:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				forceRedraw(ptmx)
			}
		}
	}()

	// Forward SIGWINCH so PTY tracks terminal resizes.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			inheritSize(ptmx)
		}
	}()

	// SIGTERM can be delivered even in raw mode (unlike Ctrl+C which becomes 0x03).
	// Restore the terminal synchronously before letting the signal kill the process.
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGTERM)
	defer signal.Stop(sigterm)
	go func() {
		select {
		case <-sigterm:
			ptylog.Debug("client: SIGTERM (restoring terminal, writing termResetSeq)")
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
			// Same full reset as the normal detach path so mouse/paste modes are
			// undone too, not just cursor/alt-screen.
			_, _ = os.Stdout.WriteString(termResetSeq)
			os.Exit(0)
		case <-ctx.Done():
		}
	}()

	done := make(chan struct{}, 2)
	go func() {
		// Scan stdin for the detach key (Ctrl+\, 0x1c): it leaves the session
		// running and returns to the caller, which clears the attachment record.
		// Everything else is forwarded raw to the child (so Ctrl+C still reaches
		// the agent). On relay failure we fall back to writing ptmx directly.
		buf := make([]byte, 4096)
		for {
			n, rerr := os.Stdin.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				if i := bytes.IndexByte(chunk, detachKey); i >= 0 {
					if i > 0 {
						_, _ = stdinDst.Write(chunk[:i])
					}
					ptylog.Debug("client: detach key (Ctrl+\\) pressed")
					break
				}
				if _, werr := stdinDst.Write(chunk); werr != nil && stdinDst != ptmx {
					ptylog.Warn("stdin-relay write failed, falling back to direct ptmx write", "err", werr)
					stdinDst = ptmx
					_, _ = ptmx.Write(chunk)
				}
			}
			if rerr != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	go func() {
		buf := make([]byte, 32*1024)
		first := true
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				if first {
					first = false
					// The child responded with output → it has painted. Stop the
					// repaint pump.
					select {
					case painted <- struct{}{}:
					default:
					}
				}
				if _, werr := os.Stdout.Write(buf[:n]); werr != nil {
					break
				}
			}
			if rerr != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	select {
	case <-ctx.Done():
	case <-done:
	}
	return nil
}

// inheritSize copies the calling terminal's window size onto ptmx.
func inheritSize(ptmx *os.File) {
	ws, err := pkgpty.GetWinsize(os.Stdin)
	if err != nil {
		return
	}
	_ = pkgpty.SetWinsize(ptmx, ws)
}

// redrawSettle is how long forceRedraw waits between the nudge and the real
// size so the child's SIGWINCH handler runs for each — standard signals
// coalesce, so a rapid set/restore would merge into a single no-op event.
const redrawSettle = 40 * time.Millisecond

// detachKey is the byte that detaches the interactive client (Ctrl+\). Ctrl+C
// is intentionally NOT used — it is forwarded to the agent.
const detachKey = 0x1c

// termResetSeq undoes the private DEC modes a full-screen agent (claude/codex/…)
// turns on, so the user's outer terminal is handed back in a sane state on
// detach. term.Restore only fixes termios (echo/canon) — it does NOT touch these
// app-set output modes, so we must emit the disables ourselves. Omitting the
// mouse resets leaves the terminal in mouse-tracking mode after detach, which
// breaks native click/drag text selection ("mouse copy"); omitting 2004 leaves
// bracketed paste on, so later pastes show stray \x1b[200~ markers.
//
//	?1000/1002/1003/1006 l  disable mouse click/drag/any-motion/SGR reporting
//	?2004 l                 disable bracketed paste
//	?25 h                   show cursor
//	?1049 l                 leave alternate screen
//	0 m                     reset SGR (colors/attrs)
const termResetSeq = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1b[?25h\x1b[?1049l\x1b[0m\r\n"

// Repaint pump cadence: poke a resumed TUI with a forced repaint on these beats
// until it paints (or the beats run out and the user can press a key).
const (
	redrawPumpInterval = 400 * time.Millisecond
	redrawPumpBeats    = 6
)

// forceRedraw sets ptmx to the calling terminal's size, guaranteeing a genuine
// SIGWINCH to the child even when the PTY is already at that size.
//
// On resume the PTY winsize persists from the previous client, so a plain
// inheritSize() is frequently a no-op — and the kernel suppresses SIGWINCH on an
// unchanged TIOCSWINSZ (tty_do_resize early-returns). Full-screen TUIs that only
// repaint on a real resize (e.g. claude/Ink) then show a blank screen until the
// next keypress. We force a real delta: briefly set a different size, settle so
// the child processes that SIGWINCH, then set the true size.
func forceRedraw(ptmx *os.File) {
	size, err := pkgpty.GetWinsize(os.Stdin)
	if err != nil {
		return // not a tty; nothing to repaint against
	}
	if size.Rows == 0 || size.Cols == 0 {
		// No real window size (e.g. a harness PTY without stty/winsize). A 0-size
		// resize is meaningless and would never trigger a repaint — skip it.
		return
	}
	nudge := size
	if nudge.Rows > 1 {
		nudge.Rows--
	} else {
		nudge.Rows++
	}
	_ = pkgpty.SetWinsize(ptmx, nudge)
	time.Sleep(redrawSettle)
	_ = pkgpty.SetWinsize(ptmx, size)
}
