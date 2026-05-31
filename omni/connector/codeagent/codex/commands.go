package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

const (
	Codex codeagent.Provider = "codex"
)

// submitKey is the key sequence codex uses to submit a prompt in interactive mode.
const submitKey = "\r"

// interactiveStdin/Stdout/Stderr are the I/O streams used by Resume.
// They are package-level vars so tests can substitute non-TTY writers.
var (
	interactiveStdin  io.Reader = nil // nil = open /dev/tty at runtime
	interactiveStdout io.Writer = nil
	interactiveStderr io.Writer = nil
)

// ============================================================
// Session lifecycle
// ============================================================

// Create prepares a codex CLI session.
// It verifies the binary and auth, then bootstraps a real codex session by
// running codex briefly and parsing the session ID from its exit line:
//
//	"To continue this session, run codex resume <id>"
//
// The returned ID is the real codex session ID so that Resume can find it.
func (a *codexAgent) Create(p codeagent.CreateSessionParams) (*codeagent.CreateSessionResult, error) {
	a.mu.Lock()
	binPath := a.binPath
	if p.WorkDir != "" {
		a.workDir = p.WorkDir
	}
	if p.Model != "" {
		a.model = p.Model
	}
	if p.RunTime != nil {
		a.sbxRuntime = *p.RunTime
	}
	workDir := a.workDir
	model := a.model
	env := mergeEnv(os.Environ(), p.Envs)
	a.mu.Unlock()

	// Verify the codex binary is reachable by running `codex --version`.
	out, err := captureOutput(workDir, env, binPath, "--version")
	if err != nil {
		return nil, fmt.Errorf("codex: create: binary unreachable: %w", err)
	}
	logger.Debug("Create: codex binary ok", "version", trimSpace(out))

	// Verify authentication via `codex login status` (exit 0 = authenticated).
	authCmd := exec.Command(binPath, "login", "status")
	authCmd.Dir = workDir
	authCmd.Env = env
	if err := authCmd.Run(); err != nil {
		logger.Warn("Create: user not authenticated", "err", err)
		return nil, fmt.Errorf("codex: create: not authenticated — run 'codex login' first")
	}

	// Persist model into .codex/config.toml so interactive sessions inherit it.
	if syncErr := syncModelConfig(workDir, model); syncErr != nil {
		logger.Warn("Create: could not sync model to config", "err", syncErr)
	}

	// Auto-approve tunnel_mcp tool calls so Codex never pauses for MCP approval.
	if mcpErr := ensureMCPApprovalMode("tunnel_mcp"); mcpErr != nil {
		logger.Warn("Create: could not set MCP approval mode", "err", mcpErr)
	}

	// If the caller supplies a prior session ID, attach to it by verifying codex
	// can resume it. This re-uses an existing conversation rather than starting fresh.
	var sessionID string
	if p.SessionID != "" {
		logger.Info("Create: attaching to existing session via resume", "sessionID", p.SessionID)
		if attachErr := verifySessionExists(workDir, binPath, p.SessionID); attachErr != nil {
			return nil, fmt.Errorf("codex: create: attach existing session %q: %w", p.SessionID, attachErr)
		}
		sessionID = p.SessionID
		a.mu.Lock()
		a.sessionID = sessionID
		a.mu.Unlock()
		logger.Info("Create: attached to existing session", "sessionID", sessionID, "workDir", workDir)
		return &codeagent.CreateSessionResult{ID: sessionID, Name: p.Name}, nil
	}

	if sessionID == "" {
		// Bootstrap a real codex session to obtain the actual codex session ID.
		// Runs `codex exec . --json` and parses the thread_id from the first JSON line.
		var bootstrapErr error
		sessionID, bootstrapErr = bootstrapSession(workDir, binPath, model, env)
		if bootstrapErr != nil || sessionID == "" {
			// Non-fatal: fall back to the caller-supplied ID (or a generated one).
			if p.ID != "" {
				sessionID = p.ID
			} else {
				sessionID = generateID()
			}
			logger.Warn("Create: bootstrap session failed, using fallback id", "sessionID", sessionID, "err", bootstrapErr)
		}
	}

	a.mu.Lock()
	a.sessionID = sessionID
	a.mu.Unlock()

	logger.Info("Create: session ready", "id", sessionID, "model", model, "workDir", workDir)
	return &codeagent.CreateSessionResult{ID: sessionID, Name: p.Name}, nil
}

// verifySessionExists runs `codex resume <id> --json` non-interactively to
// confirm the session exists in codex's store. Returns nil if found.
func verifySessionExists(workDir, binPath, sessionID string) error {
	args := []string{"resume", sessionID, "--json", "-C", workDir}
	logger.Debug("verifySessionExists: checking session", "sessionID", sessionID, "args", args)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = workDir

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("verify session: start: %w", err)
	}

	// Give it 200 ms — if the session exists codex will start up; if not it
	// exits immediately with an error message.
	time.Sleep(200 * time.Millisecond)

	output := buf.String()
	if strings.Contains(output, "No saved session") || strings.Contains(output, "not found") {
		cancel()
		_ = cmd.Wait()
		return fmt.Errorf("session %q not found in codex store", sessionID)
	}

	// Session appears valid — kill the process, we don't need it running yet.
	cancel()
	_ = cmd.Wait()
	return nil
}

// bootstrapSession runs `codex exec "." --json` briefly to register a real
// codex session and capture its thread_id from the first JSON line:
//
//	{"type":"thread.started","thread_id":"<id>"}
//
// The process is killed as soon as the thread_id is found so we don't wait
// for the full exec to finish.
func bootstrapSession(workDir, binPath, model string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	args := []string{"exec", ".", "--json", "--skip-git-repo-check", "-C", workDir}
	logger.Info("bootstrapSession: running command", "bin", binPath, "args", args)

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stderr = io.Discard

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("bootstrap: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("bootstrap: start: %w", err)
	}

	// Read lines as they arrive — no polling, no buffering delay.
	// Stop as soon as thread_id is found.
	found := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if id := parseBootstrapSessionID(scanner.Text()); id != "" {
				found <- id
				return
			}
		}
		close(found)
	}()

	var id string
	select {
	case id = <-found:
	case <-ctx.Done():
	}

	cancel()
	_ = cmd.Wait()

	logger.Debug("bootstrapSession: completed", "sessionID", id)
	return id, nil
}

// parseBootstrapSessionID reads the thread_id from codex --json exec output:
//
//	{"type":"thread.started","thread_id":"<id>"}
func parseBootstrapSessionID(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, `"thread.started"`) && strings.Contains(line, `"thread_id"`) {
			// Fast path: extract value between `"thread_id":"` and next `"`
			const key = `"thread_id":"`
			idx := strings.Index(line, key)
			if idx == -1 {
				continue
			}
			rest := line[idx+len(key):]
			end := strings.Index(rest, `"`)
			if end > 0 {
				return rest[:end]
			}
		}
	}
	return ""
}

type ptyMetaAttached interface {
	MetaAttached(sessionID string) (int, error)
}

func (a *codexAgent) Resume(p codeagent.ResumeSessionParams) (*codeagent.ResumeSessionResult, error) {
	ctx := p.Context
	if ctx == nil {
		ctx = context.Background()
	}

	// Block if a PTY session is already live.
	a.mu.RLock()
	live := a.masterPTY != nil || a.writeCh != nil
	a.mu.RUnlock()
	if live {
		return nil, errors.New("codex: PTY session already active; stop it before resuming")
	}

	a.mu.RLock()
	binPath := a.binPath
	workDir := a.workDir
	rt := a.sbxRuntime
	ptyClient := a.ptyClient
	currentSessionID := a.sessionID
	a.mu.RUnlock()
	if p.RunTime != nil {
		rt = *p.RunTime
	}
	env := mergeEnv(os.Environ(), p.Envs)

	resolvedSessionID := strings.TrimSpace(p.ID)
	if p.SessionID != "" {
		resolvedSessionID = strings.TrimSpace(p.SessionID)
	}
	if resolvedSessionID == "" {
		resolvedSessionID = strings.TrimSpace(currentSessionID)
	}

	cmdName := "resume"
	if p.ForkSession {
		cmdName = "fork"
	}

	args := []string{cmdName, "-C", workDir}
	if resolvedSessionID != "" {
		args = append(args, resolvedSessionID)
	} else {
		args = append(args, "--last")
		resolvedSessionID = "last"
	}

	logger.Info("Resume: running command", "bin", binPath, "args", args)

	if rt != nil {
		if err := rt.Command(binPath, args); err != nil {
			return nil, fmt.Errorf("codex: resume: sandbox command: %w", err)
		}
		pid := runtimePID(rt)
		logger.Info("Resume: interactive sandbox session completed", "pid", pid, "sessionID", resolvedSessionID, "fork", p.ForkSession)
		return &codeagent.ResumeSessionResult{ProcessID: pid, SessionID: resolvedSessionID}, nil
	}

	if ptyClient != nil {
		var (
			info *codeagent.PTYTerminalInfo
			err  error
		)
		if resolvedSessionID == "last" {
			infos, listErr := ptyClient.List(string(Codex))
			if listErr != nil {
				return nil, fmt.Errorf("codex: resume: pty list: %w", listErr)
			}
			active := make([]*codeagent.PTYTerminalInfo, 0, len(infos))
			for _, candidate := range infos {
				if candidate != nil && candidate.Status == "active" {
					active = append(active, candidate)
				}
			}
			if len(active) == 1 {
				info = active[0]
				resolvedSessionID = info.SessionID
			}
		} else {
			info, err = ptyClient.Get(string(Codex), resolvedSessionID)
			if err != nil {
				return nil, fmt.Errorf("codex: resume: pty get %q: %w", resolvedSessionID, err)
			}
		}
		if meta, ok := ptyClient.(ptyMetaAttached); ok {
			count, err := meta.MetaAttached(resolvedSessionID)
			if err != nil {
				logger.Warn("Resume: PTY attached count unavailable", "sessionID", resolvedSessionID, "err", err)
			} else if count > 1 {
				return nil, errors.New("codex: resume: PTY session already has an interactive user attached")
			}
		}
		command := append([]string{binPath}, args...)
		started := false
		if info == nil || info.Status != "active" {
			if err := ptyClient.Start(resolvedSessionID, command, env, workDir, submitKey); err != nil {
				return nil, fmt.Errorf("codex: resume: pty start: %w", err)
			}
			started = true
		}
		a.mu.Lock()
		a.sessionID = resolvedSessionID
		a.writeCh = make(chan []byte, 1)
		a.activeCmd = nil
		a.mu.Unlock()
		if p.Detached {
			logger.Info("Resume: leaving PTY daemon session detached", "sessionID", resolvedSessionID)
			return &codeagent.ResumeSessionResult{ProcessID: "", SessionID: resolvedSessionID}, nil
		}
		done := make(chan error, 1)
		go func() {
			defer close(done)
			if err := ptyClient.Attach(ctx, resolvedSessionID); err != nil {
				err = fmt.Errorf("codex: resume: pty attach: %w", err)
				logger.Warn("Resume: PTY attach ended with error", "sessionID", resolvedSessionID, "err", err)
				done <- err
			} else {
				logger.Info("Resume: PTY daemon session detached", "sessionID", resolvedSessionID)
				done <- nil
			}
			a.mu.Lock()
			a.masterPTY = nil
			if a.writeCh != nil {
				close(a.writeCh)
			}
			a.writeCh = nil
			a.mu.Unlock()
			if started {
				_ = ptyClient.Stop(resolvedSessionID)
			}
			logger.Debug("Resume: PTY session terminated", "sessionID", resolvedSessionID)
		}()
		if started {
			logger.Info("Resume: PTY daemon session started", "sessionID", resolvedSessionID)
		} else {
			logger.Info("Resume: attached to active PTY daemon session", "sessionID", resolvedSessionID)
		}
		logger.Info("Resume: attaching PTY daemon session", "sessionID", resolvedSessionID)
		return &codeagent.ResumeSessionResult{ProcessID: "", SessionID: resolvedSessionID, Done: done}, nil
	}

	pid, _ := a.attachAndRun(ctx, binPath, workDir, args, env)
	// TODO: re-enable new-session fallback once bootstrap reliably returns a real session ID
	// if runErr != nil {
	// 	logger.Warn("Resume: session not found, falling back to new session", "sessionID", resolvedSessionID, "err", runErr)
	// 	newArgs := []string{"-C", workDir}
	// 	logger.Info("Resume: running command", "bin", binPath, "args", newArgs)
	// 	pid, _ = a.attachAndRun(binPath, workDir, newArgs)
	// 	resolvedSessionID = "new"
	// }

	return &codeagent.ResumeSessionResult{ProcessID: pid, SessionID: resolvedSessionID}, nil
}

// attachAndRun starts binPath with args attached to the terminal and blocks
// until the process exits. Returns the pid string and the exit error (nil = clean exit).
func (a *codexAgent) attachAndRun(ctx context.Context, binPath, workDir string, args []string, env []string) (pid string, err error) {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = workDir
	cmd.Env = env

	// Prefer injectable streams (set by tests). In production, open /dev/tty
	// directly so the child gets a proper controlling terminal for raw mode.
	// Pipe-like fds from os.Stdin/Stdout cause EIO on setRawMode.
	if interactiveStdin != nil || interactiveStdout != nil || interactiveStderr != nil {
		cmd.Stdin = interactiveStdin
		cmd.Stdout = interactiveStdout
		cmd.Stderr = interactiveStderr
	} else {
		tty, ttyErr := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if ttyErr == nil {
			defer tty.Close()
			cmd.Stdin = tty
			cmd.Stdout = tty
			cmd.Stderr = tty
		} else {
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		}
	}

	if startErr := cmd.Start(); startErr != nil {
		return "", fmt.Errorf("codex: start process: %w", startErr)
	}

	pid = fmt.Sprintf("%d", cmd.Process.Pid)

	a.mu.Lock()
	a.activeCmd = cmd
	a.mu.Unlock()

	logger.Info("Resume: interactive session started", "pid", pid, "args", args)

	// Block until the interactive session ends. This keeps the tty fd open
	// for the full duration and prevents the caller from racing with the child.
	err = cmd.Wait()
	return pid, err
}

// ============================================================
// ExecInSession
// ============================================================

// ExecInSession writes a prompt into an active interactive PTY session.
func (a *codexAgent) ExecInSession(p codeagent.ExecInSessionParams) (*codeagent.ExecInSessionResult, error) {
	a.mu.RLock()
	client := a.ptyClient
	agentID := a.sessionID
	a.mu.RUnlock()

	sid := p.SessionID
	if sid == "" {
		sid = agentID
	}
	if sid == "" {
		return nil, fmt.Errorf("codex: ExecInSession: no session ID")
	}

	if client == nil {
		return nil, fmt.Errorf("codex: ExecInSession: no active PTY session")
	}
	if err := client.Exec(sid, p.Prompt); err != nil {
		return nil, fmt.Errorf("codex: ExecInSession: session not live: %w", err)
	}
	logger.Info("ExecInSession: prompt delegated", "agentID", agentID, "sessionID", sid, "promptLen", len(p.Prompt))
	return &codeagent.ExecInSessionResult{SessionID: sid}, nil
}

func (a *codexAgent) List(p codeagent.ListSessionsParams) (*codeagent.ListSessionsResult, error) {
	a.mu.RLock()
	defaultWorkDir := a.workDir
	defaultModel := a.model
	a.mu.RUnlock()

	workDir := strings.TrimSpace(p.WorkDir)
	if workDir == "" {
		workDir = defaultWorkDir
	}

	indexEntries, err := readSessionIndex()
	if err != nil {
		return nil, fmt.Errorf("codex: list sessions: %w", err)
	}

	sessions := make([]*codeagent.Session, 0, len(indexEntries))
	for _, entry := range indexEntries {
		meta, metaErr := readSessionMeta(entry.ID)
		if metaErr != nil {
			logger.Debug("List: skipping session with unreadable metadata", "sessionID", entry.ID, "err", metaErr)
			continue
		}
		if workDir != "" && meta.Cwd != "" && meta.Cwd != workDir {
			continue
		}

		name := strings.TrimSpace(entry.ThreadName)
		if name == "" {
			name = filepath.Base(meta.FilePath)
		}

		sessions = append(sessions, &codeagent.Session{
			ID:       entry.ID,
			Name:     name,
			Provider: Codex,
			Model:    defaultModel,
			WorkDir:  meta.Cwd,
		})
	}

	logger.Info("List: sessions resolved from codex session store", "count", len(sessions), "workDir", workDir)
	return &codeagent.ListSessionsResult{Sessions: sessions}, nil
}

func (a *codexAgent) Delete(p codeagent.DeleteSessionParams) (*codeagent.DeleteSessionResult, error) {
	sessionID := strings.TrimSpace(p.ID)
	if sessionID == "" {
		return nil, errors.New("codex: delete session: empty session id")
	}

	meta, err := readSessionMeta(sessionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logger.Warn("Delete: session not found", "sessionID", sessionID)
			return &codeagent.DeleteSessionResult{Deleted: false}, nil
		}
		return nil, fmt.Errorf("codex: delete session %q: %w", sessionID, err)
	}

	if err := os.Remove(meta.FilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("codex: delete session %q file: %w", sessionID, err)
	}
	if err := removeSessionIndexEntry(sessionID); err != nil {
		return nil, fmt.Errorf("codex: delete session %q index: %w", sessionID, err)
	}

	a.mu.Lock()
	if a.sessionID == sessionID {
		a.sessionID = ""
	}
	a.mu.Unlock()

	logger.Info("Delete: session removed from codex session store", "sessionID", sessionID)
	return &codeagent.DeleteSessionResult{Deleted: true}, nil
}

func (a *codexAgent) Stop() {
	a.mu.Lock()
	cmd := a.activeCmd
	writeChLive := a.writeCh != nil
	client := a.ptyClient
	sessionID := a.sessionID
	a.activeCmd = nil
	if a.writeCh != nil {
		close(a.writeCh)
		a.writeCh = nil
	}
	a.masterPTY = nil
	a.mu.Unlock()

	if writeChLive && client != nil {
		if err := client.Stop(sessionID); err != nil {
			logger.Warn("Stop: failed to stop PTY daemon session", "sessionID", sessionID, "err", err)
		} else {
			logger.Info("Stop: PTY daemon session terminated", "sessionID", sessionID)
		}
		return
	}
	if cmd == nil || cmd.Process == nil {
		logger.Info("Stop: no active codex process")
		return
	}
	if err := cmd.Process.Kill(); err != nil {
		logger.Warn("Stop: failed to kill active process", "err", err)
		return
	}
	logger.Info("Stop: active codex process terminated")
}

// ============================================================
// GetSessionConfig
// ============================================================

func (a *codexAgent) GetSessionConfig(_ codeagent.GetSessionConfigParams) (*codeagent.GetSessionConfigResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return &codeagent.GetSessionConfigResult{
		Model:          codeagent.Model{Provider: Codex, Model: a.model},
		PermissionMode: codeagent.PermissionDefault,
		WorkDir:        a.workDir,
	}, nil
}

// ============================================================
// Exec
// ============================================================

func (a *codexAgent) Exec(p codeagent.ExecuteParams) (*codeagent.ExecuteResult, error) {
	a.mu.RLock()
	binPath := a.binPath
	workDir := a.workDir
	model := a.model
	sbx := a.sbx
	rt := a.sbxRuntime
	a.mu.RUnlock()

	args := buildExecArgs(p.Prompt, model, p.OutputFormat, p.MaxTurns, sbx)
	logger.Debug("Exec", "workDir", workDir, "args", args)

	out, err := execOutput(workDir, rt, binPath, args...)
	if err != nil {
		return nil, err
	}

	response := strings.TrimSpace(string(out))
	logger.Debug("Exec completed", "responseLen", len(response))

	return &codeagent.ExecuteResult{
		PromptID:   p.PromptId,
		Response:   response,
		StopReason: "stop",
	}, nil
}

// ============================================================
// Stream
// ============================================================

func (a *codexAgent) Stream(p codeagent.StreamParams) (*codeagent.StreamResult, error) {
	a.mu.RLock()
	binPath := a.binPath
	workDir := a.workDir
	model := a.model
	sbx := a.sbx
	rt := a.sbxRuntime
	a.mu.RUnlock()

	args := buildStreamArgs(p.Prompt, model, p.MaxTurns, sbx)
	logger.Debug("Stream", "workDir", workDir, "args", args)

	ch := make(chan codeagent.StreamEvent, 32)
	if rt != nil {
		proc, err := rt.Start(binPath, args)
		if err != nil {
			return nil, fmt.Errorf("codex stream: sandbox start: %w", err)
		}
		go func() {
			defer close(ch)
			res, waitErr := proc.Wait()
			if waitErr != nil {
				msg := runtimeErrorf("codex stream", res, waitErr).Error()
				logger.Error("Stream: sandbox process exited with error", "err", msg)
				ch <- codeagent.StreamEvent{Type: "stop", Done: true, Content: msg}
				return
			}
			scanner := bufio.NewScanner(strings.NewReader(res.Stdout))
			for scanner.Scan() {
				line := scanner.Text()
				if line == "" {
					continue
				}
				ev := parseCodexLine(line)
				ch <- ev
				if ev.Done {
					return
				}
			}
			ch <- codeagent.StreamEvent{Type: "stop", Done: true}
			logger.Debug("Stream completed via sandbox runtime")
		}()
		return &codeagent.StreamResult{Events: ch}, nil
	}

	cmd := exec.Command(binPath, args...)
	cmd.Dir = workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex stream: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex stream: start process: %w", err)
	}

	go func() {
		defer close(ch)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			ev := parseCodexLine(line)
			ch <- ev
			if ev.Done {
				return
			}
		}
		if err := cmd.Wait(); err != nil {
			msg := wrapExitError("codex stream", err).Error()
			logger.Error("Stream: process exited with error", "err", msg)
			ch <- codeagent.StreamEvent{Type: "stop", Done: true, Content: msg}
			return
		}
		ch <- codeagent.StreamEvent{Type: "stop", Done: true}
		logger.Debug("Stream completed")
	}()

	return &codeagent.StreamResult{Events: ch}, nil
}

// ============================================================
// Arg builders
// ============================================================

func buildExecArgs(prompt, model string, format codeagent.OutputFormat, maxTurns int, sbx *sandbox.Config) []string {
	args := []string{"exec", prompt}
	if model != "" {
		args = append(args, "-m", model)
	}
	if format == codeagent.OutputFormatJSON || format == codeagent.OutputFormatStreamJSON {
		args = append(args, "--json")
	}
	if maxTurns > 0 {
		args = append(args, fmt.Sprintf("--max-turns=%d", maxTurns))
	}
	if sf := sandboxFlagValue(sbx); sf != "" {
		args = append(args, "--sandbox", sf)
	}
	return args
}

func buildStreamArgs(prompt, model string, maxTurns int, sbx *sandbox.Config) []string {
	args := []string{"exec", prompt, "--json"}
	if model != "" {
		args = append(args, "-m", model)
	}
	if maxTurns > 0 {
		args = append(args, fmt.Sprintf("--max-turns=%d", maxTurns))
	}
	if sf := sandboxFlagValue(sbx); sf != "" {
		args = append(args, "--sandbox", sf)
	}
	return args
}

// ============================================================
// Helpers
// ============================================================

func wrapExitError(op string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("%s: exit %d: %s", op, exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
	}
	return fmt.Errorf("%s: %w", op, err)
}

// captureOutput runs name with args from dir. env overrides the process
// environment when non-nil; pass nil to inherit the current process env.
func captureOutput(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.Output()
	return string(out), err
}

func mergeEnv(base, overrides []string) []string {
	if len(overrides) == 0 {
		return append([]string(nil), base...)
	}
	merged := append([]string(nil), base...)
	index := make(map[string]int, len(merged))
	for i, entry := range merged {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			index[key] = i
		}
	}
	for _, entry := range overrides {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			merged = append(merged, entry)
			continue
		}
		if i, exists := index[key]; exists {
			merged[i] = entry
			continue
		}
		index[key] = len(merged)
		merged = append(merged, entry)
	}
	return merged
}

func execOutput(workDir string, rt sandbox.SandboxRuntime, name string, args ...string) (string, error) {
	if rt == nil {
		cmd := exec.Command(name, args...)
		cmd.Dir = workDir
		out, err := cmd.Output()
		if err != nil {
			return "", wrapExitError("codex exec", err)
		}
		return string(out), nil
	}
	res, err := rt.Capture(name, args)
	if err != nil {
		return "", runtimeErrorf("codex exec", res, err)
	}
	return res.Stdout, nil
}

func runtimeErrorf(op string, res *sandbox.ExecutionResult, err error) error {
	if res != nil {
		stderr := strings.TrimSpace(res.Stderr)
		if stderr == "" {
			stderr = strings.TrimSpace(res.Stdout)
		}
		return fmt.Errorf("%s: exit %d: %s", op, res.ExitCode, stderr)
	}
	return fmt.Errorf("%s: %w", op, err)
}

func runtimePID(rt sandbox.SandboxRuntime) string {
	if rt == nil || rt.Sandbox() == nil || rt.Sandbox().State == nil {
		return ""
	}
	return rt.Sandbox().State.PID
}

func trimSpace(s string) string {
	return strings.TrimSpace(s)
}

type sessionIndexEntry struct {
	ID         string `json:"id"`
	ThreadName string `json:"thread_name"`
	UpdatedAt  string `json:"updated_at"`
}

type sessionMetaLine struct {
	Type    string             `json:"type"`
	Payload sessionMetaPayload `json:"payload"`
}

type sessionMetaPayload struct {
	ID  string `json:"id"`
	Cwd string `json:"cwd"`
}

type sessionMeta struct {
	Cwd      string
	FilePath string
}

func readSessionIndex() ([]sessionIndexEntry, error) {
	sessionDir, err := globalCodexDir()
	if err != nil {
		return nil, err
	}
	indexPath := filepath.Join(sessionDir, "session_index.jsonl")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var entries []sessionIndexEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry sessionIndexEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			logger.Debug("readSessionIndex: skipping malformed line", "err", err)
			continue
		}
		if strings.TrimSpace(entry.ID) == "" {
			continue
		}
		entries = append(entries, entry)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		ti, errI := time.Parse(time.RFC3339Nano, entries[i].UpdatedAt)
		tj, errJ := time.Parse(time.RFC3339Nano, entries[j].UpdatedAt)
		if errI == nil && errJ == nil {
			return ti.After(tj)
		}
		return entries[i].UpdatedAt > entries[j].UpdatedAt
	})

	return entries, nil
}

func readSessionMeta(sessionID string) (*sessionMeta, error) {
	filePath, err := findSessionFile(sessionID)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		return &sessionMeta{FilePath: filePath}, nil
	}

	var line sessionMetaLine
	if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
		return nil, fmt.Errorf("parse session meta %s: %w", filePath, err)
	}
	return &sessionMeta{
		Cwd:      line.Payload.Cwd,
		FilePath: filePath,
	}, nil
}

func findSessionFile(sessionID string) (string, error) {
	codexDir, err := globalCodexDir()
	if err != nil {
		return "", err
	}

	searchRoot := filepath.Join(codexDir, "sessions")
	var match string
	walkErr := filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.Contains(filepath.Base(path), sessionID) {
			match = path
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return "", walkErr
	}
	if match == "" {
		return "", os.ErrNotExist
	}
	return match, nil
}

func removeSessionIndexEntry(sessionID string) error {
	codexDir, err := globalCodexDir()
	if err != nil {
		return err
	}

	indexPath := filepath.Join(codexDir, "session_index.jsonl")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	lines := strings.Split(string(data), "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var entry sessionIndexEntry
		if err := json.Unmarshal([]byte(trimmed), &entry); err != nil || entry.ID != sessionID {
			filtered = append(filtered, trimmed)
		}
	}

	content := strings.Join(filtered, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(indexPath, []byte(content), 0o600)
}
