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

	ptydaemon "github.com/Shaik-Sirajuddin/memory/svc/ptydaemon"
	"github.com/creack/pty"
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
	var filtered []*PTYTerminalInfo
	for _, info := range resp.Sessions {
		if info != nil && info.AgentID == agentID {
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

	// Open a second connection for stdin relay so the daemon can track human
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
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		_ = ptmx.Close()
		return err
	}
	defer func() {
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
		// Undo any cursor/screen escape codes the child may have emitted.
		// MakeRaw restores termios but not escape-code state.
		_, _ = os.Stdout.WriteString("\033[?25h\033[?1049l\033[0m\r\n")
		_ = ptmx.Close()
	}()

	// Sync PTY size to the current terminal immediately.
	inheritSize(ptmx)

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
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
			_, _ = os.Stdout.WriteString("\033[?25h\033[?1049l\033[0m\r\n")
			os.Exit(0)
		case <-ctx.Done():
		}
	}()

	done := make(chan struct{}, 2)
	go func() {
		n, err := io.Copy(stdinDst, os.Stdin)
		if err != nil && stdinDst != ptmx {
			// Relay conn died — fall back to writing stdin directly to ptmx
			// so the session stays alive and input is not silently lost.
			// ptmx is safe to write here: deferred ptmx.Close() only runs after
			// the select below exits, which waits for this goroutine's done signal.
			ptylog.Warn("stdin-relay write failed, falling back to direct ptmx write",
				"err", err, "bytes_relayed", n)
			_, _ = io.Copy(ptmx, os.Stdin)
		}
		done <- struct{}{}
	}()
	go func() { _, _ = io.Copy(os.Stdout, ptmx); done <- struct{}{} }()
	select {
	case <-ctx.Done():
	case <-done:
	}
	return nil
}

// inheritSize copies the calling terminal's window size onto ptmx.
func inheritSize(ptmx *os.File) {
	size, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		return
	}
	_ = pty.Setsize(ptmx, size)
}
