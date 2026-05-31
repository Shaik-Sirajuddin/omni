package ptyunix

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Shaik-Sirajuddin/memory/svc/ptydaemon/internal"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// session is an in-memory PTY session used in fallback (no-inner) mode.
type session struct {
	ptmx *os.File
	cmd  *exec.Cmd
	mu   sync.Mutex
}

// Daemon serves the ptyunix raw JSON protocol over a Unix domain socket.
// When constructed with NewDaemonWithInner, all session operations are
// delegated to the provided PTYDaemon (store-backed). NewDaemon returns
// a lightweight in-memory daemon for backward compatibility.
type Daemon struct {
	mu       sync.Mutex
	sessions map[string]*session // used only when inner == nil
	inner    internal.PTYDaemon  // nil = in-memory fallback

	// attachedPIDs tracks the PID of the client currently holding the PTY
	// master fd for each session (keyed by sessionID). Used instead of
	// /proc inode scanning, which is broken for PTY masters: all ptmx fds
	// share the same inode on Linux.
	attachedPIDs sync.Map // sessionID (string) → client PID (int)

	// serveCtx is set by ListenAndServe and used by handleStdinRelay to
	// interrupt blocked reads when the daemon shuts down.
	serveCtx context.Context
}

// NewDaemon returns an in-memory daemon with no persistent store.
// Use NewDaemonWithInner for production use.
func NewDaemon() *Daemon {
	return &Daemon{sessions: make(map[string]*session)}
}

// NewDaemonWithInner returns a daemon that delegates all session operations
// to the provided PTYDaemon, which is expected to be store-backed.
func NewDaemonWithInner(inner internal.PTYDaemon) *Daemon {
	return &Daemon{inner: inner}
}

// ListenAndServe listens on socketPath and serves connections until ctx is cancelled.
func (d *Daemon) ListenAndServe(ctx context.Context, socketPath string) error {
	d.serveCtx = ctx
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0600); err != nil {
		ptylog.Warn("failed to set socket permissions", "path", socketPath, "err", err)
	}

	ptylog.Info("ptyunix listening", "socket", socketPath)

	go func() {
		<-ctx.Done()
		ln.Close()
		_ = os.Remove(socketPath)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go d.handleConn(conn.(*net.UnixConn))
	}
}

func (d *Daemon) handleConn(conn *net.UnixConn) {
	defer conn.Close()

	// Use a bufio.Reader so we can pass any pre-buffered bytes to the stdin
	// relay without losing them (json.Decoder reads ahead internally).
	br := bufio.NewReader(conn)

	var req Request
	if err := json.NewDecoder(br).Decode(&req); err != nil {
		ptylog.Debug("failed to decode request", "err", err)
		return
	}

	ptylog.Debug("request received", "op", req.Op, "agent_id", req.AgentID, "session_id", req.SessionID)

	switch req.Op {
	case "start":
		d.handleStart(conn, req)
	case "create":
		d.handleCreate(conn, req)
	case "adopt":
		d.handleAdopt(conn, req)
	case "attach":
		d.handleAttach(conn, req)
	case "detach":
		d.handleDetach(conn, req)
	case "stdin-relay":
		// Pass the bufio.Reader so pre-buffered bytes are not lost.
		d.handleStdinRelay(conn, br, req)
	case "pipe":
		d.handlePipe(conn, req)
	case "exec":
		d.handleExec(conn, req)
	case "keybind":
		d.handleKeybind(conn, req)
	case "stop":
		d.handleStop(conn, req)
	case "list":
		d.handleList(conn, req)
	case "get":
		d.handleGet(conn, req)
	case "list-attached":
		d.handleListAttached(conn, req)
	case "meta-attached":
		d.handleMetaAttached(conn, req)
	default:
		respond(conn, Response{Error: "unknown op: " + req.Op})
	}
}

// handleStart is the legacy in-memory start op (no agentID, no store).
// When inner is set it delegates to handleCreate for store-backed behaviour.
func (d *Daemon) handleStart(conn *net.UnixConn, req Request) {
	if d.inner != nil {
		d.handleCreate(conn, req)
		return
	}
	if len(req.Command) == 0 {
		respond(conn, Response{Error: "command is required"})
		return
	}

	ptylog.Debug("starting in-memory session", "session_id", req.SessionID, "command", req.Command)

	cmd := exec.Command(req.Command[0], req.Command[1:]...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		ptylog.Error("pty start failed", "err", err, "session_id", req.SessionID)
		respond(conn, Response{Error: err.Error()})
		return
	}

	s := &session{ptmx: ptmx, cmd: cmd}
	d.mu.Lock()
	d.sessions[req.SessionID] = s
	d.mu.Unlock()

	go func() {
		_ = cmd.Wait()
		_ = ptmx.Close()
		d.mu.Lock()
		delete(d.sessions, req.SessionID)
		d.mu.Unlock()
		ptylog.Debug("in-memory session exited", "session_id", req.SessionID)
	}()

	ptylog.Info("in-memory session started", "session_id", req.SessionID, "pid", cmd.Process.Pid)
	respond(conn, Response{OK: true, SessionID: req.SessionID})
}

// handleCreate starts a new store-backed PTY session.
func (d *Daemon) handleCreate(conn *net.UnixConn, req Request) {
	if d.inner == nil {
		respond(conn, Response{Error: "create requires a store-backed daemon"})
		return
	}
	if len(req.Command) == 0 {
		respond(conn, Response{Error: "command is required"})
		return
	}

	p := internal.PTYCreateParams{
		AgentID:   req.AgentID,
		SessionID: req.SessionID,
		Command:   req.Command[0],
		Args:      req.Command[1:],
		SubmitKey: req.SubmitKey,
		Env:       req.Env,
		Dir:       req.Dir,
	}

	ptylog.Debug("creating session", "agent_id", p.AgentID, "session_id", p.SessionID, "command", p.Command)

	info, err := d.inner.Create(p)
	if err != nil {
		ptylog.Error("create session failed", "err", err, "session_id", req.SessionID)
		respond(conn, Response{Error: err.Error()})
		return
	}

	ptylog.Info("session created", "agent_id", info.AgentID, "session_id", info.SessionID, "pid", info.PID)
	respond(conn, Response{OK: true, SessionID: info.SessionID})
}

// handleAdopt registers an externally started process as a managed session.
func (d *Daemon) handleAdopt(conn *net.UnixConn, req Request) {
	if d.inner == nil {
		respond(conn, Response{Error: "adopt requires a store-backed daemon"})
		return
	}

	ptylog.Debug("adopting process", "agent_id", req.AgentID, "session_id", req.SessionID, "pid", req.PID)

	if err := d.inner.Adopt(req.AgentID, req.SessionID, req.PID, req.SubmitKey); err != nil {
		ptylog.Error("adopt failed", "err", err, "session_id", req.SessionID, "pid", req.PID)
		respond(conn, Response{Error: err.Error()})
		return
	}

	ptylog.Info("process adopted", "agent_id", req.AgentID, "session_id", req.SessionID, "pid", req.PID)
	respond(conn, Response{OK: true})
}

// handleAttach sends the PTY master fd to the caller via SCM_RIGHTS.
// The attach guard tracks the attached client's PID via SO_PEERCRED.
// A second attach is rejected unless the previously attached process has exited.
func (d *Daemon) handleAttach(conn *net.UnixConn, req Request) {
	// Resolve client PID before doing anything else so we can guard accurately.
	clientPID, err := peerPID(conn)
	if err != nil {
		ptylog.Warn("attach: SO_PEERCRED failed, skipping guard", "err", err, "session_id", req.SessionID)
	}

	ptylog.Debug("attach guard check", "session_id", req.SessionID, "client_pid", clientPID)

	// Reject if a live client is already attached to this specific session.
	if prev, ok := d.attachedPIDs.Load(req.SessionID); ok {
		prevPID := prev.(int)
		if processExists(prevPID) {
			ptylog.Warn("attach denied — session already attached", "session_id", req.SessionID, "holder_pid", prevPID)
			respond(conn, Response{
				OK:    false,
				Error: fmt.Sprintf("session already attached (pid %d)", prevPID),
			})
			return
		}
		// Previous client has exited; allow re-attach.
		ptylog.Debug("attach: previous client gone, allowing re-attach", "session_id", req.SessionID, "old_pid", prevPID)
		d.attachedPIDs.Delete(req.SessionID)
	}

	ptmx, err := d.getMasterFd(req)
	if err != nil {
		respond(conn, Response{Error: err.Error()})
		return
	}

	rights := unix.UnixRights(int(ptmx.Fd()))
	payload, err := json.Marshal(Response{OK: true})
	if err != nil {
		ptylog.Error("attach response encode failed", "err", err, "session_id", req.SessionID)
		respond(conn, Response{Error: err.Error()})
		return
	}
	payload = append(payload, '\n')

	if _, _, err := conn.WriteMsgUnix(payload, rights, nil); err != nil {
		ptylog.Error("SCM_RIGHTS send failed", "err", err, "session_id", req.SessionID)
		return
	}

	if clientPID > 0 {
		d.attachedPIDs.Store(req.SessionID, clientPID)
	}
	ptylog.Info("fd granted to client", "session_id", req.SessionID, "client_pid", clientPID)
}

// handleStdinRelay acknowledges the request then enters a raw streaming loop,
// forwarding every chunk from the client to the PTY master via
// inner.StdinRelay — which calls trackHumanInput before each write so that
// ExecInSession can serialise around human-typed input.
//
// br must be the bufio.Reader wrapping conn from handleConn; it carries any
// bytes the JSON decoder pre-buffered after reading the request envelope.
func (d *Daemon) handleStdinRelay(conn *net.UnixConn, br *bufio.Reader, req Request) {
	if d.inner == nil {
		respond(conn, Response{Error: "stdin-relay requires a store-backed daemon"})
		return
	}
	respond(conn, Response{OK: true})

	ctx := d.serveCtx
	if ctx == nil {
		ctx = context.Background()
	}

	// When the daemon shuts down, set a past deadline on conn to unblock any
	// pending r.Read — preventing goroutine leak on dirty client disconnect.
	go func() {
		<-ctx.Done()
		_ = conn.SetReadDeadline(time.Now())
	}()

	// io.MultiReader drains any bytes the json.Decoder pre-buffered, then
	// continues reading from the raw connection.
	r := io.MultiReader(br, conn)
	if err := d.inner.StdinRelay(ctx, req.AgentID, req.SessionID, r); err != nil {
		ptylog.Debug("stdin-relay ended", "session_id", req.SessionID, "err", err)
	}
}

// handleDetach clears the attachment record for a session so the next caller
// can attach without waiting for the previous client's process to be reaped.
// Idempotent: succeeds even when the session was never attached.
func (d *Daemon) handleDetach(conn *net.UnixConn, req Request) {
	if _, loaded := d.attachedPIDs.LoadAndDelete(req.SessionID); loaded {
		ptylog.Debug("detach: cleared attachment record", "session_id", req.SessionID)
	} else {
		ptylog.Warn("detach: session had no attachment record (idempotent)", "session_id", req.SessionID)
	}
	respond(conn, Response{OK: true})
}

// handlePipe writes raw bytes to the PTY master.
func (d *Daemon) handlePipe(conn *net.UnixConn, req Request) {
	if d.inner == nil {
		respond(conn, Response{Error: "pipe requires a store-backed daemon"})
		return
	}

	ptylog.Debug("pipe", "agent_id", req.AgentID, "session_id", req.SessionID, "bytes", len(req.Data))

	if err := d.inner.Pipe(req.AgentID, req.SessionID, req.Data); err != nil {
		ptylog.Error("pipe failed", "err", err, "session_id", req.SessionID)
		respond(conn, Response{Error: err.Error()})
		return
	}
	respond(conn, Response{OK: true})
}

// handleExec pipes the pre-formatted payload from the connector and then retries
// the submit key (100ms, 200ms) to handle timing races in the terminal.
// The connector already wraps the prompt in bracketed paste + submit key, so we
// must not re-wrap — we only add the retry safety net on top.
func (d *Daemon) handleExec(conn *net.UnixConn, req Request) {
	ptylog.Debug("exec", "agent_id", req.AgentID, "session_id", req.SessionID)

	if d.inner != nil {
		if err := d.inner.Exec(req.AgentID, req.SessionID, req.Input); err != nil {
			if errors.Is(err, internal.ErrNotFound) {
				respond(conn, Response{Error: "session not found"})
				return
			}
			ptylog.Error("exec failed", "err", err, "session_id", req.SessionID)
			respond(conn, Response{Error: err.Error()})
			return
		}
		respond(conn, Response{OK: true})
		return
	}

	// in-memory fallback
	d.mu.Lock()
	s, ok := d.sessions[req.SessionID]
	d.mu.Unlock()
	if !ok {
		respond(conn, Response{Error: "session not found"})
		return
	}
	s.mu.Lock()
	_, err := s.ptmx.Write([]byte(req.Input))
	s.mu.Unlock()
	if err != nil {
		respond(conn, Response{Error: err.Error()})
		return
	}
	respond(conn, Response{OK: true})
}

func (d *Daemon) handleKeybind(conn *net.UnixConn, req Request) {
	seq, ok := keybinds[req.Key]
	if !ok {
		respond(conn, Response{Error: fmt.Sprintf("unknown keybind: %q", req.Key)})
		return
	}

	ptylog.Debug("keybind", "session_id", req.SessionID, "key", req.Key)

	if d.inner != nil {
		if err := d.inner.Pipe(req.AgentID, req.SessionID, seq); err != nil {
			respond(conn, Response{Error: err.Error()})
			return
		}
		respond(conn, Response{OK: true})
		return
	}

	d.mu.Lock()
	s, ok2 := d.sessions[req.SessionID]
	d.mu.Unlock()
	if !ok2 {
		respond(conn, Response{Error: "session not found"})
		return
	}
	s.mu.Lock()
	_, err := s.ptmx.Write(seq)
	s.mu.Unlock()
	if err != nil {
		respond(conn, Response{Error: err.Error()})
		return
	}
	respond(conn, Response{OK: true})
}

func (d *Daemon) handleStop(conn *net.UnixConn, req Request) {
	ptylog.Debug("stop", "agent_id", req.AgentID, "session_id", req.SessionID, "safe", req.Safe, "force", req.Force)

	// Attachment check: only when the caller opts in via Safe=true.
	// Omitting Safe preserves the original unconditional-kill behaviour.
	if req.Safe {
		if pid, ok := d.attachedPIDs.Load(req.SessionID); ok {
			p := pid.(int)
			if processExists(p) {
				if !req.Force {
					respond(conn, Response{Error: fmt.Sprintf("session is attached (pid %d); use force to override", p)})
					return
				}
				ptylog.Info("force-stopping attached session", "session_id", req.SessionID, "attached_pid", p)
			}
			d.attachedPIDs.Delete(req.SessionID)
		}
	}

	if d.inner != nil {
		if err := d.inner.Stop(req.AgentID, req.SessionID); err != nil {
			if errors.Is(err, internal.ErrNotFound) {
				respond(conn, Response{OK: true}) // idempotent
				return
			}
			ptylog.Error("stop failed", "err", err, "session_id", req.SessionID)
			respond(conn, Response{Error: err.Error()})
			return
		}
		d.attachedPIDs.Delete(req.SessionID)
		ptylog.Info("session stopped", "agent_id", req.AgentID, "session_id", req.SessionID)
		respond(conn, Response{OK: true})
		return
	}

	d.mu.Lock()
	s, ok := d.sessions[req.SessionID]
	if ok {
		delete(d.sessions, req.SessionID)
	}
	d.mu.Unlock()
	if !ok {
		respond(conn, Response{Error: "session not found"})
		return
	}
	_ = s.cmd.Process.Kill()
	_ = s.cmd.Wait()
	_ = s.ptmx.Close()
	respond(conn, Response{OK: true})
}

func (d *Daemon) handleList(conn *net.UnixConn, req Request) {
	ptylog.Debug("list", "agent_id", req.AgentID)

	if d.inner != nil {
		records, err := d.inner.ListSessions(req.AgentID)
		if err != nil {
			ptylog.Error("list sessions failed", "err", err)
			respond(conn, Response{Error: err.Error()})
			return
		}
		entries := make([]SessionEntry, 0, len(records))
		for _, r := range records {
			entries = append(entries, recordToEntry(r))
		}
		ptylog.Debug("list response", "count", len(entries))
		respond(conn, Response{OK: true, Sessions: entries})
		return
	}

	d.mu.Lock()
	entries := make([]SessionEntry, 0, len(d.sessions))
	for id := range d.sessions {
		entries = append(entries, SessionEntry{SessionID: id, Status: "active"})
	}
	d.mu.Unlock()
	respond(conn, Response{OK: true, Sessions: entries})
}

func (d *Daemon) handleGet(conn *net.UnixConn, req Request) {
	ptylog.Debug("get", "agent_id", req.AgentID, "session_id", req.SessionID)

	if d.inner != nil {
		rec, err := d.inner.GetSession(req.AgentID, req.SessionID)
		if err != nil {
			ptylog.Error("get session failed", "err", err, "session_id", req.SessionID)
			respond(conn, Response{Error: err.Error()})
			return
		}
		if rec == nil {
			respond(conn, Response{Error: "session not found"})
			return
		}
		entry := recordToEntry(rec)
		respond(conn, Response{OK: true, SessionID: rec.SessionID, Sessions: []SessionEntry{entry}})
		return
	}

	d.mu.Lock()
	_, ok := d.sessions[req.SessionID]
	d.mu.Unlock()
	if !ok {
		respond(conn, Response{Error: "session not found"})
		return
	}
	respond(conn, Response{OK: true, SessionID: req.SessionID, Sessions: []SessionEntry{
		{SessionID: req.SessionID, Status: "active"},
	}})
}

// handleListAttached returns the client process currently attached to the session.
func (d *Daemon) handleListAttached(conn *net.UnixConn, req Request) {
	ptylog.Debug("list-attached", "session_id", req.SessionID)
	var procs []AttachedProcess
	if pid, ok := d.attachedPIDs.Load(req.SessionID); ok {
		p := pid.(int)
		if processExists(p) {
			procs = []AttachedProcess{{PID: p, Comm: readComm(p)}}
		} else {
			d.attachedPIDs.Delete(req.SessionID)
		}
	}
	ptylog.Debug("list-attached response", "session_id", req.SessionID, "count", len(procs))
	respond(conn, Response{OK: true, SessionID: req.SessionID, Processes: procs})
}

// handleMetaAttached returns 1 if a live client is attached, 0 otherwise.
func (d *Daemon) handleMetaAttached(conn *net.UnixConn, req Request) {
	count := 0
	if pid, ok := d.attachedPIDs.Load(req.SessionID); ok {
		if processExists(pid.(int)) {
			count = 1
		} else {
			d.attachedPIDs.Delete(req.SessionID)
		}
	}
	ptylog.Debug("meta-attached", "session_id", req.SessionID, "count", count)
	respond(conn, Response{OK: true, SessionID: req.SessionID, Count: count})
}

// getMasterFd resolves the PTY master file for the session in the request,
// using inner when available and falling back to the in-memory sessions map.
func (d *Daemon) getMasterFd(req Request) (*os.File, error) {
	if d.inner != nil {
		f, err := d.inner.GetMasterFd(req.AgentID, req.SessionID)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	d.mu.Lock()
	s, ok := d.sessions[req.SessionID]
	d.mu.Unlock()
	if !ok {
		return nil, errors.New("session not found")
	}
	return s.ptmx, nil
}

// peerPID returns the PID of the process on the other end of a Unix socket
// using SO_PEERCRED.
func peerPID(conn *net.UnixConn) (int, error) {
	rc, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var credErr error
	_ = rc.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e != nil {
			credErr = e
			return
		}
		pid = int(cred.Pid)
	})
	return pid, credErr
}

// processExists reports whether the process with the given PID is still alive.
func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

func readComm(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}

func recordToEntry(r *internal.PTYSessionRecord) SessionEntry {
	e := SessionEntry{
		AgentID:   r.AgentID,
		SessionID: r.SessionID,
		Status:    string(r.Status),
	}
	s := r.StartedAt.UTC().Format(time.RFC3339)
	e.StartedAt = &s
	if r.StoppedAt != nil {
		t := r.StoppedAt.UTC().Format(time.RFC3339)
		e.StoppedAt = &t
	}
	return e
}

func respond(conn *net.UnixConn, r Response) {
	_ = json.NewEncoder(conn).Encode(r)
}
