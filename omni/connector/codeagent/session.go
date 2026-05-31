package codeagent

import (
	"context"

	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

// Session holds metadata about a persisted agent session.
type Session struct {
	ID       string
	Name     string
	Provider Provider
	Model    string
	WorkDir  string
}

// --- Create ---
type CreateSessionParams struct {
	ID             string
	ParentID       string
	Model          string
	Name           string
	WorkDir        string
	PermissionMode PermissionMode
	SystemPrompt   string
	SessionID      string
	Envs           []string
	RunTime        *sandbox.SandboxRuntime
}

type CreateSessionResult struct {
	ID   string
	Name string
}

// --- Resume ---
type ResumeSessionParams struct {
	Context     context.Context
	ID          string
	ForkSession bool
	SessionID   string
	Detached    bool
	Envs        []string
	RunTime     *sandbox.SandboxRuntime
}

type ResumeSessionResult struct {
	ProcessID string
	SessionID string
	Done      <-chan error
}

// --- List Sessions ---

type ListSessionsParams struct {
	WorkDir  string
	Provider Provider
}

type ListSessionsResult struct {
	Sessions []*Session
}

// --- Delete Session ---

type DeleteSessionParams struct {
	ID string
}

type DeleteSessionResult struct {
	Deleted bool
}

// --- Session Config ---

type GetSessionConfigParams struct {
	ID string
}

type GetSessionConfigResult struct {
	Model          Model
	PermissionMode PermissionMode
	WorkDir        string
	SystemPrompt   string
}

// --- Sandbox ---

type UpdateSessionSandboxParams struct {
	Sandbox *sandbox.Config
}
type UpdateSessionSandboxResult struct {
	Sandbox *sandbox.Config
}

type GetSessionSandboxParams struct {
	ID string
}

type GetSessionSandboxResult struct {
	Sandbox *sandbox.Config
}

// --- ExecInSession ---

type ExecInSessionParams struct {
	SessionID string
	Prompt    string
}

type ExecInSessionResult struct {
	SessionID string
}
