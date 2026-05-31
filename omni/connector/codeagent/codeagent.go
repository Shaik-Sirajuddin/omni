package codeagent

import (
	"context"

	"github.com/Shaik-Sirajuddin/memory/config"
	confhooks "github.com/Shaik-Sirajuddin/memory/config/hooks"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
)

type Provider string

type Model struct {
	Provider Provider
	Model    string
}

// --- Execute ---

type ExecuteParams struct {
	PromptId     string
	Prompt       string
	OutputFormat OutputFormat
	MaxTurns     int
}

type Usage struct {
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

type ExecuteResult struct {
	PromptID   string
	SessionID  string
	Response   string
	StopReason string
	Usage      *Usage
}

// --- Stream ---

type StreamParams struct {
	PromptId string
	Prompt   string
	MaxTurns int
}

type StreamEvent struct {
	Type    string // text | tool_use | tool_result | stop
	Content string
	Done    bool
}

type StreamResult struct {
	Events    <-chan StreamEvent
	SessionID string
}

// --- Capabilities & Config ---

type Capabilities struct {
	Hooks      *hooks.Capabilities
	Streaming  bool
	MCPSupport bool
	Worktrees  bool
	Subagents  bool
}

// --- Info & Identity ---

type CodeAgentInfo struct {
	Provider Provider
	Name     string
	Version  string
}

type UserIdentify struct {
	Authenticated bool
	Email         string
	DisplayName   string
}

type ConfigPaths struct {
	GlobalConfigDirs    []string
	WorkspaceConfigDirs []string
	Binary              []string
}

type ModelID string

type ModelInfo struct {
	ID        string
	Reasoning string
}

type DiscoverResult struct {
	Models []ModelID
}

type PTYTerminalInfo struct {
	AgentID   string
	SessionID string
	Status    string
}

// PTYClient is the minimal interface each connector needs to send
// prompts into an active PTY session via the PTY daemon.
type PTYClient interface {
	Pipe(agentID, sessionID string, data []byte) error
	Start(sessionID string, command []string, env []string, dir, submitKey string) error
	Attach(ctx context.Context, sessionID string) error
	Exec(sessionID, input string) error
	Stop(sessionID string) error
	StopSafe(sessionID string, force bool) error
	Detach(sessionID string) error
	List(agentID string) ([]*PTYTerminalInfo, error)
	Get(agentID, sessionID string) (*PTYTerminalInfo, error)
}

// HookTransformer translates between omni-layer hooks and codeagent-specific formats.
// Each connector (claude, codex, gemini) provides its own implementation.
type HookTransformer interface {
	// Add registers a hook by name. Returns false without storing if name already exists.
	// Key = exact name string (case-sensitive). No update — remove then re-add.
	Add(name string, entry config.HookEntry) bool

	// GetHooks returns all registered hooks in insertion order.
	// Hook.Schemas is the concrete *EventSchema for type assertion.
	GetHooks() []confhooks.Hook

	// GetHookResponse parses a codeagent-specific payload and returns what omni
	// sends back to the codeagent. Covers all 7 events.
	GetHookResponse(eventName string, payload any) (confhooks.HookResponseSchema, error)

	// GetHookResult parses a hook command's stdout into the canonical result.
	// Covers all 7 events.
	GetHookResult(eventName string, raw any) (confhooks.HookResultSchema, error)
}

// CodeAgent implements Model
// CodeAgent Provides access to sessions
// All operations of CodeAgent are concurrent safe
type CodeAgent interface {
	hooks.HookIOParser
	hooks.HookManager
	SettingsResolver

	// Create a non-interactive session and return its ID.
	// Implementor must pass Envs (KEY=VALUE pairs) to the launched process environment.
	Create(CreateSessionParams) (*CreateSessionResult, error)
	// Exec runs a prompt to completion and returns the full response.
	Exec(ExecuteParams) (*ExecuteResult, error)
	// Stream runs a prompt and returns a channel of incremental events.
	Stream(StreamParams) (*StreamResult, error)
	// Resume the session and return the process ID; defaults to interactive.
	// When PTYClient is set: connector calls PTYClient.Start then PTYClient.Attach
	// (blocking until terminal detaches). Operator must not call Start/Attach separately.
	// When PTYClient is nil: blocking /dev/tty mode.
	// Implementor must pass Envs (KEY=VALUE pairs) to the launched process environment.
	Resume(ResumeSessionParams) (*ResumeSessionResult, error)
	// List returns persisted sessions matching the given filter.
	List(ListSessionsParams) (*ListSessionsResult, error)
	// Delete removes a persisted session.
	Delete(DeleteSessionParams) (*DeleteSessionResult, error)

	GetSessionConfig(GetSessionConfigParams) (*GetSessionConfigResult, error)
	GetSessionSandbox(GetSessionSandboxParams) (*GetSessionSandboxResult, error)
	UpdateSessionSandbox(UpdateSessionSandboxParams) (*UpdateSessionSandboxResult, error)

	Capabilities() (*Capabilities, error)

	Info() *CodeAgentInfo
	GetUserIdentity() UserIdentify
	// Stop terminates the active interactive session.
	Stop()

	// SetPTYClient wires the PTY daemon client used by ExecInSession.
	// May be called after construction; safe for concurrent use.
	SetPTYClient(PTYClient)

	// ExecInSession sends a prompt into an active interactive PTY session.
	// Fire-and-forget: returns immediately, does not collect output.
	// Returns error if session is not live or not a PTY session.
	ExecInSession(ExecInSessionParams) (*ExecInSessionResult, error)

	// Discover returns available models
	// else returns default model
	Discover() (DiscoverResult, error)
}

// TODO : all codeagent should accept a *sandbox.Config in new

// CodeAgentTakes a shell with new command where it can execute
