package codex

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
)

// ============================================================
// Codex wire format — JSON stream event types
// ============================================================
//
// codex exec --json emits newline-delimited JSON following the
// OpenAI Responses API streaming format.
//
// Known event shapes:
//   response.output_text.delta   → text chunk
//   response.output_text.done    → text chunk complete
//   response.output_item.added   → new item started (message or function_call)
//   response.output_item.done    → item completed
//   response.completed           → stream finished
//   error                        → stream error

// codexEvent is the minimal wire envelope for every codex JSON stream line.
type codexEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`    // response.output_text.delta
	Text     string          `json:"text"`     // response.output_text.done
	Item     *codexItem      `json:"item"`     // response.output_item.*
	Error    *codexErrorBody `json:"error"`    // error event
}

type codexItem struct {
	Type string `json:"type"` // "message" | "function_call"
	Name string `json:"name"` // function name (function_call only)
}

type codexErrorBody struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// parseCodexLine converts one raw JSON line from codex exec --json output
// into a codeagent.StreamEvent.
// Unrecognised lines are emitted as type="text" events so no output is silently dropped.
func parseCodexLine(line string) codeagent.StreamEvent {
	var ev codexEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		// Plain-text output (e.g. non-JSON startup messages).
		return codeagent.StreamEvent{Type: "text", Content: line}
	}

	switch ev.Type {
	case "response.output_text.delta":
		return codeagent.StreamEvent{Type: "text", Content: ev.Delta}

	case "response.output_text.done":
		// Final text chunk — emit only if delta wasn't already sent.
		return codeagent.StreamEvent{Type: "text", Content: ev.Text}

	case "response.output_item.added":
		if ev.Item != nil && ev.Item.Type == "function_call" {
			return codeagent.StreamEvent{Type: "tool_use", Content: ev.Item.Name}
		}
		return codeagent.StreamEvent{Type: "text", Content: ""}

	case "response.output_item.done":
		if ev.Item != nil && ev.Item.Type == "function_call" {
			return codeagent.StreamEvent{Type: "tool_result", Content: ev.Item.Name}
		}
		return codeagent.StreamEvent{Type: "text", Content: ""}

	case "response.completed":
		return codeagent.StreamEvent{Type: "stop", Done: true}

	case "error":
		msg := ""
		if ev.Error != nil {
			msg = fmt.Sprintf("%s: %s", ev.Error.Code, ev.Error.Message)
		}
		logger.Error("parseCodexLine: stream error event", "msg", msg)
		return codeagent.StreamEvent{Type: "stop", Done: true, Content: msg}

	default:
		// Forward unknown event types transparently.
		return codeagent.StreamEvent{Type: ev.Type, Content: ""}
	}
}

// ============================================================
// HookIOParser — parse codex hook wire format → interface types
// ============================================================
//
// Codex sends hook payloads as JSON on stdin when a hook fires.
// The schemas live in codex/hooks/*.command.input.schema.json.
// Responses are written as JSON to stdout.

func (a *codexAgent) PreToolUseParams(raw any) (*hooks.PreToolUseParams, error) {
	return parseHookInput[hooks.PreToolUseParams](raw)
}

func (a *codexAgent) PostToolUseParams(raw any) (*hooks.PostToolUseParams, error) {
	return parseHookInput[hooks.PostToolUseParams](raw)
}

func (a *codexAgent) PostToolUseFailureParams(raw any) (*hooks.PostToolUseFailureParams, error) {
	return parseHookInput[hooks.PostToolUseFailureParams](raw)
}

func (a *codexAgent) SessionStartParams(raw any) (*hooks.SessionStartParams, error) {
	return parseHookInput[hooks.SessionStartParams](raw)
}

func (a *codexAgent) SessionEndParams(raw any) (*hooks.SessionEndParams, error) {
	return parseHookInput[hooks.SessionEndParams](raw)
}

func (a *codexAgent) PrePromptInputParams(raw any) (*hooks.PrePromptInputParams, error) {
	return parseHookInput[hooks.PrePromptInputParams](raw)
}

func (a *codexAgent) PostPromptInputParams(raw any) (*hooks.PostPromptInputParams, error) {
	return parseHookInput[hooks.PostPromptInputParams](raw)
}

func (a *codexAgent) PreToolUseResult(raw any) (*hooks.PreToolUseResult, error) {
	return parseHookInput[hooks.PreToolUseResult](raw)
}

func (a *codexAgent) PostToolUseResult(raw any) (*hooks.PostToolUseResult, error) {
	return parseHookInput[hooks.PostToolUseResult](raw)
}

func (a *codexAgent) PostToolUseFailureResult(raw any) (*hooks.PostToolUseFailureResult, error) {
	return parseHookInput[hooks.PostToolUseFailureResult](raw)
}

func (a *codexAgent) SessionStartResult(raw any) (*hooks.SessionStartResult, error) {
	return parseHookInput[hooks.SessionStartResult](raw)
}

func (a *codexAgent) SessionEndResult(raw any) (*hooks.SessionEndResult, error) {
	return parseHookInput[hooks.SessionEndResult](raw)
}

func (a *codexAgent) PrePromptInputResult(raw any) (*hooks.PrePromptInputResult, error) {
	return parseHookInput[hooks.PrePromptInputResult](raw)
}

func (a *codexAgent) PostPromptInputResult(raw any) (*hooks.PostPromptInputResult, error) {
	return parseHookInput[hooks.PostPromptInputResult](raw)
}

// ============================================================
// Generic JSON round-trip parser
// ============================================================

// parseHookInput deserialises raw ([]byte | string | any) into T.
// Accepts whatever shape the caller passes — bytes, string, or a pre-decoded map.
func parseHookInput[T any](raw any) (*T, error) {
	var data []byte
	switch v := raw.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("codex: parser: marshal input: %w", err)
		}
		data = b
	}

	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("codex: parser: unmarshal %T: %w", result, err)
	}
	return &result, nil
}

// ============================================================
// Utilities
// ============================================================

// generateID returns a cryptographically random 8-byte hex string.
func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		logger.Error("generateID: crypto/rand failed", "err", err)
		return "codex-session"
	}
	return hex.EncodeToString(b)
}

// lookPath wraps exec.LookPath so it can be stubbed in tests.
func lookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// resolveBinary tries each candidate name/path in order and returns the first
// one that resolves via exec.LookPath. This allows ConfigPaths.Binary to list
// multiple fallback names (e.g. "codex", "/usr/local/bin/codex").
func resolveBinary(candidates []string) (string, error) {
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("none of %v found in PATH", candidates)
}

// parseJSONStringSlice decodes a JSON array of strings (e.g. ["m1","m2"]) into a string slice.
// Returns nil if decoding fails.
func parseJSONStringSlice(raw string) []string {
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}
