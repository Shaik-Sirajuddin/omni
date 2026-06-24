package claude

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

const (
	Claude codeagent.Provider = "claude"
)

// createSeedTimeout is the maximum time allowed for the bootstrap seed call
// (`claude -p hello`). If no deadline is already set on p.Context, Create wraps
// the seed with this timeout so an unauthenticated or hung CLI cannot freeze the
// operator indefinitely.
const createSeedTimeout = 120 * time.Second

// submitKey is sent by ExecInSession after the prompt to trigger submission.
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

// Create verifies the claude binary is reachable and the user is authenticated,
// then stores the resolved session parameters for subsequent Exec/Stream calls.
func (a *claudeAgent) Create(p codeagent.CreateSessionParams) (*codeagent.CreateSessionResult, error) {
	a.mu.Lock()
	if p.WorkDir != "" {
		a.workDir = p.WorkDir
	}
	if p.Model != "" {
		a.model = p.Model
	}
	if p.PermissionMode != "" {
		a.permMode = p.PermissionMode
	}
	if p.SystemPrompt != "" {
		a.systemPrompt = p.SystemPrompt
	}
	id := p.ID
	if id == "" {
		id = generateID()
	}
	// If a pre-existing Claude session ID is provided, attach to it directly
	// instead of seeding a new one.
	if p.SessionID != "" {
		a.sessionID = p.SessionID
	} else {
		a.sessionID = id
	}
	// if p.RunTime != nil { a.sbxRuntime = *p.RunTime } // sandbox disabled
	workDir := a.workDir
	sessionID := a.sessionID
	binPath := a.binPath
	env := mergeEnv(os.Environ(), p.Envs)
	a.mu.Unlock()

	// Derive a context for the seed call. If the caller provided one with a
	// deadline, use it as-is; otherwise apply a bounded default so an
	// unauthenticated or hung CLI cannot freeze the operator indefinitely.
	ctx := p.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancelSeed context.CancelFunc
		ctx, cancelSeed = context.WithTimeout(ctx, createSeedTimeout)
		defer cancelSeed()
	}

	// Verify binary.
	out, err := captureOutputEnv(workDir, env, binPath, "--version")
	if err != nil {
		return nil, fmt.Errorf("claude: create: binary unreachable: %w", err)
	}
	logger.Debug("Create: claude binary ok", "version", trimSpace(out))

	// Verify authentication.
	authCmd := exec.Command(binPath, "auth", "status")
	authCmd.Dir = workDir
	authCmd.Env = env
	if err := authCmd.Run(); err != nil {
		logger.Warn("Create: user not authenticated", "err", err)
		return nil, fmt.Errorf("claude: create: not authenticated — run 'claude auth login' first")
	}

	// When attaching to an existing session, skip seeding — the conversation
	// already exists in Claude's store and a seed call would corrupt it.
	if p.SessionID != "" {
		logger.Info("Create: attached to existing session", "id", id, "sessionID", sessionID, "workDir", workDir)
		return &codeagent.CreateSessionResult{ID: id, Name: p.Name}, nil
	}

	// Seed the session into Claude's session store by running a minimal
	// print-mode call with --session-id. Without this, `claude -r <id>`
	// fails with "No conversation found" because Claude only persists a
	// session after at least one print-mode exchange.
	// rt := a.sbxRuntime // sandbox disabled
	var rt sandbox.SandboxRuntime // sandbox disabled — always nil

	seedArgs := []string{
		"-p", "hello",
		"--session-id", sessionID,
		"--output-format", "json",
		"--max-turns", "1",
	}
	// Only pin --model when the caller explicitly requested one; otherwise let
	// Claude pick its configured default instead of forcing our stored model.
	if p.Model != "" {
		seedArgs = append(seedArgs, "--model", p.Model)
	}
	seedArgs = append(seedArgs, p.ExtraArgs...)
	seedOut, seedErr := execOutputEnvContext(ctx, workDir, rt, env, binPath, seedArgs...)
	if seedErr != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("claude: create: seed session timed out after %s (is the CLI authenticated?): %w", createSeedTimeout, ctx.Err())
		}
		return nil, fmt.Errorf("claude: create: seed session: %w", seedErr)
	}
	logger.Debug("Create: session seeded", "id", id, "output", trimSpace(seedOut))

	logger.Info("Create: session ready", "id", id, "workDir", workDir)
	return &codeagent.CreateSessionResult{ID: id, Name: p.Name}, nil
}

type ptyMetaAttached interface {
	MetaAttached(sessionID string) (int, error)
}

// ptyPIDGetter is optionally satisfied by PTY clients that can report the OS
// process ID of a managed session. codexPTYAdapter implements this once the
// daemon's "get" response includes the pid field (see collab instruction:
// memory/team/entry/instructions/ptydaemon/pty_session_pid.md).
type ptyPIDGetter interface {
	SessionPID(agentID, sessionID string) (int, error)
}

// Resume launches an interactive claude session via `claude -r <id>`.
func (a *claudeAgent) Resume(p codeagent.ResumeSessionParams) (*codeagent.ResumeSessionResult, error) {
	ctx := p.Context
	if ctx == nil {
		ctx = context.Background()
	}

	a.mu.Lock()
	binPath := a.binPath
	workDir := a.workDir
	// if p.RunTime != nil { a.sbxRuntime = *p.RunTime } // sandbox disabled
	// rt := a.sbxRuntime // sandbox disabled
	var rt sandbox.SandboxRuntime // sandbox disabled — always nil
	client := a.ptyClient
	currentSessionID := a.sessionID
	env := mergeEnv(os.Environ(), p.Envs)
	a.mu.Unlock()

	// Prefer the explicit Claude session ID when provided; fall back to p.ID.
	resumeID := p.ID
	if p.SessionID != "" {
		resumeID = p.SessionID
	}
	if resumeID == "" {
		resumeID = currentSessionID
	}
	if resumeID == "" {
		return nil, errors.New("claude: resume: empty session id")
	}

	args := []string{"-r", resumeID}
	if p.ForkSession {
		args = append(args, "--fork-session")
	}
	args = append(args, p.ExtraArgs...)

	// sandbox disabled:
	// if rt != nil {
	//     if err := rt.Command(binPath, args); err != nil { return nil, fmt.Errorf(...) }
	//     return &codeagent.ResumeSessionResult{ProcessID: runtimePID(rt), SessionID: resumeID}, nil
	// }
	_ = rt

	if client != nil {
		info, err := client.Get("", resumeID)
		if err != nil {
			return nil, fmt.Errorf("claude: resume: pty get %q: %w", resumeID, err)
		}
		if meta, ok := client.(ptyMetaAttached); ok {
			count, err := meta.MetaAttached(resumeID)
			if err != nil {
				logger.Warn("Resume: PTY attached count unavailable", "sessionID", resumeID, "err", err)
			} else if count >= 1 {
				return nil, errors.New("claude: resume: PTY session already has an interactive user attached")
			}
		}
		command := append([]string{binPath}, args...)
		started := false
		if info == nil || info.Status != "active" {
			if err := client.Start(resumeID, command, env, workDir, submitKey); err != nil {
				return nil, fmt.Errorf("claude: resume: pty start: %w", err)
			}
			started = true
		}

		// Capture the session process ID so the operator's registerPTYSession can
		// call adopt, establishing the agentID→sessionID→PID mapping in the daemon.
		// Requires codexPTYAdapter to implement ptyPIDGetter — see collab instruction:
		// memory/team/entry/instructions/ptydaemon/pty_session_pid.md
		var processID string
		if pg, ok := client.(ptyPIDGetter); ok {
			if pid, err := pg.SessionPID("", resumeID); err == nil && pid > 0 {
				processID = fmt.Sprintf("%d", pid)
				logger.Debug("Resume: captured session PID", "sessionID", resumeID, "pid", processID)
			}
		}

		a.mu.Lock()
		a.sessionID = resumeID
		a.mu.Unlock()
		if started {
			logger.Info("Resume: PTY daemon session started", "sessionID", resumeID, "pid", processID)
		} else {
			logger.Info("Resume: reusing active PTY daemon session", "sessionID", resumeID)
		}
		if p.Detached {
			logger.Info("Resume: leaving PTY daemon session detached", "sessionID", resumeID)
			return &codeagent.ResumeSessionResult{ProcessID: processID, SessionID: resumeID}, nil
		}
		logger.Info("Resume: attaching PTY daemon session", "sessionID", resumeID)
		done := make(chan error, 1)
		go func() {
			defer close(done)
			err := client.Attach(ctx, resumeID)
			if err != nil {
				done <- fmt.Errorf("claude: resume: pty attach: %w", err)
				return
			}
			logger.Info("Resume: PTY daemon session detached", "sessionID", resumeID)
			done <- nil
		}()
		return &codeagent.ResumeSessionResult{ProcessID: processID, SessionID: resumeID, Done: done}, nil
	}

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = workDir
	cmd.Env = env

	// Blocking mode: open /dev/tty directly so the child gets a proper
	// controlling terminal for raw mode. Pipe-like fds cause EIO on setRawMode.
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

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude: resume: start process: %w", err)
	}

	pid := fmt.Sprintf("%d", cmd.Process.Pid)
	logger.Info("Resume: interactive session started", "pid", pid, "sessionID", resumeID)

	// Block until the interactive session ends. This keeps the tty fd open
	// for the full duration and prevents the caller from racing with the child.
	_ = cmd.Wait()

	return &codeagent.ResumeSessionResult{ProcessID: pid, SessionID: resumeID}, nil
}

// List is not supported by the Claude CLI.
func (a *claudeAgent) List(_ codeagent.ListSessionsParams) (*codeagent.ListSessionsResult, error) {
	logger.Warn("List: claude CLI has no session list command")
	return &codeagent.ListSessionsResult{Sessions: nil}, nil
}

// Delete is not supported by the Claude CLI.
func (a *claudeAgent) Delete(_ codeagent.DeleteSessionParams) (*codeagent.DeleteSessionResult, error) {
	logger.Warn("Delete: claude CLI has no session delete command")
	return &codeagent.DeleteSessionResult{Deleted: false}, nil
}

func (a *claudeAgent) Stop() {
	logger.Info("Stop: no-op for non-interactive claude sessions")
}

// ============================================================
// ExecInSession
// ============================================================

// ExecInSession sends a prompt into an active interactive PTY session.
// It is fire-and-forget: the prompt is piped into the PTY stdin and the call
// returns immediately without waiting for a response.
func (a *claudeAgent) ExecInSession(p codeagent.ExecInSessionParams) (*codeagent.ExecInSessionResult, error) {
	a.mu.RLock()
	client := a.ptyClient
	sessionID := a.sessionID
	a.mu.RUnlock()

	if p.SessionID != "" {
		sessionID = p.SessionID
	}
	if sessionID == "" {
		return nil, fmt.Errorf("claude: ExecInSession: no session ID")
	}
	if client == nil {
		return nil, fmt.Errorf("claude: ExecInSession: no active PTY session")
	}
	if err := client.Exec(sessionID, p.Prompt); err != nil {
		return nil, fmt.Errorf("claude: ExecInSession: session not live: %w", err)
	}

	logger.Debug("ExecInSession: prompt delegated", "sessionID", sessionID, "promptLen", len(p.Prompt))
	return &codeagent.ExecInSessionResult{SessionID: sessionID}, nil
}

// ============================================================
// GetSessionConfig
// ============================================================

func (a *claudeAgent) GetSessionConfig(_ codeagent.GetSessionConfigParams) (*codeagent.GetSessionConfigResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return &codeagent.GetSessionConfigResult{
		Model:          codeagent.Model{Provider: Claude, Model: a.model},
		PermissionMode: a.permMode,
		WorkDir:        a.workDir,
		SystemPrompt:   a.systemPrompt,
	}, nil
}

// ============================================================
// Exec
// ============================================================

func (a *claudeAgent) Exec(p codeagent.ExecuteParams) (*codeagent.ExecuteResult, error) {
	a.mu.RLock()
	binPath := a.binPath
	workDir := a.workDir
	model := a.model
	permMode := a.permMode
	systemPrompt := a.systemPrompt
	sessionID := a.sessionID
	// rt := a.sbxRuntime // sandbox disabled
	a.mu.RUnlock()

	args := buildExecArgs(p.Prompt, model, permMode, systemPrompt, sessionID, p.OutputFormat, p.MaxTurns)
	logger.Debug("Exec", "workDir", workDir, "args", args)

	response, err := execOutput(workDir, nil, binPath, args...)
	if err != nil {
		return nil, err
	}
	logger.Debug("Exec completed", "responseLen", len(response))

	return &codeagent.ExecuteResult{
		PromptID:   p.PromptId,
		SessionID:  sessionID,
		Response:   response,
		StopReason: "stop",
	}, nil
}

// ============================================================
// Stream
// ============================================================

func (a *claudeAgent) Stream(p codeagent.StreamParams) (*codeagent.StreamResult, error) {
	a.mu.RLock()
	binPath := a.binPath
	workDir := a.workDir
	model := a.model
	permMode := a.permMode
	systemPrompt := a.systemPrompt
	sessionID := a.sessionID
	// rt := a.sbxRuntime // sandbox disabled
	a.mu.RUnlock()

	args := buildStreamArgs(p.Prompt, model, permMode, systemPrompt, sessionID, p.MaxTurns)
	logger.Debug("Stream", "workDir", workDir, "args", args)

	ch := make(chan codeagent.StreamEvent, 32)
	// sandbox disabled:
	// if rt != nil { proc, err := rt.Start(binPath, args); ... return &StreamResult{...} }

	cmd := localCommand(workDir, binPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude stream: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude stream: start process: %w", err)
	}
	go func() {
		defer close(ch)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			ev := parseClaudeLine(line)
			ch <- ev
			if ev.Done {
				return
			}
		}
		if err := cmd.Wait(); err != nil {
			msg := wrapExitError("claude stream", err).Error()
			logger.Error("Stream: process exited with error", "err", msg)
			ch <- codeagent.StreamEvent{Type: "stop", Done: true, Content: msg}
			return
		}
		ch <- codeagent.StreamEvent{Type: "stop", Done: true}
		logger.Debug("Stream completed")
	}()

	return &codeagent.StreamResult{Events: ch, SessionID: sessionID}, nil
}

// ============================================================
// PTY helpers
// ============================================================

// ============================================================
// Arg builders
// ============================================================

func buildExecArgs(prompt, model string, permMode codeagent.PermissionMode, systemPrompt, sessionID string, format codeagent.OutputFormat, maxTurns int) []string {
	args := []string{"-p", prompt, "--model", model}

	switch format {
	case codeagent.OutputFormatJSON:
		args = append(args, "--output-format", "json")
	case codeagent.OutputFormatStreamJSON:
		args = append(args, "--output-format", "stream-json")
	default:
		args = append(args, "--output-format", "text")
	}

	if maxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", maxTurns))
	}
	if permMode != "" && permMode != codeagent.PermissionDefault {
		args = append(args, "--permission-mode", string(permMode))
	}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	if sessionID != "" {
		args = append(args, "--session-id", sessionID)
	}
	return args
}

func buildStreamArgs(prompt, model string, permMode codeagent.PermissionMode, systemPrompt, sessionID string, maxTurns int) []string {
	args := []string{"-p", prompt, "--model", model, "--output-format", "stream-json"}
	if maxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", maxTurns))
	}
	if permMode != "" && permMode != codeagent.PermissionDefault {
		args = append(args, "--permission-mode", string(permMode))
	}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	if sessionID != "" {
		args = append(args, "--session-id", sessionID)
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

func captureOutput(dir, name string, args ...string) (string, error) {
	cmd := localCommand(dir, name, args...)
	out, err := cmd.Output()
	return string(out), err
}

func captureOutputEnv(dir string, env []string, name string, args ...string) (string, error) {
	cmd := localCommand(dir, name, args...)
	cmd.Env = env
	out, err := cmd.Output()
	return string(out), err
}

func localCommand(workDir, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Dir = workDir
	return cmd
}

func execOutput(workDir string, rt sandbox.SandboxRuntime, name string, args ...string) (string, error) {
	if rt == nil {
		out, err := localCommand(workDir, name, args...).Output()
		if err != nil {
			return "", wrapExitError("claude exec", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	res, err := rt.Capture(name, args)
	if err != nil {
		return "", runtimeErrorf("claude exec", res, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

func execOutputEnv(workDir string, rt sandbox.SandboxRuntime, env []string, name string, args ...string) (string, error) {
	if rt == nil {
		cmd := localCommand(workDir, name, args...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			return "", wrapExitError("claude exec", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	res, err := rt.Capture(name, args)
	if err != nil {
		return "", runtimeErrorf("claude exec", res, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// execOutputEnvContext is like execOutputEnv but honours ctx: when the context
// is cancelled or times out the subprocess is killed via exec.CommandContext.
// This prevents a hung seed call (e.g. unauthenticated CLI blocking on a login
// prompt) from freezing the caller indefinitely.
func execOutputEnvContext(ctx context.Context, workDir string, rt sandbox.SandboxRuntime, env []string, name string, args ...string) (string, error) {
	if rt == nil {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = workDir
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			return "", wrapExitError("claude exec", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	// Sandbox path does not support context cancellation; fall through to the
	// non-context variant and rely on the sandbox's own timeout handling.
	res, err := rt.Capture(name, args)
	if err != nil {
		return "", runtimeErrorf("claude exec", res, err)
	}
	return strings.TrimSpace(res.Stdout), nil
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

func runtimeErrorf(op string, res *sandbox.ExecutionResult, err error) error {
	if res == nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if strings.TrimSpace(res.Stderr) != "" {
		return fmt.Errorf("%s: exit %d: %s", op, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return fmt.Errorf("%s: exit %d: %w", op, res.ExitCode, err)
}

func runtimePID(rt sandbox.SandboxRuntime) string {
	if rt == nil {
		return ""
	}
	sbx := rt.Sandbox()
	if sbx == nil || sbx.State == nil {
		return ""
	}
	return sbx.State.PID
}

func trimSpace(s string) string {
	return strings.TrimSpace(s)
}
