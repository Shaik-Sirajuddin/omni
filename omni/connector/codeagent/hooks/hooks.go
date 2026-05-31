package hooks

import sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"

type HookID string

// Hooks is the full set of declared hook IDs.
var Hooks = []HookID{
	PreToolUse,
	PostToolUse,
	PostToolUseFailure,
	PrePrompt,
	PostPrompt,
	SessionStart,
	SessionEnd,
}

type Capabilities struct {
	PrePrompt          bool
	PostPrompt         bool
	PreToolUse         bool
	PostToolUse        bool
	PostToolUseFailure bool
	SessionStart       bool
	SessionEnd         bool
}

// type Hook struct {
// 	Input  *HookInput
// 	Output *HookOuput
// }

// OmniAgent is the agent context injected into every enriched hook payload.
type OmniAgent struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Model  string `json:"model,omitempty"`
	Status string `json:"status,omitempty"`
}

// OmniContext is the top-level omni field added to every enriched hook payload.
// It is nil when the hook-operator could not resolve session/agent context.
type OmniContext struct {
	Agent     OmniAgent            `json:"agent"`
	Workspace sandbox.WorkspaceDir `json:"workspace,omitempty"`
}

// HookInput holds fields present in every provider's hook input payload.
type HookInput struct {
	SessionID      string       `json:"session_id"`
	TranscriptPath string       `json:"transcript_path"`
	Cwd            string       `json:"cwd"`
	HookEventName  HookID       `json:"hook_event_name"`
	Omni           *OmniContext `json:"omni,omitempty"`
}

// HookOuput holds fields present in every provider's hook output payload.
type HookOuput struct {
	Continue       bool    `json:"continue"`
	StopReason     *string `json:"stopReason,omitempty"`
	SuppressOutput bool    `json:"suppressOutput"`
	SystemMessage  *string `json:"systemMessage,omitempty"`
}
