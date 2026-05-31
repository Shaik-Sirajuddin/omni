package gemini

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
	"github.com/creack/pty"
)

const (
	Gemini codeagent.Provider = "gemini"

	submitKey = "\x1b[13;2u" // CSI-u Shift+Enter
)

const (
	ctrlU      = "\x15"
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

var (
	interactiveStdin  io.Reader = os.Stdin
	interactiveStdout io.Writer = os.Stdout
	interactiveStderr io.Writer = os.Stderr
)

// Create prepares a Gemini CLI session and syncs model/approval/sandbox settings.
// TODO : Missing validation checks on model
func (a *geminiAgent) Create(p codeagent.CreateSessionParams) (*codeagent.CreateSessionResult, error) {
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
	requestedSessionID := strings.TrimSpace(p.SessionID)
	id := requestedSessionID
	if id == "" {
		id = p.ID
		if id == "" {
			id = generateID()
		}
	}
	a.sessionID = id
	workDir := a.workDir
	model := a.model
	permMode := a.permMode
	a.mu.Unlock()

	out, err := captureOutput(workDir, "gemini", "--version")
	if err != nil {
		return nil, fmt.Errorf("gemini: create: binary unreachable: %w", err)
	}
	logger.Debug("Create: gemini binary ok", "version", trimSpace(out))

	if identity := a.GetUserIdentity(); !identity.Authenticated {
		return nil, errors.New("gemini: create: not authenticated or gemini CLI unavailable")
	}

	if err := syncModelAndModeConfig(workDir, model, permMode); err != nil {
		logger.Warn("Create: could not sync model/mode config", "err", err)
	}
	if err := a.syncSandboxConfig(); err != nil {
		logger.Warn("Create: could not sync sandbox config", "err", err)
	}

	sessionPrompt := "reply-with-hi-" + id
	if _, err := captureOutput(workDir, "gemini", "-p", sessionPrompt); err != nil {
		return nil, fmt.Errorf("gemini: create: seed session: %w", err)
	}
	if requestedSessionID != "" {
		a.mu.Lock()
		a.sessionID = requestedSessionID
		a.mu.Unlock()
		return &codeagent.CreateSessionResult{ID: requestedSessionID, Name: p.Name}, nil
	}

	listOut, err := captureOutput(workDir, "gemini", "--list-sessions")
	if err != nil {
		return nil, fmt.Errorf("gemini: create: list sessions: %w", err)
	}
	actualID, err := sessionIDForPrompt(listOut, sessionPrompt)
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	a.sessionID = actualID
	a.mu.Unlock()

	return &codeagent.CreateSessionResult{ID: actualID, Name: p.Name}, nil
}

func (a *geminiAgent) Resume(p codeagent.ResumeSessionParams) (*codeagent.ResumeSessionResult, error) {
	a.mu.RLock()
	workDir := a.workDir
	ptyClient := a.ptyClient
	live := a.masterPTY != nil
	a.mu.RUnlock()
	if live {
		return nil, errors.New("gemini: PTY session already active; stop it before resuming")
	}

	geminiArgs := []string{"--resume"}
	resolvedSessionID := strings.TrimSpace(p.SessionID)
	if resolvedSessionID == "" {
		resolvedSessionID = p.ID
	}
	if resolvedSessionID != "" {
		geminiArgs = append(geminiArgs, resolvedSessionID)
	}
	if p.ForkSession {
		// Gemini CLI does not expose a fork-session flag in non-interactive args.
		logger.Warn("Resume: ForkSession requested but unsupported by gemini CLI; continuing without fork")
	}

	cmd := exec.Command(resumeShell(), "-lc", buildShellExecCommand("gemini", geminiArgs...))
	cmd.Dir = workDir
	if ptyClient != nil {
		if resolvedSessionID == "" {
			resolvedSessionID = "latest"
		}
		if p.Detached {
			command := []string{resumeShell(), "-lc", buildShellExecCommand("gemini", geminiArgs...)}
			if err := ptyClient.Start(resolvedSessionID, command, p.Envs, workDir, submitKey); err != nil {
				return nil, fmt.Errorf("gemini: resume: pty start: %w", err)
			}
			a.mu.Lock()
			a.sessionID = resolvedSessionID
			a.mu.Unlock()
			logger.Info("Resume: PTY daemon session started detached", "sessionID", resolvedSessionID)
			return &codeagent.ResumeSessionResult{ProcessID: "", SessionID: resolvedSessionID}, nil
		}

		master, err := pty.Start(cmd)
		if err != nil {
			return nil, fmt.Errorf("gemini: resume: pty start: %w", err)
		}
		pid := fmt.Sprintf("%d", cmd.Process.Pid)
		if resolvedSessionID == "" {
			resolvedSessionID = "latest"
		}

		a.mu.Lock()
		a.masterPTY = master
		a.activeCmd = cmd
		a.sessionID = resolvedSessionID
		a.mu.Unlock()

		go func(sessionID string, client PTYClient, master *os.File) {
			_ = cmd.Wait()
			_ = master.Close()
			a.mu.Lock()
			a.masterPTY = nil
			a.mu.Unlock()
			if stopper, ok := client.(interface {
				Stop(agentID, sessionID string) error
			}); ok {
				_ = stopper.Stop(string(Gemini), sessionID)
			}
		}(resolvedSessionID, ptyClient, master)

		logger.Info("Resume: interactive PTY session started", "pid", pid, "sessionID", resolvedSessionID)
		return &codeagent.ResumeSessionResult{ProcessID: pid, SessionID: resolvedSessionID}, nil
	}

	cmd.Stdin = interactiveStdin
	cmd.Stdout = interactiveStdout
	cmd.Stderr = interactiveStderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("gemini: resume: start process: %w", err)
	}

	pid := fmt.Sprintf("%d", cmd.Process.Pid)
	a.mu.Lock()
	a.activeCmd = cmd
	if resolvedSessionID == "" {
		resolvedSessionID = "latest"
	}
	a.sessionID = resolvedSessionID
	a.mu.Unlock()

	logger.Info("Resume: interactive session started", "pid", pid, "sessionID", resolvedSessionID)
	return &codeagent.ResumeSessionResult{ProcessID: pid, SessionID: resolvedSessionID}, nil
}

func (a *geminiAgent) ExecInSession(p codeagent.ExecInSessionParams) (*codeagent.ExecInSessionResult, error) {
	sessionID := strings.TrimSpace(p.SessionID)
	if sessionID == "" {
		return nil, errors.New("session not live")
	}

	a.mu.RLock()
	client := a.ptyClient
	master := a.masterPTY
	a.mu.RUnlock()

	payload := []byte(ctrlU + pasteStart + p.Prompt + pasteEnd + submitKey)
	if master != nil {
		if _, err := master.Write(payload); err != nil {
			return nil, err
		}
		return &codeagent.ExecInSessionResult{SessionID: sessionID}, nil
	}
	if client == nil {
		return nil, errors.New("no active PTY session")
	}
	if err := client.Pipe(string(Gemini), sessionID, payload); err != nil {
		return nil, err
	}
	return &codeagent.ExecInSessionResult{SessionID: sessionID}, nil
}

func (a *geminiAgent) List(_ codeagent.ListSessionsParams) (*codeagent.ListSessionsResult, error) {
	a.mu.RLock()
	workDir := a.workDir
	model := a.model
	a.mu.RUnlock()

	out, err := captureOutput(workDir, "gemini", "--list-sessions")
	if err != nil {
		return nil, fmt.Errorf("gemini: list sessions: %w", err)
	}

	sessions := parseSessionList(out, workDir, model)
	return &codeagent.ListSessionsResult{Sessions: sessions}, nil
}

func (a *geminiAgent) Delete(p codeagent.DeleteSessionParams) (*codeagent.DeleteSessionResult, error) {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()

	if strings.TrimSpace(p.ID) == "" {
		return nil, errors.New("gemini: delete session: empty session id")
	}
	if _, err := captureOutput(workDir, "gemini", "--delete-session", p.ID); err != nil {
		return nil, fmt.Errorf("gemini: delete session %q: %w", p.ID, err)
	}
	logger.Info("Delete: session deleted", "sessionID", p.ID)
	return &codeagent.DeleteSessionResult{Deleted: true}, nil
}

func (a *geminiAgent) Stop() {
	a.mu.Lock()
	cmd := a.activeCmd
	a.activeCmd = nil
	a.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		logger.Info("Stop: no active gemini process")
		return
	}
	if err := cmd.Process.Kill(); err != nil {
		logger.Warn("Stop: failed to kill active process", "err", err)
		return
	}
	if err := cmd.Wait(); err != nil {
		logger.Debug("Stop: active gemini process reaped", "err", err)
	}
	logger.Info("Stop: active gemini process terminated")
}

func (a *geminiAgent) GetSessionConfig(_ codeagent.GetSessionConfigParams) (*codeagent.GetSessionConfigResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return &codeagent.GetSessionConfigResult{
		Model:          codeagent.Model{Provider: Gemini, Model: a.model},
		PermissionMode: a.permMode,
		WorkDir:        a.workDir,
		SystemPrompt:   a.systemPrompt,
	}, nil
}

func (a *geminiAgent) Exec(p codeagent.ExecuteParams) (*codeagent.ExecuteResult, error) {
	a.mu.RLock()
	workDir := a.workDir
	model := a.model
	permMode := a.permMode
	systemPrompt := a.systemPrompt
	sessionID := a.sessionID
	sbx := a.sbx
	a.mu.RUnlock()

	args := buildExecArgs(p.Prompt, model, permMode, systemPrompt, p.OutputFormat, p.MaxTurns, sbx)
	logger.Debug("Exec", "workDir", workDir, "args", args)

	cmd := exec.Command("gemini", args...)
	cmd.Dir = workDir

	out, err := cmd.Output()
	if err != nil {
		return nil, wrapExitError("gemini exec", err)
	}

	response := strings.TrimSpace(string(out))
	return &codeagent.ExecuteResult{
		PromptID:   p.PromptId,
		SessionID:  sessionID,
		Response:   response,
		StopReason: "stop",
	}, nil
}

func (a *geminiAgent) Stream(p codeagent.StreamParams) (*codeagent.StreamResult, error) {
	a.mu.RLock()
	workDir := a.workDir
	model := a.model
	permMode := a.permMode
	systemPrompt := a.systemPrompt
	sessionID := a.sessionID
	sbx := a.sbx
	a.mu.RUnlock()

	args := buildStreamArgs(p.Prompt, model, permMode, systemPrompt, p.MaxTurns, sbx)
	logger.Debug("Stream", "workDir", workDir, "args", args)

	cmd := exec.Command("gemini", args...)
	cmd.Dir = workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("gemini stream: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("gemini stream: start process: %w", err)
	}

	ch := make(chan codeagent.StreamEvent, 32)
	go func() {
		defer close(ch)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			ev := parseGeminiLine(line)
			ch <- ev
			if ev.Done {
				return
			}
		}
		if err := cmd.Wait(); err != nil {
			msg := wrapExitError("gemini stream", err).Error()
			logger.Error("Stream: process exited with error", "err", msg)
			ch <- codeagent.StreamEvent{Type: "stop", Done: true, Content: msg}
			return
		}
		ch <- codeagent.StreamEvent{Type: "stop", Done: true}
	}()

	return &codeagent.StreamResult{Events: ch, SessionID: sessionID}, nil
}

func buildExecArgs(prompt, model string, permMode codeagent.PermissionMode, systemPrompt string, format codeagent.OutputFormat, maxTurns int, sbx *sandbox.Config) []string {
	args := []string{}
	if prompt != "" {
		args = append(args, prompt)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if am := approvalModeFlag(permMode); am != "" {
		args = append(args, "--approval-mode", am)
	}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	if maxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", maxTurns))
	}
	if sf := sandboxFlagValue(sbx); sf == "danger-full-access" {
		args = append(args, "--yolo")
	}
	if format == codeagent.OutputFormatJSON || format == codeagent.OutputFormatStreamJSON {
		args = append(args, "--acp")
	}
	return args
}

func buildStreamArgs(prompt, model string, permMode codeagent.PermissionMode, systemPrompt string, maxTurns int, sbx *sandbox.Config) []string {
	return buildExecArgs(prompt, model, permMode, systemPrompt, codeagent.OutputFormatStreamJSON, maxTurns, sbx)
}

func approvalModeFlag(mode codeagent.PermissionMode) string {
	switch mode {
	case "", codeagent.PermissionDefault:
		return "default"
	case codeagent.PermissionPlan:
		return "plan"
	case codeagent.PermissionAcceptEdits, codeagent.PermissionAuto:
		return "auto_edit"
	case codeagent.PermissionDontAsk, codeagent.PermissionBypassPermissions:
		return "yolo"
	default:
		return "default"
	}
}

func permissionFromApprovalMode(mode string) codeagent.PermissionMode {
	switch strings.TrimSpace(mode) {
	case "default":
		return codeagent.PermissionDefault
	case "plan":
		return codeagent.PermissionPlan
	case "auto_edit":
		return codeagent.PermissionAcceptEdits
	case "yolo":
		return codeagent.PermissionBypassPermissions
	default:
		return ""
	}
}

func wrapExitError(op string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("%s: exit %d: %s", op, exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
	}
	return fmt.Errorf("%s: %w", op, err)
}

func captureOutput(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func trimSpace(s string) string {
	return strings.TrimSpace(s)
}

func resumeShell() string {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell
	}
	return "bash"
}

func buildShellExecCommand(name string, args ...string) string {
	parts := make([]string, 0, len(args)+2)
	parts = append(parts, "exec", shellQuote(name))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(v string) string {
	if v == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(v, "'", `'"'"'`) + "'"
}

var sessionLinePattern = regexp.MustCompile(`^\s*\d+\.\s*(.*?)\s*\[([^\]]+)\]\s*$`)

func parseSessionList(raw, workDir, model string) []*codeagent.Session {
	var sessions []*codeagent.Session
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := sessionLinePattern.FindStringSubmatch(line)
		if len(m) != 3 {
			continue
		}
		name := strings.TrimSpace(m[1])
		id := strings.TrimSpace(m[2])
		sessions = append(sessions, &codeagent.Session{
			ID:       id,
			Name:     name,
			Provider: Gemini,
			Model:    model,
			WorkDir:  workDir,
		})
	}
	return sessions
}

func sessionIDForPrompt(raw, prompt string) (string, error) {
	sessions := parseSessionList(raw, "", "")
	for _, session := range sessions {
		if strings.Contains(session.Name, prompt) {
			return session.ID, nil
		}
	}
	return "", fmt.Errorf("gemini: create: session prompt %q not found in session list", prompt)
}
