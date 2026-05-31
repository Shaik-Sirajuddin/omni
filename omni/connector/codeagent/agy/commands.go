package agy

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
	Agy codeagent.Provider = "agy"
)

// PTY bracketed-paste and submit constants for ExecInSession.
const (
	submitKey  = "\x1b[13;2u" // CSI-u Shift+Enter
	ctrlU      = "\x15"
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

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

// Create verifies the agy binary is reachable and the user is authenticated,
// then stores the resolved session parameters for subsequent Exec/Stream calls.
func (a *agyAgent) Create(p codeagent.CreateSessionParams) (*codeagent.CreateSessionResult, error) {
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
	// If a pre-existing Agy session ID is provided, attach to it directly
	// instead of seeding a new one.
	if p.SessionID != "" {
		a.sessionID = p.SessionID
	} else {
		a.sessionID = id
	}
	if p.RunTime != nil {
		a.sbxRuntime = *p.RunTime
	}
	workDir := a.workDir
	sessionID := a.sessionID
	binPath := a.binPath
	env := mergeEnv(os.Environ(), p.Envs)
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Verify binary.
	cmdVer := exec.CommandContext(ctx, binPath, "--version")
	cmdVer.Dir = workDir
	cmdVer.Env = env
	out, err := cmdVer.Output()
	if err != nil {
		return nil, fmt.Errorf("agy: create: binary unreachable: %w", err)
	}
	logger.Debug("Create: agy binary ok", "version", trimSpace(string(out)))
	
	// Verify authentication.
	// TODO: agy CLI currently does not support an 'auth status' command.
	// The below code is commented out to prevent hanging the session by dropping into an interactive REPL.
	/*
	authCmd := exec.CommandContext(ctx, binPath, "auth", "status")
	authCmd.Env = env
	if err := authCmd.Run(); err != nil {
		logger.Warn("Create: user not authenticated", "err", err)
		return nil, fmt.Errorf("agy: create: not authenticated — run 'agy auth login' first")
	}
	*/

	// When attaching to an existing session, skip seeding — the conversation
	// already exists in Agy's store and a seed call would corrupt it.
	if p.SessionID != "" {
		logger.Info("Create: attached to existing session", "id", id, "sessionID", sessionID, "workDir", workDir)
		return &codeagent.CreateSessionResult{ID: id, Name: p.Name}, nil
	}

	// Seed the session into Agy's session store by running a minimal
	// print-mode call with --session-id. Without this, `agy -r <id>`
	// fails with "No conversation found" because Agy only persists a
	// session after at least one print-mode exchange.
	a.mu.RLock()
	workDir = a.workDir
	a.mu.RUnlock()

	// Agy does not support seeding sessions via empty prompts.
	// We simply initialize the tracking structure with the new ID.

	logger.Info("Create: session ready", "id", id, "workDir", workDir)
	return &codeagent.CreateSessionResult{ID: id, Name: p.Name}, nil
}

type ptyMetaAttached interface {
	MetaAttached(sessionID string) (int, error)
}

// Resume launches an interactive agy session via `agy -r <id>`.
func (a *agyAgent) Resume(p codeagent.ResumeSessionParams) (*codeagent.ResumeSessionResult, error) {
	ctx := p.Context
	if ctx == nil {
		ctx = context.Background()
	}

	a.mu.Lock()
	binPath := a.binPath
	workDir := a.workDir
	if p.RunTime != nil {
		a.sbxRuntime = *p.RunTime
	}
	rt := a.sbxRuntime
	client := a.ptyClient
	currentSessionID := a.sessionID
	env := mergeEnv(os.Environ(), p.Envs)
	a.mu.Unlock()

	// Prefer the explicit Agy session ID when provided; fall back to p.ID.
	resumeID := p.ID
	if p.SessionID != "" {
		resumeID = p.SessionID
	}
	if resumeID == "" {
		resumeID = currentSessionID
	}
	if resumeID == "" {
		return nil, errors.New("agy: resume: empty session id")
	}

	args := []string{"--conversation", resumeID}
	if p.ForkSession {
		args = append(args, "--continue")
	}

	if rt != nil {
		if err := rt.Command(binPath, args); err != nil {
			return nil, fmt.Errorf("agy: resume: sandbox command: %w", err)
		}
		pid := runtimePID(rt)
		logger.Info("Resume: interactive sandbox session completed", "pid", pid, "sessionID", resumeID)
		return &codeagent.ResumeSessionResult{ProcessID: pid, SessionID: resumeID}, nil
	}

	if client != nil {
		info, err := client.Get("", resumeID)
		if err != nil {
			return nil, fmt.Errorf("agy: resume: pty get %q: %w", resumeID, err)
		}
		if meta, ok := client.(ptyMetaAttached); ok {
			count, err := meta.MetaAttached(resumeID)
			if err != nil {
				logger.Warn("Resume: PTY attached count unavailable", "sessionID", resumeID, "err", err)
			} else if count >= 1 {
				return nil, errors.New("agy: resume: PTY session already has an interactive user attached")
			}
		}
		command := append([]string{binPath}, args...)
		started := false
		if info == nil || info.Status != "active" {
			if err := client.Start(resumeID, command, env, workDir, submitKey); err != nil {
				return nil, fmt.Errorf("agy: resume: pty start: %w", err)
			}
			started = true
		}
		a.mu.Lock()
		a.sessionID = resumeID
		a.mu.Unlock()
		if started {
			logger.Info("Resume: PTY daemon session started", "sessionID", resumeID)
		} else {
			logger.Info("Resume: attached to active PTY daemon session", "sessionID", resumeID)
		}
		logger.Info("Resume: attaching PTY daemon session", "sessionID", resumeID)
		done := make(chan error, 1)
		go func() {
			defer close(done)
			err := client.Attach(ctx, resumeID)
			if err != nil {
				done <- fmt.Errorf("agy: resume: pty attach: %w", err)
				return
			}
			logger.Info("Resume: PTY daemon session detached", "sessionID", resumeID)
			done <- nil
		}()
		return &codeagent.ResumeSessionResult{ProcessID: "", SessionID: resumeID, Done: done}, nil
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
		return nil, fmt.Errorf("agy: resume: start process: %w", err)
	}

	pid := fmt.Sprintf("%d", cmd.Process.Pid)
	logger.Info("Resume: interactive session started", "pid", pid, "sessionID", resumeID)

	// Block until the interactive session ends. This keeps the tty fd open
	// for the full duration and prevents the caller from racing with the child.
	_ = cmd.Wait()

	return &codeagent.ResumeSessionResult{ProcessID: pid, SessionID: resumeID}, nil
}

// List is not supported by the Agy CLI.
func (a *agyAgent) List(_ codeagent.ListSessionsParams) (*codeagent.ListSessionsResult, error) {
	logger.Warn("List: agy CLI has no session list command")
	return &codeagent.ListSessionsResult{Sessions: nil}, nil
}

// Delete is not supported by the Agy CLI.
func (a *agyAgent) Delete(_ codeagent.DeleteSessionParams) (*codeagent.DeleteSessionResult, error) {
	logger.Warn("Delete: agy CLI has no session delete command")
	return &codeagent.DeleteSessionResult{Deleted: false}, nil
}

func (a *agyAgent) Stop() {
	logger.Info("Stop: no-op for non-interactive agy sessions")
}

// ============================================================
// ExecInSession
// ============================================================

// ExecInSession sends a prompt into an active interactive PTY session.
// It is fire-and-forget: the prompt is piped into the PTY stdin and the call
// returns immediately without waiting for a response.
func (a *agyAgent) ExecInSession(p codeagent.ExecInSessionParams) (*codeagent.ExecInSessionResult, error) {
	a.mu.RLock()
	client := a.ptyClient
	sessionID := a.sessionID
	a.mu.RUnlock()

	payload := buildExecPayload(p.Prompt)
	if p.SessionID != "" {
		sessionID = p.SessionID
	}
	if sessionID == "" {
		return nil, fmt.Errorf("agy: ExecInSession: no session ID")
	}
	if client == nil {
		return nil, fmt.Errorf("agy: ExecInSession: no active PTY session")
	}
	if err := client.Exec(sessionID, string(payload)); err != nil {
		return nil, fmt.Errorf("agy: ExecInSession: session not live: %w", err)
	}

	logger.Debug("ExecInSession: prompt delegated", "sessionID", sessionID, "promptLen", len(p.Prompt))
	return &codeagent.ExecInSessionResult{SessionID: sessionID}, nil
}

// ============================================================
// GetSessionConfig
// ============================================================

func (a *agyAgent) GetSessionConfig(_ codeagent.GetSessionConfigParams) (*codeagent.GetSessionConfigResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return &codeagent.GetSessionConfigResult{
		Model:          codeagent.Model{Provider: Agy, Model: a.model},
		PermissionMode: a.permMode,
		WorkDir:        a.workDir,
		SystemPrompt:   a.systemPrompt,
	}, nil
}

// ============================================================
// Exec
// ============================================================

func (a *agyAgent) Exec(p codeagent.ExecuteParams) (*codeagent.ExecuteResult, error) {
	a.mu.RLock()
	binPath := a.binPath
	workDir := a.workDir
	model := a.model
	permMode := a.permMode
	systemPrompt := a.systemPrompt
	sessionID := a.sessionID
	rt := a.sbxRuntime
	a.mu.RUnlock()

	args := buildExecArgs(p.Prompt, model, permMode, systemPrompt, sessionID, p.OutputFormat, p.MaxTurns)
	logger.Debug("Exec", "workDir", workDir, "args", args)

	response, err := execOutput(workDir, rt, binPath, args...)
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

func (a *agyAgent) Stream(p codeagent.StreamParams) (*codeagent.StreamResult, error) {
	a.mu.RLock()
	binPath := a.binPath
	workDir := a.workDir
	model := a.model
	permMode := a.permMode
	systemPrompt := a.systemPrompt
	sessionID := a.sessionID
	rt := a.sbxRuntime
	a.mu.RUnlock()

	args := buildStreamArgs(p.Prompt, model, permMode, systemPrompt, sessionID, p.MaxTurns)
	logger.Debug("Stream", "workDir", workDir, "args", args)

	ch := make(chan codeagent.StreamEvent, 32)
	if rt != nil {
		proc, err := rt.Start(binPath, args)
		if err != nil {
			return nil, fmt.Errorf("agy stream: sandbox start: %w", err)
		}
		go func() {
			defer close(ch)
			res, waitErr := proc.Wait()
			if waitErr != nil {
				msg := runtimeErrorf("agy stream", res, waitErr).Error()
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
				ev := parseAgyLine(line)
				ch <- ev
				if ev.Done {
					return
				}
			}
			ch <- codeagent.StreamEvent{Type: "stop", Done: true}
			logger.Debug("Stream completed via sandbox runtime")
		}()
		return &codeagent.StreamResult{Events: ch, SessionID: sessionID}, nil
	}

	cmd := localCommand(workDir, binPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("agy stream: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("agy stream: start process: %w", err)
	}
	go func() {
		defer close(ch)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			ev := parseAgyLine(line)
			ch <- ev
			if ev.Done {
				return
			}
		}
		if err := cmd.Wait(); err != nil {
			msg := wrapExitError("agy stream", err).Error()
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

// buildExecPayload constructs the bracketed-paste sequence used by ExecInSession
// to inject a prompt into a live PTY without triggering mid-paste interpretation.
func buildExecPayload(prompt string) []byte {
	return []byte(ctrlU + pasteStart + prompt + pasteEnd + submitKey)
}

// ============================================================
// Arg builders
// ============================================================

func buildExecArgs(prompt, model string, permMode codeagent.PermissionMode, systemPrompt, sessionID string, format codeagent.OutputFormat, maxTurns int) []string {
	args := []string{"-p", prompt}

	if permMode == codeagent.PermissionDontAsk || permMode == codeagent.PermissionAuto || permMode == codeagent.PermissionAcceptEdits {
		args = append(args, "--dangerously-skip-permissions")
	}
	if sessionID != "" {
		args = append(args, "--conversation", sessionID)
	}
	return args
}

func buildStreamArgs(prompt, model string, permMode codeagent.PermissionMode, systemPrompt, sessionID string, maxTurns int) []string {
	args := []string{"-p", prompt}
	if permMode == codeagent.PermissionDontAsk || permMode == codeagent.PermissionAuto || permMode == codeagent.PermissionAcceptEdits {
		args = append(args, "--dangerously-skip-permissions")
	}
	if sessionID != "" {
		args = append(args, "--conversation", sessionID)
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
			return "", wrapExitError("agy exec", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	res, err := rt.Capture(name, args)
	if err != nil {
		return "", runtimeErrorf("agy exec", res, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

func execOutputEnv(workDir string, rt sandbox.SandboxRuntime, env []string, name string, args ...string) (string, error) {
	if rt == nil {
		cmd := localCommand(workDir, name, args...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			return "", wrapExitError("agy exec", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	res, err := rt.Capture(name, args)
	if err != nil {
		return "", runtimeErrorf("agy exec", res, err)
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
