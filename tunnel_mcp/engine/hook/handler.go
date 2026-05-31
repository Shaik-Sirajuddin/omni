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
	OnUserPromptSubmit(ctx context.Context, agentID, sessionID, prompt string)
	OnStop(ctx context.Context, agentID, sessionID string)
	OnPostToolUseFailure(agentID, sessionID, toolName, errMsg string)
}

// HookHandler routes omni hook events to the engine.
// It implements http.Handler — the caller registers it on their own mux.
type HookHandler struct {
	eng Engine
}

// New creates a HookHandler wired to eng.
func New(eng Engine) *HookHandler {
	return &HookHandler{eng: eng}
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
		logger.Warn("hook: agent_id is empty, dropping event", "session_id", base.SessionID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(hooks.HookOuput{Continue: true})
		return
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

	case hooks.PrePrompt:
		var input hooks.PrePromptInputParams
		if err := json.Unmarshal(body, &input); err == nil {
			h.eng.OnUserPromptSubmit(r.Context(), agentID, base.SessionID, input.Prompt)
		}

	case hooks.PostPrompt:
		h.eng.OnStop(r.Context(), agentID, base.SessionID)

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
