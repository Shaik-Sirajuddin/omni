package impl

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/agy"
	agysettings "github.com/Shaik-Sirajuddin/memory/connector/codeagent/agy/settings"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/claude"
	claudesettings "github.com/Shaik-Sirajuddin/memory/connector/codeagent/claude/settings"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/codex"
	codexsettings "github.com/Shaik-Sirajuddin/memory/connector/codeagent/codex/settings"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/gemini"
	"github.com/Shaik-Sirajuddin/memory/omniagent"
	operator "github.com/Shaik-Sirajuddin/memory/operator"
	"github.com/Shaik-Sirajuddin/memory/operator/impl/defaults"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox"
	agentstore "github.com/Shaik-Sirajuddin/memory/store/agent"
	"github.com/Shaik-Sirajuddin/memory/store/codesession"
	operatorstore "github.com/Shaik-Sirajuddin/memory/store/operator"
	ptydaemon "github.com/Shaik-Sirajuddin/memory/svc/ptydaemon"
	ptyclients "github.com/Shaik-Sirajuddin/memory/svc/ptydaemon/clients"
	"github.com/google/uuid"
)

// Provider constants mirror those in each connector package, defined here to
// avoid importing packages that currently have build errors.
const (
	providerClaude codeagent.Provider = "claude"
	providerCodex  codeagent.Provider = "codex"
	providerGemini codeagent.Provider = "gemini"
	providerAgy    codeagent.Provider = "agy"

	mcpAuthToken  = "tunnel-mcp-dev-token"
	mcpSenderType = "omni_agent"
)

// DefaultOperator implements [Operator].
// agentMemory is a pluggable module: non-nil enables memory operations;
// nil disables them (e.g. when config.Features.Memory is false).
type DefaultOperator struct {
	config               config.OmniConfig
	store                operatorstore.OperatorStore
	agentStore           agentstore.AgentStore
	sessionStore         codesession.CodeSessionStore
	agentMemory          operator.AgentMemory
	provisioner          sandbox.SandboxProvisioner // nil = sandboxing disabled
	ptyDaemon            ptyclients.Client
	newCodeAgent         func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error)
	newCodeAgentForAgent func(agentID string, provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error)
}

type codexPTYAdapter struct {
	agentID string
	workDir string
	inner   ptyclients.Client
}

func (a *codexPTYAdapter) Pipe(_ string, sessionID string, data []byte) error {
	if a == nil || a.inner == nil {
		return fmt.Errorf("operator: pty daemon client is not configured")
	}
	return a.inner.Pipe(a.agentID, sessionID, data)
}

func (a *codexPTYAdapter) Start(sessionID string, command []string, env []string, dir, submitKey string) error {
	if a == nil || a.inner == nil {
		return fmt.Errorf("operator: pty daemon client is not configured")
	}
	if strings.TrimSpace(dir) == "" {
		dir = a.workDir
	}
	return a.inner.Start(sessionID, command, env, dir, submitKey)
}

func (a *codexPTYAdapter) Attach(ctx context.Context, sessionID string) error {
	if a == nil || a.inner == nil {
		return fmt.Errorf("operator: pty daemon client is not configured")
	}
	return a.inner.Attach(ctx, sessionID)
}

func (a *codexPTYAdapter) Exec(sessionID, input string) error {
	if a == nil || a.inner == nil {
		return fmt.Errorf("operator: pty daemon client is not configured")
	}
	return a.inner.Exec(sessionID, input)
}

func (a *codexPTYAdapter) Stop(sessionID string) error {
	if a == nil || a.inner == nil {
		return fmt.Errorf("operator: pty daemon client is not configured")
	}
	return a.inner.Stop(sessionID)
}

func (a *codexPTYAdapter) StopSafe(sessionID string, force bool) error {
	if a == nil || a.inner == nil {
		return fmt.Errorf("operator: pty daemon client is not configured")
	}
	return a.inner.StopSafe(sessionID, force)
}

func (a *codexPTYAdapter) Detach(sessionID string) error {
	if a == nil || a.inner == nil {
		return fmt.Errorf("operator: pty daemon client is not configured")
	}
	return a.inner.Detach(sessionID)
}

func (a *codexPTYAdapter) List(_ string) ([]*codeagent.PTYTerminalInfo, error) {
	if a == nil || a.inner == nil {
		return nil, fmt.Errorf("operator: pty daemon client is not configured")
	}
	infos, err := a.inner.List(a.agentID)
	if err != nil {
		return nil, err
	}
	out := make([]*codeagent.PTYTerminalInfo, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}
		out = append(out, &codeagent.PTYTerminalInfo{
			AgentID:   info.AgentID,
			SessionID: info.SessionID,
			Status:    info.Status,
		})
	}
	return out, nil
}

func (a *codexPTYAdapter) Get(_ string, sessionID string) (*codeagent.PTYTerminalInfo, error) {
	if a == nil || a.inner == nil {
		return nil, fmt.Errorf("operator: pty daemon client is not configured")
	}
	info, err := a.inner.Get(a.agentID, sessionID)
	if err != nil || info == nil {
		return nil, err
	}
	return &codeagent.PTYTerminalInfo{
		AgentID:   info.AgentID,
		SessionID: info.SessionID,
		Status:    info.Status,
	}, nil
}

// SwitchProvider implements [Operator].
// SwitchProvider when switching between models , if a non fill session exists matching provider , agent , session is reused , override by CleanStart
func (o *DefaultOperator) SwitchProvider(params operator.SwitchProviderParams) error {
	if strings.TrimSpace(params.ID) == "" {
		return fmt.Errorf("operator: agent id is required")
	}
	if params.Provider == "" {
		return fmt.Errorf("operator: provider is required")
	}
	if o.sessionStore == nil {
		return fmt.Errorf("operator: session store is not configured")
	}
	agent, err := o.store.GetAgent(params.ID)
	if err != nil {
		return fmt.Errorf("operator: switch provider: load agent %q: %w", params.ID, err)
	}
	sessions, err := o.sessionStore.ListSessions(agent.ID, nil)
	if err != nil {
		return fmt.Errorf("operator: switch provider: list sessions for agent %q: %w", agent.ID, err)
	}

	target := (*omniagent.CodeSession)(nil)
	if !params.CleanStart {
		for _, s := range sessions {
			if s != nil && s.Model != nil && s.Model.Provider == params.Provider {
				target = s
				break
			}
		}
	}

	if target == nil {
		model := ""
		ca, err := o.createCodeAgent(agent, params.Provider, string(agent.WorkspaceDir), model)
		if err != nil {
			return fmt.Errorf("operator: switch provider: init code agent: %w", err)
		}

		var sbxRuntime *sandbox.SandboxRuntime
		if o.provisioner != nil {
			workDir := string(agent.WorkspaceDir)
			cfg := defaults.SandboxConfig(params.Provider, workDir)
			configDir := sandboxConfigDir(workDir, agent.Name)
			rt, sbxErr := o.provisioner.Create(sandbox.CreateSandboxParams{
				ID:        agent.ID,
				ConfigDir: configDir,
				Config:    cfg,
			})
			if sbxErr == nil {
				sbxRuntime = &rt
			}
		}

		requestedSessionID := strings.TrimSpace(params.SessionID)
		newID := requestedSessionID
		if newID == "" {
			newID = uuid.NewString()
		}
		envs := mcpSessionEnvs(agent)
		createResult, err := ca.Create(codeagent.CreateSessionParams{
			ID:        newID,
			Name:      agent.Name,
			Model:     model,
			WorkDir:   string(agent.WorkspaceDir),
			SessionID: requestedSessionID,
			Envs:      envs,
			RunTime:   sbxRuntime,
		})
		if err != nil {
			return fmt.Errorf("operator: switch provider: create session for agent %q: %w", agent.ID, err)
		}
		if createResult != nil && createResult.ID != "" {
			newID = createResult.ID
		}
		model = resolvedSessionModel(ca, model)
		target = &omniagent.CodeSession{
			Id:       newID,
			Model:    &codeagent.Model{Provider: params.Provider, Model: model},
			IsActive: true,
		}
		if err := o.sessionStore.CreateSession(agent.ID, target); err != nil {
			return fmt.Errorf("operator: switch provider: persist new session: %w", err)
		}
	}

	for _, s := range sessions {
		if s == nil || s.Id == "" || s.Id == target.Id {
			continue
		}
		if s.IsActive {
			s.IsActive = false
			if err := o.sessionStore.UpdateSession(agent.ID, s); err != nil {
				return fmt.Errorf("operator: switch provider: deactivate session %q: %w", s.Id, err)
			}
		}
	}
	target.IsActive = true
	if err := o.sessionStore.UpdateSession(agent.ID, target); err != nil {
		return fmt.Errorf("operator: switch provider: activate session %q: %w", target.Id, err)
	}

	model := ""
	if target.Model != nil && target.Model.Model != "" {
		model = target.Model.Model
	}
	ca, err := o.createCodeAgent(agent, params.Provider, string(agent.WorkspaceDir), model)
	if err != nil {
		return fmt.Errorf("operator: switch provider: init runtime for resume: %w", err)
	}
	resumeCtx, cancelResume := newResumeContext()
	defer cancelResume()
	resumeResult, err := ca.Resume(codeagent.ResumeSessionParams{Context: resumeCtx, ID: target.Id, Envs: mcpSessionEnvs(agent)})
	if err != nil {
		return fmt.Errorf("operator: switch provider: resume session %q: %w", target.Id, err)
	}
	if err := waitResumeLifecycle(resumeCtx, resumeResult); err != nil {
		return err
	}
	return nil
}

func (o *DefaultOperator) ExecInSession(params operator.ExecInSessionParams) (*operator.ExecInSessionResult, error) {
	agent, ca, err := o.codeAgentForAgentID(params.AgentID)
	if err != nil {
		return nil, err
	}
	sessionID := strings.TrimSpace(params.SessionID)
	if sessionID == "" {
		if o.sessionStore == nil {
			return nil, fmt.Errorf("operator: session store is not configured")
		}
		session, err := o.sessionStore.GetSession(agent.ID)
		if err != nil {
			return nil, fmt.Errorf("operator: active session for agent %q: %w", agent.ID, err)
		}
		if session == nil || strings.TrimSpace(session.Id) == "" {
			return nil, fmt.Errorf("operator: active session not found for agent %q", agent.ID)
		}
		sessionID = strings.TrimSpace(session.Id)
	}

	// Check whether the session is currently attached via the pty daemon.
	sessionActive := false
	if o.ptyDaemon != nil {
		if count, err := o.ptyDaemon.MetaAttached(sessionID); err == nil {
			sessionActive = count >= 1
		}
	}

	if !sessionActive {
		if params.LiveOnly {
			return nil, fmt.Errorf("operator: exec in session: session %q is not active", sessionID)
		}
		// Auto-resume the agent before executing.
		logger.Info("ExecInSession: session not active, auto-resuming", "agentID", agent.ID, "sessionID", sessionID)
		if err := o.ResumeAgent(operator.ResumeAgentParams{
			Workspace: agent.WorkspaceDir,
			Name:      agent.Name,
			SessionID: sessionID,
			Detached:  true,
		}); err != nil {
			return nil, fmt.Errorf("operator: exec in session: auto-resume: %w", err)
		}
		// Re-init ca after resume since the connector instance may be stale.
		_, ca, err = o.codeAgentForAgentID(params.AgentID)
		if err != nil {
			return nil, fmt.Errorf("operator: exec in session: re-init after resume: %w", err)
		}
	}

	result, err := ca.ExecInSession(codeagent.ExecInSessionParams{
		SessionID: sessionID,
		Prompt:    params.Prompt,
	})
	if err != nil {
		return nil, fmt.Errorf("operator: exec in session: %w", err)
	}
	if result != nil && result.SessionID != "" {
		sessionID = result.SessionID
	}
	return &operator.ExecInSessionResult{SessionID: sessionID}, nil
}

func (o *DefaultOperator) StopSession(params operator.StopSessionParams) (*operator.StopSessionResult, error) {
	if o.ptyDaemon == nil {
		return nil, fmt.Errorf("operator: pty daemon client is not configured")
	}
	_, sessionID, err := o.resolveAgentSession(params.AgentID, params.SessionID)
	if err != nil {
		return nil, err
	}
	if err := o.ptyDaemon.StopSafe(sessionID, params.Force); err != nil {
		return nil, fmt.Errorf("operator: stop session: %w", err)
	}
	return &operator.StopSessionResult{SessionID: sessionID}, nil
}

func (o *DefaultOperator) DetachSession(params operator.DetachSessionParams) (*operator.DetachSessionResult, error) {
	if o.ptyDaemon == nil {
		return nil, fmt.Errorf("operator: pty daemon client is not configured")
	}
	_, sessionID, err := o.resolveAgentSession(params.AgentID, params.SessionID)
	if err != nil {
		return nil, err
	}
	if err := o.ptyDaemon.Detach(sessionID); err != nil {
		return nil, fmt.Errorf("operator: detach session: %w", err)
	}
	return &operator.DetachSessionResult{SessionID: sessionID}, nil
}

func (o *DefaultOperator) Pipe(params operator.PipeParams) error {
	agent, _, err := o.codeAgentForAgentID(params.AgentID)
	if err != nil {
		return err
	}
	sessionID := strings.TrimSpace(params.SessionID)
	if sessionID == "" {
		if o.sessionStore == nil {
			return fmt.Errorf("operator: session store is not configured")
		}
		session, err := o.sessionStore.GetSession(agent.ID)
		if err != nil {
			return fmt.Errorf("operator: active session for agent %q: %w", agent.ID, err)
		}
		if session == nil || strings.TrimSpace(session.Id) == "" {
			return fmt.Errorf("operator: active session not found for agent %q", agent.ID)
		}
		sessionID = strings.TrimSpace(session.Id)
	}
	if o.ptyDaemon == nil {
		return fmt.Errorf("operator: pty daemon client is not configured")
	}
	if err := o.ptyDaemon.Pipe(agent.ID, sessionID, params.Data); err != nil {
		return fmt.Errorf("operator: pipe session: %w", err)
	}
	return nil
}

func (o *DefaultOperator) resolveAgentSession(agentRef, requestedSessionID string) (*omniagent.AgentInfo, string, error) {
	agent, err := o.resolveAgentRef(agentRef)
	if err != nil {
		return nil, "", fmt.Errorf("operator: load agent %q: %w", agentRef, err)
	}
	sessionID := strings.TrimSpace(requestedSessionID)
	if sessionID != "" {
		return agent, sessionID, nil
	}
	if o.sessionStore == nil {
		return nil, "", fmt.Errorf("operator: session store is not configured")
	}
	session, err := o.sessionStore.GetSession(agent.ID)
	if err != nil {
		return nil, "", fmt.Errorf("operator: active session for agent %q: %w", agent.ID, err)
	}
	if session == nil || strings.TrimSpace(session.Id) == "" {
		return nil, "", fmt.Errorf("operator: active session not found for agent %q", agent.ID)
	}
	return agent, strings.TrimSpace(session.Id), nil
}

func (o *DefaultOperator) codeAgentForAgentID(agentID string) (*omniagent.AgentInfo, codeagent.CodeAgent, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, nil, fmt.Errorf("operator: agent id is required")
	}
	agent, err := o.resolveAgentRef(agentID)
	if err != nil {
		return nil, nil, fmt.Errorf("operator: load agent %q: %w", agentID, err)
	}

	provider := codeagent.Provider(operator.DefaultProvider)
	model := ""
	if o.sessionStore != nil {
		if session, sErr := o.sessionStore.GetSession(agent.ID); sErr == nil && session != nil && session.Model != nil {
			if session.Model.Provider != "" {
				provider = session.Model.Provider
			}
			if session.Model.Model != "" {
				model = session.Model.Model
			}
		}
	}
	ca, err := o.createCodeAgent(agent, provider, string(agent.WorkspaceDir), model)
	if err != nil {
		return nil, nil, fmt.Errorf("operator: init code agent runtime: %w", err)
	}
	return agent, ca, nil
}

func (o *DefaultOperator) createCodeAgent(agent *omniagent.AgentInfo, provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
	var ca codeagent.CodeAgent
	var err error
	if agent != nil && o.newCodeAgentForAgent != nil {
		ca, err = o.newCodeAgentForAgent(agent.ID, provider, workDir, model)
	} else if o.newCodeAgent != nil {
		ca, err = o.newCodeAgent(provider, workDir, model)
	} else {
		return nil, fmt.Errorf("operator: code agent factory is not configured")
	}
	if err != nil {
		return nil, err
	}
	if ca != nil && agent != nil && o.ptyDaemon != nil {
		ca.SetPTYClient(o.ptyClientForProvider(agent.ID, provider, workDir))
	}
	return ca, nil
}

func (o *DefaultOperator) ptyClientForProvider(agentID string, provider codeagent.Provider, workDir string) codeagent.PTYClient {
	return &codexPTYAdapter{agentID: agentID, workDir: workDir, inner: o.ptyDaemon}
}

func (o *DefaultOperator) resolveAgentRef(ref string) (*omniagent.AgentInfo, error) {
	agent, err := o.store.GetAgent(ref)
	if err == nil {
		return agent, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	workspace, wErr := o.resolveWorkspace("")
	if wErr != nil {
		return nil, wErr
	}
	agents, listErr := o.store.ListAgentsByDir(workspace)
	if listErr != nil {
		return nil, listErr
	}
	name := sanitizeAgentName(ref)
	for _, candidate := range agents {
		if candidate != nil && candidate.Name == name {
			return candidate, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (o *DefaultOperator) registerPTYSession(agentID, sessionID string, result *codeagent.ResumeSessionResult) {
	if o.ptyDaemon == nil {
		logger.Debug("ResumeAgent: pty daemon registration skipped", "agentID", agentID, "sessionID", sessionID, "reason", "pty daemon client is not configured")
		return
	}
	if result == nil {
		logger.Debug("ResumeAgent: pty daemon registration skipped", "agentID", agentID, "sessionID", sessionID, "reason", "resume result is nil")
		return
	}
	if result.ProcessID == "" {
		logger.Debug("ResumeAgent: pty daemon registration skipped", "agentID", agentID, "sessionID", sessionID, "resultSessionID", result.SessionID, "reason", "process id is empty")
		return
	}
	if result.SessionID != "" {
		sessionID = result.SessionID
	}
	logger.Debug("ResumeAgent: registering pty session", "agentID", agentID, "sessionID", sessionID, "pid", result.ProcessID)
	if err := o.ptyDaemon.Register(agentID, sessionID, result.ProcessID); err != nil {
		logger.Warn("ResumeAgent: pty daemon registration failed", "agentID", agentID, "sessionID", sessionID, "pid", result.ProcessID, "err", err)
		return
	}
	logger.Debug("ResumeAgent: pty daemon registration completed", "agentID", agentID, "sessionID", sessionID, "pid", result.ProcessID)
}

func newResumeContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func waitResumeLifecycle(ctx context.Context, result *codeagent.ResumeSessionResult) error {
	if result == nil || result.Done == nil {
		return nil
	}
	select {
	case err := <-result.Done:
		if err != nil {
			return fmt.Errorf("operator: resume lifecycle failed: %w", err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ResumeAgent implements [Operator].
func (o *DefaultOperator) ResumeAgent(params operator.ResumeAgentParams) error {
	workspace, err := o.resolveWorkspace(params.Workspace)
	if err != nil {
		logger.Error("ResumeAgent: workspace resolution failed", "workspace", params.Workspace, "err", err)
		return err
	}
	name := sanitizeAgentName(params.Name)
	if name == "" {
		logger.Error("ResumeAgent: validation failed", "err", "agent name is required")
		return fmt.Errorf("operator: agent name is required")
	}

	agents, err := o.store.ListAgentsByDir(workspace)
	if err != nil {
		logger.Error("ResumeAgent: list agents failed", "workspace", workspace, "err", err)
		return err
	}

	var agent *omniagent.AgentInfo
	for _, item := range agents {
		if item.Name == name {
			agent = item
			break
		}
	}
	if agent == nil {
		if params.InitIfMissing {
			logger.Info("ResumeAgent: agent missing, creating because init-if-missing is enabled", "workspace", workspace, "name", name)
			return o.CreateAgent(operator.CreateAgentParams{
				Workspace:      workspace,
				Name:           name,
				Provider:       params.Provider,
				Model:          params.Model,
				Interactive:    true,
				ResumeIfExists: false,
				SessionID:      params.SessionID,
			})
		}
		logger.Error("ResumeAgent: agent not found", "workspace", workspace, "name", name)
		return fmt.Errorf("operator: agent %q not found in workspace %q", name, workspace)
	}

	// Resolve provider/model from persisted session; let connectors apply model defaults.
	provider := codeagent.Provider(operator.DefaultProvider)
	model := ""
	sessionID := agent.ID
	if o.sessionStore != nil {
		if session, sErr := o.sessionStore.GetSession(agent.ID); sErr == nil && session != nil {
			if session.Model != nil {
				if session.Model.Provider != "" {
					provider = session.Model.Provider
				}
				if session.Model.Model != "" {
					model = session.Model.Model
				}
			}
			if session.Id != "" {
				sessionID = session.Id
			}
		} else {
			logger.Debug("ResumeAgent: no persisted session, using defaults", "agentID", agent.ID)
		}
	}
	requestedSessionID := strings.TrimSpace(params.SessionID)
	if requestedSessionID != "" {
		sessionID = requestedSessionID
	}
	if o.ptyDaemon != nil {
		if infos, err := o.ptyDaemon.List(agent.ID); err == nil {
			for _, info := range infos {
				if info != nil && info.AgentID == agent.ID && info.SessionID == sessionID && info.Status == "active" {
					if params.Detached {
						logger.Info("ResumeAgent: PTY terminal already active, leaving detached", "agentID", agent.ID, "sessionID", sessionID)
						return nil
					}
					return fmt.Errorf("operator: session %q already has an active PTY terminal", sessionID)
				}
			}
		} else {
			logger.Debug("ResumeAgent: pty daemon active session check skipped", "agentID", agent.ID, "sessionID", sessionID, "err", err)
		}
		if mac, ok := o.ptyDaemon.(interface{ MetaAttached(string) (int, error) }); ok {
			if count, err := mac.MetaAttached(sessionID); err == nil && count >= 1 {
				logger.Info("ResumeAgent: session already attached, skipping resume", "component", "operator", "agentID", agent.ID, "sessionID", sessionID, "attachedCount", count)
				return nil
			}
		}
	}
	ca, err := o.createCodeAgent(agent, provider, string(agent.WorkspaceDir), model)
	if err != nil {
		logger.Error("ResumeAgent: init code agent runtime failed", "agentID", agent.ID, "err", err)
		return fmt.Errorf("operator: init code agent runtime: %w", err)
	}

	// Provision a sandbox runtime for this resume session.
	var sbxRuntime *sandbox.SandboxRuntime
	if o.provisioner != nil {
		workDir := string(agent.WorkspaceDir)
		cfg := defaults.SandboxConfig(provider, workDir)
		configDir := sandboxConfigDir(workDir, agent.Name)
		rt, sbxErr := o.provisioner.Create(sandbox.CreateSandboxParams{
			ID:        agent.ID,
			ConfigDir: configDir,
			Config:    cfg,
		})
		if sbxErr != nil {
			logger.Warn("ResumeAgent: sandbox provision failed", "agentID", agent.ID, "err", sbxErr)
		} else {
			sbxRuntime = &rt
			logger.Debug("ResumeAgent: sandbox runtime provisioned", "agentID", agent.ID, "provider", provider, "configDir", configDir)
		}
	}

	resumeCtx, cancelResume := newResumeContext()
	defer cancelResume()

	envs := mcpSessionEnvs(agent)
	resumeResult, err := ca.Resume(codeagent.ResumeSessionParams{Context: resumeCtx, ID: sessionID, SessionID: requestedSessionID, Detached: params.Detached, Envs: envs, RunTime: sbxRuntime})
	if err != nil {
		if !isSessionNotFoundError(err) {
			logger.Error("ResumeAgent: resume failed", "agentID", agent.ID, "err", err)
			return fmt.Errorf("operator: resume session for agent %q: %w", agent.ID, err)
		}
		logger.Warn("ResumeAgent: no resumable session found, creating a new session", "agentID", agent.ID, "sessionID", sessionID)
		createResult, createErr := ca.Create(codeagent.CreateSessionParams{
			ID:        sessionID,
			Name:      agent.Name,
			Model:     model,
			WorkDir:   string(agent.WorkspaceDir),
			SessionID: requestedSessionID,
			Envs:      envs,
			RunTime:   sbxRuntime,
		})
		if createErr != nil {
			logger.Error("ResumeAgent: fallback create failed", "agentID", agent.ID, "err", createErr)
			return fmt.Errorf("operator: create session fallback for agent %q: %w", agent.ID, createErr)
		}
		if createResult != nil && createResult.ID != "" {
			sessionID = createResult.ID
		}
		model = resolvedSessionModel(ca, model)
		if o.sessionStore != nil {
			if storeErr := o.sessionStore.CreateSession(agent.ID, &omniagent.CodeSession{
				Id:       sessionID,
				Model:    &codeagent.Model{Provider: provider, Model: model},
				IsActive: true,
			}); storeErr != nil {
				logger.Warn("ResumeAgent: fallback session store sync failed", "agentID", agent.ID, "sessionID", sessionID, "err", storeErr)
			}
		}
		resumeResult, err = ca.Resume(codeagent.ResumeSessionParams{Context: resumeCtx, ID: sessionID, SessionID: requestedSessionID, Detached: params.Detached, Envs: envs, RunTime: sbxRuntime})
		if err != nil {
			logger.Error("ResumeAgent: fallback resume failed", "agentID", agent.ID, "sessionID", sessionID, "err", err)
			return fmt.Errorf("operator: resume fallback session for agent %q: %w", agent.ID, err)
		}
	}
	o.registerPTYSession(agent.ID, sessionID, resumeResult)
	if err := waitResumeLifecycle(resumeCtx, resumeResult); err != nil {
		logger.Error("ResumeAgent: resume lifecycle ended with error", "agentID", agent.ID, "sessionID", sessionID, "err", err)
		return err
	}
	logger.Info("ResumeAgent: completed", "agentID", agent.ID, "workspace", workspace, "name", name, "provider", provider, "sessionID", sessionID)
	return nil
}

// New returns an Operator with the default embedded AgentMemory module enabled.
// The memory feature is gated at runtime by OmniConfig.Features.Memory.
func New() (operator.Operator, error) {
	logger.Debug("New: initialising operator")
	s, err := operatorstore.GetOperatorStore()
	if err != nil {
		logger.Error("New: store initialisation failed", "err", err)
		return nil, fmt.Errorf("operator: init store: %w", err)
	}
	as, err := agentstore.GetOmniAgentStore()
	if err != nil {
		logger.Error("New: agent store initialisation failed", "err", err)
		return nil, fmt.Errorf("operator: init agent store: %w", err)
	}
	ss, err := codesession.GetCodeSessionStore()
	if err != nil {
		logger.Error("New: session store initialisation failed", "err", err)
		return nil, fmt.Errorf("operator: init session store: %w", err)
	}
	// Initialise a sandbox provisioner when a supported kind is available on the host.
	var provisioner sandbox.SandboxProvisioner
	if kinds := sandbox.HostSupportedProvisioners(); len(kinds) > 0 {
		p, perr := sandbox.NewProvisioner(kinds[0], nil, sandbox.ProvisionerOptions{})
		if perr != nil {
			logger.Warn("New: sandbox provisioner init failed", "kind", kinds[0], "err", perr)
		} else {
			provisioner = p
			logger.Info("New: sandbox provisioner ready", "kind", kinds[0])
		}
	}

	ptySocketPath := ptydaemon.DefaultSocketPath()
	ptyDaemon := ptyclients.NewUnixSocketClient(ptySocketPath)
	logger.Debug("New: pty daemon client configured", "socket", ptySocketPath)
	op := &DefaultOperator{
		config: config.OmniConfig{
			Features: &config.Features{Memory: true},
		},
		store:        s,
		agentStore:   as,
		sessionStore: ss,
		agentMemory:  operator.NewDefaultAgentMemory(),
		provisioner:  provisioner,
		ptyDaemon:    ptyDaemon,
		newCodeAgent: func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			switch provider {
			case providerClaude:
				return claude.New(workDir, model, nil)
			case providerCodex:
				return codex.New(workDir, model, nil)
			case providerGemini:
				return gemini.New(workDir, model, nil)
			case providerAgy:
				return agy.New(workDir, model, nil)
			default:
				return nil, fmt.Errorf("operator: unknown provider %q", provider)
			}
		},
		newCodeAgentForAgent: func(agentID string, provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			switch provider {
			case providerClaude:
				return claude.New(workDir, model, &codexPTYAdapter{agentID: agentID, workDir: workDir, inner: ptyDaemon})
			case providerCodex:
				return codex.New(workDir, model, &codexPTYAdapter{agentID: agentID, workDir: workDir, inner: ptyDaemon})
			case providerGemini:
				return gemini.New(workDir, model, &codexPTYAdapter{agentID: agentID, workDir: workDir, inner: ptyDaemon})
			case providerAgy:
				return agy.New(workDir, model, &codexPTYAdapter{agentID: agentID, workDir: workDir, inner: ptyDaemon})
			default:
				return nil, fmt.Errorf("operator: unknown provider %q", provider)
			}
		},
	}
	logger.Info("New: operator initialised", "memoryEnabled", op.memoryEnabled(), "sandboxEnabled", provisioner != nil)
	return op, nil
}

// memoryEnabled reports whether the agentMemory module should be invoked.
// Both the module and the feature flag must be active.
func (o *DefaultOperator) memoryEnabled() bool {
	return o.agentMemory != nil &&
		o.config.Features != nil &&
		o.config.Features.Memory
}

// DisoverCodeAgents checks which code-agent CLI binaries are present in PATH.
// For each available provider it calls codeagent.Discover() to enumerate models.
func (o *DefaultOperator) DisoverCodeAgents() (*operator.DisocveryResult, error) {
	logger.Debug("DisoverCodeAgents: scanning PATH for provider binaries")
	candidates := []struct {
		provider codeagent.Provider
		binary   string
	}{
		{providerClaude, "claude"},
		{providerCodex, "codex"},
		{providerAgy, "agy"},
		// {providerGemini, "gemini"}, // temporarily disabled
	}

	cwd, _ := os.Getwd()

	var providers []codeagent.Provider
	var providerModels []operator.ProviderModels

	for _, c := range candidates {
		if _, err := exec.LookPath(c.binary); err != nil {
			continue
		}
		logger.Debug("DisoverCodeAgents: provider available", "provider", c.provider, "binary", c.binary)

		// Attempt model discovery via the codeagent interface.
		if o.newCodeAgent != nil {
			ca, caErr := o.newCodeAgent(c.provider, cwd, "")
			if caErr == nil {
				// Auth check: skip providers that are not authenticated.
				identity := ca.GetUserIdentity()
				if !identity.Authenticated {
					logger.Info("DisoverCodeAgents: provider not authenticated, skipping", "provider", c.provider)
					continue
				}

				providers = append(providers, c.provider)
				if result, discErr := ca.Discover(); discErr == nil {
					models := make([]string, len(result.Models))
					for i, m := range result.Models {
						models[i] = string(m)
					}
					providerModels = append(providerModels, operator.ProviderModels{
						Provider: c.provider,
						Models:   models,
					})
					logger.Debug("DisoverCodeAgents: models discovered", "provider", c.provider, "count", len(models))
					continue
				}
			}
			logger.Debug("DisoverCodeAgents: model discovery unavailable, skipping", "provider", c.provider)
			continue
		}
		providers = append(providers, c.provider)
	}

	logger.Info("DisoverCodeAgents: completed", "providers", providers, "modelEntries", len(providerModels))
	return &operator.DisocveryResult{Providers: providers, Models: providerModels}, nil
}

// ListCodeAgents returns agents registered under the given workspace directory.
func (o *DefaultOperator) ListCodeAgents(params operator.GetCodeAgentsParams) (*operator.GetAgentsResult, error) {
	if err := params.Validate(); err != nil {
		logger.Error("ListCodeAgents: validation failed", "err", err)
		return nil, err
	}
	workspace, err := o.resolveWorkspace(params.Workspace)
	if err != nil {
		logger.Error("ListCodeAgents: workspace resolution failed", "workspace", params.Workspace, "err", err)
		return nil, err
	}
	logger.Debug("ListCodeAgents: listing agents", "workspace", workspace)
	agents, err := o.store.ListAgentsByDir(workspace)
	if err != nil {
		logger.Error("ListCodeAgents: store query failed", "workspace", workspace, "err", err)
		return nil, err
	}
	if len(agents) == 0 {
		logger.Info("ListCodeAgents: no agents found", "workspace", workspace)
		return &operator.GetAgentsResult{}, nil
	}
	logger.Info("ListCodeAgents: agents resolved", "workspace", workspace, "count", len(agents))
	return &operator.GetAgentsResult{Agents: agents}, nil
}

// CreateAgent creates a new agent entry, auto-creating the workspace when needed.
// When memory is enabled, the agent's memory directory is seeded with the initial template.
func (o *DefaultOperator) CreateAgent(params operator.CreateAgentParams) error {
	if err := params.Validate(); err != nil {
		logger.Error("CreateAgent: validation failed", "err", err)
		return err
	}
	workspace, err := o.resolveWorkspace(params.Workspace)
	if err != nil {
		logger.Error("CreateAgent: workspace resolution failed", "workspace", params.Workspace, "err", err)
		return err
	}
	logger.Info("CreateAgent: start", "workspace", workspace, "name", params.Name, "provider", params.Provider, "model", params.Model, "interactive", params.Interactive, "memoryEnabled", o.memoryEnabled())

	if params.ResumeIfExists {
		agentName := sanitizeAgentName(params.Name)
		if agentName != "" {
			agents, err := o.store.ListAgentsByDir(workspace)
			if err != nil {
				logger.Error("CreateAgent: existing agent lookup failed", "workspace", workspace, "err", err)
				return fmt.Errorf("operator: list existing agents: %w", err)
			}
			for _, existing := range agents {
				if existing.Name == agentName {
					logger.Info("CreateAgent: agent already exists, resuming instead of creating", "workspace", workspace, "name", agentName, "agentID", existing.ID)
					return o.ResumeAgent(operator.ResumeAgentParams{
						Workspace: workspace,
						Name:      agentName,
					})
				}
			}
		}
	}

	if o.memoryEnabled() && !memoryRootExists(string(workspace)) {
		logger.Info("CreateAgent: memory root missing, initialising workspace memory", "workspace", workspace)
		if err := o.agentMemory.Init(string(workspace), ""); err != nil {
			logger.Error("CreateAgent: auto team-init failed", "workspace", workspace, "err", err)
			return fmt.Errorf("operator: create agent: init memory root: %w", err)
		}
	}

	ws, err := o.store.WorkspaceByDir(workspace)
	if err != nil {
		logger.Debug("CreateAgent: workspace missing, creating new workspace", "workspace", workspace)
		ws, err = o.createWorkspaceWithAutoName(workspace, "", "localhost")
		if err != nil {
			logger.Error("CreateAgent: workspace creation failed", "workspace", workspace, "err", err)
			return fmt.Errorf("operator: create workspace: %w", err)
		}
		logger.Info("CreateAgent: workspace created", "workspaceID", ws.ID, "name", ws.Name, "workspace", ws.WorkspaceDir)
	}

	agentID := uuid.NewString()
	agentName := sanitizeAgentName(params.Name)
	if agentName == "" {
		agentName = fmt.Sprintf("agent-%s", agentID[:8])
	}

	// Reject duplicate names within the same workspace.
	existing, err := o.store.ListAgentsByDir(workspace)
	if err != nil {
		logger.Error("CreateAgent: name collision check failed", "workspace", workspace, "err", err)
		return fmt.Errorf("operator: create agent: check existing agents: %w", err)
	}
	for _, a := range existing {
		if a.Name == agentName {
			logger.Error("CreateAgent: duplicate name", "workspace", workspace, "name", agentName)
			return fmt.Errorf("operator: agent %q already exists in workspace", agentName)
		}
	}

	memDir := operator.AgentMemDir(string(workspace), agentName)

	agent := &omniagent.AgentInfo{
		ID:           agentID,
		Name:         agentName,
		WorkspaceDir: workspace,
		MemoryDir:    memDir,
	}
	if err := o.store.CreateAgent(agent); err != nil {
		logger.Error("CreateAgent: store insert failed", "workspaceID", ws.ID, "agentID", agentID, "err", err)
		return fmt.Errorf("operator: create agent (workspace %s): %w", ws.ID, err)
	}
	if o.agentStore != nil {
		if err := o.agentStore.UpdateSettings(agentID, &omniagent.Settings{}); err != nil {
			logger.Warn("CreateAgent: settings init failed", "agentID", agentID, "err", err)
		}
	}
	logger.Info("CreateAgent: agent stored", "workspaceID", ws.ID, "agentID", agentID, "memoryDir", memDir)

	if o.memoryEnabled() {
		if err := o.agentMemory.Create(memDir); err != nil {
			logger.Error("CreateAgent: memory seed failed", "agentID", agentID, "memoryDir", memDir, "err", err)
			return fmt.Errorf("operator: create agent: seed memory: %w", err)
		}
		logger.Info("CreateAgent: memory seeded", "agentID", agentID, "memoryDir", memDir)
	}

	if err := o.startAgentSession(agent, params.Provider, params.Model, params.Interactive, params.SessionID); err != nil {
		logger.Warn("CreateAgent: session bootstrap failed — agent persisted; use 'omni agent resume' to start session", "agentID", agentID, "provider", params.Provider, "model", params.Model, "err", err)
	}
	logger.Info("CreateAgent: completed", "workspaceID", ws.ID, "agentID", agentID)
	return nil
}

// TeamInit initialises the workspace memory root.
// Returns ErrMemoryDisabled when the memory feature is off.
func (o *DefaultOperator) TeamInit(params operator.TeamInitParams) error {
	if err := params.Validate(); err != nil {
		logger.Error("TeamInit: validation failed", "err", err)
		return err
	}
	workspace, err := o.resolveWorkspace(params.Workspace)
	if err != nil {
		logger.Error("TeamInit: workspace resolution failed", "workspace", params.Workspace, "err", err)
		return err
	}
	logger.Info("TeamInit: start", "workspace", workspace, "repoURL", params.RepoURL, "memoryEnabled", o.memoryEnabled())
	if !o.memoryEnabled() {
		logger.Error("TeamInit: memory feature disabled", "workspace", workspace)
		return operator.ErrMemoryDisabled
	}
	if err := o.agentMemory.Init(string(workspace), params.RepoURL); err != nil {
		logger.Error("TeamInit: initialisation failed", "workspace", workspace, "err", err)
		return fmt.Errorf("operator: team-init: %w", err)
	}
	if err := o.ensureWorkspaceAndGuideAgent(workspace, params.Name, params.Remote); err != nil {
		logger.Error("TeamInit: guide agent initialisation failed", "workspace", workspace, "err", err)
		return fmt.Errorf("operator: team-init: ensure guide agent: %w", err)
	}
	logger.Info("TeamInit: completed", "workspace", workspace)
	return nil
}

// ListTemplates returns the short-name (version field) of every embedded agent template.
func (o *DefaultOperator) ListTemplates() ([]string, error) {
	return operator.ListTemplateVersions()
}

// DoctorTerminals checks whether each registered terminal provider binary is present.
// Terminal provider registry is not yet wired — returns an empty result.
func (o *DefaultOperator) DoctorTerminals() (*operator.DoctorTerminalsResult, error) {
	return &operator.DoctorTerminalsResult{}, nil
}

// InstallTerminal runs the install procedure for the named terminal provider.
// Terminal provider registry is not yet wired.
func (o *DefaultOperator) InstallTerminal(name string) error {
	return fmt.Errorf("operator: install terminal: provider %q not registered", name)
}

// UpgradeAgent applies a newer template version to an existing agent's memory directory.
// Returns ErrMemoryDisabled when the memory feature is off.
func (o *DefaultOperator) UpgradeAgent(params operator.UpgradeAgentParams) error {
	if err := params.Validate(); err != nil {
		logger.Error("UpgradeAgent: validation failed", "err", err)
		return err
	}
	logger.Info("UpgradeAgent: start", "agentID", params.ID, "version", params.Version, "memoryEnabled", o.memoryEnabled())
	if !o.memoryEnabled() {
		logger.Error("UpgradeAgent: memory feature disabled", "agentID", params.ID)
		return operator.ErrMemoryDisabled
	}
	agent, err := o.store.GetAgent(params.ID)
	if err != nil {
		logger.Error("UpgradeAgent: agent lookup failed", "agentID", params.ID, "err", err)
		return fmt.Errorf("operator: upgrade agent %q: %w", params.ID, err)
	}
	version := params.Version
	if version == "" {
		version = operator.LatestVersion
	}
	if err := o.agentMemory.Upgrade(agent.MemoryDir, version); err != nil {
		logger.Error("UpgradeAgent: template upgrade failed", "agentID", params.ID, "version", version, "memoryDir", agent.MemoryDir, "err", err)
		return fmt.Errorf("operator: upgrade agent %q: %w", params.ID, err)
	}
	logger.Info("UpgradeAgent: completed", "agentID", params.ID, "version", version, "memoryDir", agent.MemoryDir)
	return nil
}

// ListWorkspaces returns all workspaces with their agent counts.
func (o *DefaultOperator) ListWorkspaces(_ operator.ListWorkspacesParams) (operator.ListWorkspacesResult, error) {
	logger.Debug("ListWorkspaces: loading workspaces")
	teams, err := o.store.ListWorkspaces()
	if err != nil {
		logger.Error("ListWorkspaces: store query failed", "err", err)
		return operator.ListWorkspacesResult{}, err
	}
	logger.Info("ListWorkspaces: completed", "count", len(teams))
	return operator.ListWorkspacesResult{Teams: teams}, nil
}

// GetWorkspace returns a workspace and all agents registered under it.
func (o *DefaultOperator) GetWorkspace(params operator.GetWorkSpaceParams) (operator.GetTeamResult, error) {
	if err := params.Validate(); err != nil {
		logger.Error("GetWorkspace: validation failed", "err", err)
		return operator.GetTeamResult{}, err
	}
	logger.Debug("GetWorkspace: loading workspace", "workspaceID", params.ID)
	ws, err := o.store.GetWorkspace(params.ID)
	if err != nil {
		logger.Error("GetWorkspace: workspace lookup failed", "workspaceID", params.ID, "err", err)
		return operator.GetTeamResult{}, fmt.Errorf("operator: get workspace %q: %w", params.ID, err)
	}
	agents, err := o.store.ListAgentsByDir(o.workspaceDirOf(ws))
	if err != nil {
		logger.Error("GetWorkspace: agent listing failed", "workspaceID", params.ID, "workspace", ws.WorkspaceDir, "err", err)
		return operator.GetTeamResult{}, fmt.Errorf("operator: list agents for workspace %q: %w", params.ID, err)
	}
	logger.Info("GetWorkspace: completed", "workspaceID", params.ID, "agentCount", len(agents))
	return operator.GetTeamResult{Info: ws, Agents: agents}, nil
}

// DeleteAgent removes an agent entry from the index.
// When memory is enabled, the agent's memory directory is also deleted from disk.
func (o *DefaultOperator) DeleteAgent(params operator.DeleteAgentParams) error {
	if err := params.Validate(); err != nil {
		logger.Error("DeleteAgent: validation failed", "err", err)
		return err
	}
	logger.Info("DeleteAgent: start", "agentID", params.ID, "memoryEnabled", o.memoryEnabled())
	if o.memoryEnabled() {
		agent, err := o.store.GetAgent(params.ID)
		if err == nil {
			if deleteErr := o.agentMemory.Delete(agent.MemoryDir); deleteErr != nil {
				logger.Error("DeleteAgent: memory delete failed", "agentID", params.ID, "memoryDir", agent.MemoryDir, "err", deleteErr)
			} else {
				logger.Info("DeleteAgent: memory deleted", "agentID", params.ID, "memoryDir", agent.MemoryDir)
			}
		} else {
			logger.Debug("DeleteAgent: memory delete skipped, agent lookup failed", "agentID", params.ID, "err", err)
		}
	}
	if err := o.store.DeleteAgent(params.ID); err != nil {
		logger.Error("DeleteAgent: store delete failed", "agentID", params.ID, "err", err)
		return err
	}
	if o.agentStore != nil {
		if err := o.agentStore.DeleteAgent(params.ID); err != nil {
			logger.Warn("DeleteAgent: agent store sync failed", "agentID", params.ID, "err", err)
		}
	}
	logger.Info("DeleteAgent: completed", "agentID", params.ID)
	return nil
}

// workspaceDirOf converts a TeamInfo directory string to a WorkspaceDir.
func (o *DefaultOperator) workspaceDirOf(ws *operator.TeamInfo) sandbox.WorkspaceDir {
	return sandbox.WorkspaceDir(ws.WorkspaceDir)
}

func (o *DefaultOperator) resolveWorkspace(in sandbox.WorkspaceDir) (sandbox.WorkspaceDir, error) {
	if in != "" {
		return in, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("operator: resolve workspace: getwd: %w", err)
	}
	if resolved, ok := findWorkspaceFromMemory(cwd); ok {
		logger.Debug("resolveWorkspace: resolved from ancestor memory root", "cwd", cwd, "workspace", resolved)
		return sandbox.WorkspaceDir(resolved), nil
	}
	logger.Debug("resolveWorkspace: defaulting to cwd", "cwd", cwd)
	return sandbox.WorkspaceDir(cwd), nil
}

func findWorkspaceFromMemory(start string) (string, bool) {
	dir := filepath.Clean(start)
	for {
		if memoryRootExists(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func memoryRootExists(workspace string) bool {
	info, err := os.Stat(filepath.Join(workspace, operator.MemoryDirName))
	return err == nil && info.IsDir()
}

func sanitizeAgentName(name string) string {
	return strings.TrimSpace(name)
}

func isSessionNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no session") || strings.Contains(msg, "session not found")
}

func resolvedSessionModel(ca codeagent.CodeAgent, fallback string) string {
	if fallback != "" || ca == nil {
		return fallback
	}
	cfg, err := ca.GetSessionConfig(codeagent.GetSessionConfigParams{})
	if err != nil || cfg == nil {
		return fallback
	}
	return strings.TrimSpace(cfg.Model.Model)
}

func mcpSessionEnvs(agent *omniagent.AgentInfo) []string {
	if agent == nil {
		return nil
	}
	workspace := strings.TrimSpace(string(agent.WorkspaceDir))
	senderID := strings.TrimSpace(agent.Name)
	envs := []string{
		"AXO_LINK_MCP_AUTH_TOKEN=" + mcpAuthToken,
		"AXO_LINK_MCP_SENDER_ID=" + senderID,
		"AXO_LINK_MCP_SENDER_TYPE=" + mcpSenderType,
		"AXO_LINK_MCP_AGENT_WORKSPACE=" + workspace,
	}
	logger.Debug("mcpSessionEnvs: built session envs", "agentID", agent.ID, "senderID", senderID, "workspace", workspace, "envKeys", []string{"AXO_LINK_MCP_AUTH_TOKEN", "AXO_LINK_MCP_SENDER_ID", "AXO_LINK_MCP_SENDER_TYPE", "AXO_LINK_MCP_AGENT_WORKSPACE"})
	return envs
}

func (o *DefaultOperator) startAgentSession(agent *omniagent.AgentInfo, provider codeagent.Provider, model string, interactive bool, requestedSessionID string) error {
	if provider == "" {
		provider = codeagent.Provider(operator.DefaultProvider)
	}

	ca, err := o.createCodeAgent(agent, provider, string(agent.WorkspaceDir), model)
	if err != nil {
		return fmt.Errorf("operator: init code agent: %w", err)
	}

	// Provision a sandbox runtime for this session when the provisioner is available.
	var sbxRuntime *sandbox.SandboxRuntime
	if o.provisioner != nil {
		workDir := string(agent.WorkspaceDir)
		cfg := defaults.SandboxConfig(provider, workDir)
		configDir := sandboxConfigDir(workDir, agent.Name)
		rt, sbxErr := o.provisioner.Create(sandbox.CreateSandboxParams{
			ID:        agent.ID,
			ConfigDir: configDir,
			Config:    cfg,
		})
		if sbxErr != nil {
			logger.Warn("startAgentSession: sandbox provision failed", "agentID", agent.ID, "err", sbxErr)
		} else {
			sbxRuntime = &rt
			logger.Debug("startAgentSession: sandbox runtime provisioned", "agentID", agent.ID, "provider", provider, "configDir", configDir)
		}
	}

	requestedSessionID = strings.TrimSpace(requestedSessionID)
	createID := agent.ID
	if requestedSessionID != "" {
		createID = requestedSessionID
	}
	envs := mcpSessionEnvs(agent)
	createResult, err := ca.Create(codeagent.CreateSessionParams{
		ID:        createID,
		Name:      agent.Name,
		Model:     model,
		WorkDir:   string(agent.WorkspaceDir),
		SessionID: requestedSessionID,
		Envs:      envs,
		RunTime:   sbxRuntime,
	})
	if err != nil {
		return fmt.Errorf("operator: create session for agent %q: %w", agent.ID, err)
	}

	sessionID := createID
	if createResult != nil && createResult.ID != "" {
		sessionID = createResult.ID
	}
	model = resolvedSessionModel(ca, model)

	// Persist the session to the code session store.
	if o.sessionStore != nil {
		if err := o.sessionStore.CreateSession(agent.ID, &omniagent.CodeSession{
			Id:       sessionID,
			Model:    &codeagent.Model{Provider: provider, Model: model},
			IsActive: true,
		}); err != nil {
			logger.Warn("startAgentSession: session store sync failed", "agentID", agent.ID, "sessionID", sessionID, "err", err)
		}
	}
	logger.Info("startAgentSession: session created", "agentID", agent.ID, "provider", provider, "sessionID", sessionID, "interactive", interactive)

	if !interactive {
		logger.Info("startAgentSession: interactive session is not attached", "agentID", agent.ID, "provider", provider, "sessionID", sessionID)
		return nil
	}

	resumeCtx, cancelResume := newResumeContext()
	defer cancelResume()
	resumeResult, err := ca.Resume(codeagent.ResumeSessionParams{Context: resumeCtx, ID: sessionID, SessionID: requestedSessionID, Envs: envs})
	if err != nil {
		return fmt.Errorf("operator: resume session for agent %q: %w", agent.ID, err)
	}
	if err := waitResumeLifecycle(resumeCtx, resumeResult); err != nil {
		return err
	}
	logger.Info("startAgentSession: interactive session resumed", "agentID", agent.ID, "provider", provider, "sessionID", sessionID)
	return nil
}

var slugWords = []string{
	"amber", "arctic", "azure", "bold", "bright", "calm", "cedar", "clear",
	"coral", "dawn", "deep", "dusk", "echo", "ember", "fern", "field",
	"flame", "fleet", "frost", "grove", "jade", "kind", "leaf", "lunar",
	"maple", "mesa", "mist", "nova", "oak", "pine", "reef", "river",
	"sage", "sky", "solar", "stone", "swift", "teal", "tide", "vale",
	"warm", "wind", "wolf", "zenith",
}

func wordSlug(n int) string {
	words := make([]string, n)
	for i := range words {
		words[i] = slugWords[rand.Intn(len(slugWords))]
	}
	return strings.Join(words, "-")
}

// createWorkspaceWithAutoName creates a workspace record, defaulting name to the
// folder basename and remote to "localhost". On a (remote, name) UNIQUE conflict
// it retries once with a 5-word slug appended to the name.
func (o *DefaultOperator) createWorkspaceWithAutoName(workspace sandbox.WorkspaceDir, name, remote string) (*operator.TeamInfo, error) {
	if name == "" {
		name = filepath.Base(string(workspace))
	}
	if remote == "" {
		remote = "localhost"
	}
	ws := &operator.TeamInfo{
		ID:           uuid.NewString(),
		Name:         name,
		Remote:       remote,
		WorkspaceDir: string(workspace),
	}
	if err := o.store.CreateWorkspace(ws); err == nil {
		return ws, nil
	} else if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return nil, err
	}
	// Name conflict on this remote — retry with a 5-word slug.
	ws.ID = uuid.NewString()
	ws.Name = name + "-" + wordSlug(5)
	if err := o.store.CreateWorkspace(ws); err != nil {
		return nil, err
	}
	return ws, nil
}

func (o *DefaultOperator) ensureWorkspaceAndGuideAgent(workspace sandbox.WorkspaceDir, name, remote string) error {
	if _, err := o.store.WorkspaceByDir(workspace); err != nil {
		if _, err = o.createWorkspaceWithAutoName(workspace, name, remote); err != nil {
			return err
		}
	}

	agents, err := o.store.ListAgentsByDir(workspace)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if agent.Name == "guide" {
			return nil
		}
	}

	guide := &omniagent.AgentInfo{
		ID:           uuid.NewString(),
		Name:         "guide",
		WorkspaceDir: workspace,
		MemoryDir:    operator.AgentMemDir(string(workspace), "guide"),
	}
	if err := o.store.CreateAgent(guide); err != nil {
		return err
	}
	if o.agentStore != nil {
		if err := o.agentStore.Create(&omniagent.Data{Info: guide}); err != nil {
			logger.Warn("ensureWorkspaceAndGuideAgent: agent store sync failed", "agentID", guide.ID, "err", err)
		}
	}
	if o.memoryEnabled() {
		if err := o.agentMemory.Create(guide.MemoryDir); err != nil {
			return err
		}
	}
	return nil
}

// sandboxConfigDir returns the per-agent sandbox config directory:
// <workspaceDir>/memory/agents/<agentName>/sandbox
func sandboxConfigDir(workspaceDir, agentName string) string {
	if workspaceDir == "" || agentName == "" {
		return ""
	}
	return filepath.Join(workspaceDir, operator.MemoryDirName, "agents", agentName, "sandbox")
}

// GetCodeAgentResolver returns the SettingsResolver for the given provider.
// Claude and Codex use their settings sub-packages directly.
// Gemini has no exported settings constructor and returns ErrResolverUnavailable.
func (o *DefaultOperator) GetCodeAgentResolver(agent codeagent.Provider) (*codeagent.SettingsResolver, error) {
	logger.Debug("GetCodeAgentResolver: resolving provider", "provider", agent)
	var r codeagent.SettingsResolver
	switch agent {
	case providerClaude:
		r = claudesettings.New(providerClaude)
	case providerCodex:
		r = codexsettings.New(providerCodex)
	case providerAgy:
		r = agysettings.New(providerAgy)
	case providerGemini:
		logger.Error("GetCodeAgentResolver: resolver unavailable", "provider", providerGemini)
		return nil, fmt.Errorf("operator: %w: %s", operator.ErrResolverUnavailable, providerGemini)
	default:
		logger.Error("GetCodeAgentResolver: unknown provider", "provider", agent)
		return nil, fmt.Errorf("operator: unknown provider %q", agent)
	}
	logger.Info("GetCodeAgentResolver: resolver ready", "provider", agent)
	return &r, nil
}
