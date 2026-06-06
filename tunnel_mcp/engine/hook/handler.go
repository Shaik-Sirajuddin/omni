package hook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
)

// Engine is the callback interface HookHandler uses to drive the ProcessingEngine.
type Engine interface {
	OnPreSessionStart(agentID, agentName, sessionID, cwd string)
	OnPreToolUse(agentID, sessionID, toolName string, toolInput map[string]any)
	// OnPostToolUse fires after a tool completes successfully. Delivery-confirming
	// (mandatory) tools are recognised here, not in OnPreToolUse, so a tool call that
	// errors does not falsely confirm delivery.
	OnPostToolUse(agentID, sessionID, toolName string, toolInput map[string]any)
	OnUserPromptSubmit(ctx context.Context, agentID, sessionID, prompt string)
	// OnStop returns an optional recall prompt to inject as systemMessage when the
	// mandatory tool was not invoked and retries remain. Nil means no injection.
	OnStop(ctx context.Context, agentID, sessionID string) *string
	OnPostToolUseFailure(agentID, sessionID, toolName, errMsg string)
}

// AgentResolver resolves an agent ID from a session ID. Used to handle hooks
// that arrive before the operator's adopt call has written the agent_id (e.g.
// SessionStart fires in the race window between ca.Resume and registerPTYSession).
// Returns "" when the session is unknown.
type AgentResolver func(sessionID string) string

// HookHandler routes omni hook events to the engine.
// It implements http.Handler — the caller registers it on their own mux.
type HookHandler struct {
	eng      Engine
	resolver AgentResolver
}

// New creates a HookHandler wired to eng.
// Pass a non-nil AgentResolver to recover from hooks where agent_id is empty
// by looking up the owning agent from the session ID.
func New(eng Engine, resolver AgentResolver) *HookHandler {
	return &HookHandler{eng: eng, resolver: resolver}
}

// ServeHTTP implements http.Handler.
func (h *HookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var base hooks.HookInput
	if err := json.Unmarshal(body, &base); err != nil {
		http.Error(w, "decode base", http.StatusBadRequest)
		return
	}

	if base.Omni == nil {
		logger.Warn("hook: omni payload missing, dropping event", "session_id", base.SessionID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(hooks.HookOuput{Continue: true})
		return
	}

	agentID := base.Omni.Agent.ID
	agentName := base.Omni.Agent.Name

	if agentID == "" {
		if h.resolver != nil && base.SessionID != "" {
			agentID = h.resolver(base.SessionID)
		}
		if agentID == "" {
			logger.Warn("hook: agent_id is empty and session lookup failed, dropping event", "session_id", base.SessionID)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(hooks.HookOuput{Continue: true})
			return
		}
		logger.Debug("hook: resolved agent_id from session_id", "session_id", base.SessionID, "agent_id", agentID)
	}

	// Prefer X-Hook-Event header (set by hook-operator) over body's hook_event_name,
	// which may carry a stale or provider-specific name.
	eventName := hooks.HookID(r.Header.Get("X-Hook-Event"))
	if eventName == "" {
		eventName = base.HookEventName
	}

	logger.Debug("hook received", "event", eventName, "session_id", base.SessionID, "agent_id", agentID)

	switch eventName {
	case hooks.SessionStart:
		h.eng.OnPreSessionStart(agentID, agentName, base.SessionID, base.Cwd)

	case hooks.PreToolUse:
		var input hooks.PreToolUseParams
		if err := json.Unmarshal(body, &input); err == nil {
			h.eng.OnPreToolUse(agentID, base.SessionID, input.ToolName, input.ToolInput)
		}

	case hooks.PostToolUse:
		var input hooks.PostToolUseParams
		if err := json.Unmarshal(body, &input); err == nil {
			h.eng.OnPostToolUse(agentID, base.SessionID, input.ToolName, input.ToolInput)
		}

	case hooks.PrePrompt:
		var input hooks.PrePromptInputParams
		if err := json.Unmarshal(body, &input); err == nil {
			h.eng.OnUserPromptSubmit(r.Context(), agentID, base.SessionID, input.Prompt)
		}

	case hooks.PostPrompt:
		if recall := h.eng.OnStop(r.Context(), agentID, base.SessionID); recall != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(hooks.HookOuput{Continue: true, SystemMessage: recall})
			return
		}

	case hooks.PostToolUseFailure:
		var input hooks.PostToolUseFailureParams
		if err := json.Unmarshal(body, &input); err == nil {
			h.eng.OnPostToolUseFailure(agentID, base.SessionID, input.ToolName, input.Error)
		}

	default:
		logger.Debug("hook: unhandled event", "event", eventName)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(hooks.HookOuput{Continue: true})
}
