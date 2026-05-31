package confhooks

import (
	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
)

// HookSchema is the marker interface for per-event schema bundles.
// Callers use Go type assertion to get the concrete event type:
//
//	if s, ok := hook.Schemas.(*PreToolUseSchema); ok { ... }
type HookSchema interface {
	EventName() string
}

// Hook is a named hook registration at the omni layer.
// Schemas carries the event-specific schema so callers can type-assert
// to the concrete *EventSchema to access typed Input, ResponseSchema, and ResultSchema.
type Hook struct {
	Name    string
	Entry   config.HookEntry
	Schemas HookSchema
}

// EventBase fields present in every hook payload from any codeagent.
type EventBase struct {
	SessionID      string `json:"session_id"       jsonschema:"title=Session ID,description=Unique identifier for the agent session"`
	TranscriptPath string `json:"transcript_path"  jsonschema:"title=Transcript Path,description=Absolute path to the session transcript file"`
	Cwd            string `json:"cwd"              jsonschema:"title=Working Directory,description=Working directory of the agent process"`
	HookEventName  string `json:"hook_event_name"  jsonschema:"title=Hook Event Name,description=Name of the hook event that fired"`
}

// Response is the canonical fields shared by every hook response.
type Response struct {
	Continue       bool    `json:"continue"                  jsonschema:"title=Continue,description=Whether the agent should continue; semantics are inverted for Stop (true overrides stop)"`
	StopReason     *string `json:"stop_reason,omitempty"     jsonschema:"title=Stop Reason,description=Optional reason string passed to the agent when stopping"`
	SuppressOutput bool    `json:"suppress_output"           jsonschema:"title=Suppress Output,description=Hide tool result or response from downstream consumers"`
	SystemMessage  *string `json:"system_message,omitempty"  jsonschema:"title=System Message,description=Text injected into the agent context alongside the hook response"`
}

// HookResponseSchema is what omni sends back to the codeagent after processing a hook.
// The codeagent reads this and acts on it (block tool, abort session, modify context).
// Applies to response hooks: PreToolUse, PreSessionStart, PrePrompt.
type HookResponseSchema struct {
	EventName string
	Response  Response
}

// HookResultSchema is the parsed result from a hook command's stdout.
// Represents what the hook subprocess decided and wrote back.
// Applies to result hooks: PostToolUse, PostToolUseFailure, PostSessionStart, PostPrompt.
type HookResultSchema struct {
	EventName string
	Result    Response
}

// ── PreToolUse ────────────────────────────────────────────────────────────────

type PreToolUseInput struct {
	EventBase
	ToolName  string         `json:"tool_name"  jsonschema:"title=Tool Name,description=Name of the tool being called"`
	ToolInput map[string]any `json:"tool_input"  jsonschema:"title=Tool Input,description=Arguments passed to the tool"`
	ToolUseID string         `json:"tool_use_id" jsonschema:"title=Tool Use ID,description=Unique identifier for this tool invocation"`
}

// PreToolUseResponseSchema — response hook.
// Continue=false blocks the tool call entirely.
// SystemMessage is injected into context before the block.
type PreToolUseResponseSchema struct{ Response }

// PreToolUseResultSchema — what the hook command writes to stdout.
type PreToolUseResultSchema struct{ Response }

type PreToolUseSchema struct {
	Input          PreToolUseInput
	ResponseSchema PreToolUseResponseSchema
	ResultSchema   PreToolUseResultSchema
}

func (s *PreToolUseSchema) EventName() string { return string(hooks.PreToolUse) }

// ── PostToolUse ───────────────────────────────────────────────────────────────

type PostToolUseInput struct {
	EventBase
	ToolName     string         `json:"tool_name"     jsonschema:"title=Tool Name,description=Name of the tool that was called"`
	ToolInput    map[string]any `json:"tool_input"    jsonschema:"title=Tool Input,description=Arguments that were passed to the tool"`
	ToolUseID    string         `json:"tool_use_id"   jsonschema:"title=Tool Use ID,description=Unique identifier for this tool invocation"`
	ToolResponse any            `json:"tool_response" jsonschema:"title=Tool Response,description=Output returned by the tool"`
}

// PostToolUseResponseSchema — response hook.
// SuppressOutput=true hides tool result from agent context.
type PostToolUseResponseSchema struct{ Response }

// PostToolUseResultSchema — what the hook command writes to stdout.
type PostToolUseResultSchema struct{ Response }

type PostToolUseSchema struct {
	Input          PostToolUseInput
	ResponseSchema PostToolUseResponseSchema
	ResultSchema   PostToolUseResultSchema
}

func (s *PostToolUseSchema) EventName() string { return string(hooks.PostToolUse) }

// ── PostToolUseFailure ────────────────────────────────────────────────────────

type PostToolUseFailureInput struct {
	EventBase
	ToolName  string         `json:"tool_name"  jsonschema:"title=Tool Name,description=Name of the tool that failed"`
	ToolInput map[string]any `json:"tool_input"  jsonschema:"title=Tool Input,description=Arguments that were passed to the tool"`
	ToolUseID string         `json:"tool_use_id" jsonschema:"title=Tool Use ID,description=Unique identifier for this tool invocation"`
	Error     string         `json:"error"       jsonschema:"title=Error,description=Error message returned by the tool"`
}

// PostToolUseFailureResponseSchema — response hook.
// Continue=false stops session after failure; StopReason names why.
type PostToolUseFailureResponseSchema struct{ Response }

// PostToolUseFailureResultSchema — what the hook command writes to stdout.
type PostToolUseFailureResultSchema struct{ Response }

type PostToolUseFailureSchema struct {
	Input          PostToolUseFailureInput
	ResponseSchema PostToolUseFailureResponseSchema
	ResultSchema   PostToolUseFailureResultSchema
}

func (s *PostToolUseFailureSchema) EventName() string { return string(hooks.PostToolUseFailure) }

// ── PreSessionStart ───────────────────────────────────────────────────────────

type SessionStartInput struct {
	EventBase
	Source string `json:"source" jsonschema:"title=Source,description=Reason the session is starting — one of: startup | resume | clear | compact"`
}

// SessionStartResponseSchema — response hook.
// Continue=false aborts session creation before it starts.
// SystemMessage is prepended as the first system turn.
type SessionStartResponseSchema struct{ Response }

// SessionStartResultSchema — what the hook command writes to stdout.
type SessionStartResultSchema struct{ Response }

type SessionStartSchema struct {
	Input          SessionStartInput
	ResponseSchema SessionStartResponseSchema
	ResultSchema   SessionStartResultSchema
}

func (s *SessionStartSchema) EventName() string { return string(hooks.SessionStart) }

// ── PostSessionStart ──────────────────────────────────────────────────────────

type SessionEndInput struct {
	EventBase
	// No extra fields — session is already live.
}

// SessionEndResponseSchema — response hook.
// SystemMessage appended after session starts (e.g. inject memory context).
type SessionEndResponseSchema struct{ Response }

// SessionEndResultSchema — what the hook command writes to stdout.
type SessionEndResultSchema struct{ Response }

type SessionEndSchema struct {
	Input          SessionEndInput
	ResponseSchema SessionEndResponseSchema
	ResultSchema   SessionEndResultSchema
}

func (s *SessionEndSchema) EventName() string { return string(hooks.SessionEnd) }

// ── PrePrompt ─────────────────────────────────────────────────────────────────

type PrePromptInput struct {
	EventBase
	Prompt string `json:"prompt" jsonschema:"title=Prompt,description=Raw user prompt before the agent sees it"`
}

// PrePromptResponseSchema — response hook.
// Continue=false rejects the prompt before submission.
// SystemMessage is injected alongside the prompt as context.
type PrePromptResponseSchema struct{ Response }

// PrePromptResultSchema — what the hook command writes to stdout.
type PrePromptResultSchema struct{ Response }

type PrePromptSchema struct {
	Input          PrePromptInput
	ResponseSchema PrePromptResponseSchema
	ResultSchema   PrePromptResultSchema
}

func (s *PrePromptSchema) EventName() string { return string(hooks.PrePrompt) }

// ── PostPrompt ────────────────────────────────────────────────────────────────

type PostPromptInput struct {
	EventBase
	Prompt      string `json:"prompt"   jsonschema:"title=Prompt,description=Original user prompt"`
	AgentAnswer string `json:"response" jsonschema:"title=Agent Answer,description=Agent's final answer for this turn"` // named AgentAnswer to avoid clash with Response type
}

// PostPromptResponseSchema — response hook.
// Continue=false stops the session after this turn.
// SuppressOutput=true hides response from downstream consumers.
type PostPromptResponseSchema struct{ Response }

// PostPromptResultSchema — what the hook command writes to stdout.
type PostPromptResultSchema struct{ Response }

type PostPromptSchema struct {
	Input          PostPromptInput
	ResponseSchema PostPromptResponseSchema
	ResultSchema   PostPromptResultSchema
}

func (s *PostPromptSchema) EventName() string { return string(hooks.PostPrompt) }
