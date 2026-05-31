package gemini

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
)

type geminiStreamEvent struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Delta   string `json:"delta,omitempty"`
	Content string `json:"content,omitempty"`
	Name    string `json:"name,omitempty"`
	Error   string `json:"error,omitempty"`
}

// parseGeminiLine converts one stdout line into a normalized stream event.
func parseGeminiLine(line string) codeagent.StreamEvent {
	var ev geminiStreamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return codeagent.StreamEvent{Type: "text", Content: line}
	}

	t := strings.ToLower(strings.TrimSpace(ev.Type))
	content := firstNonEmpty(ev.Delta, ev.Text, ev.Content)

	switch {
	case t == "text", t == "message", t == "assistant_message", t == "output_text_delta":
		return codeagent.StreamEvent{Type: "text", Content: content}
	case strings.Contains(t, "tool") && (strings.Contains(t, "start") || strings.Contains(t, "call") || strings.Contains(t, "use")):
		return codeagent.StreamEvent{Type: "tool_use", Content: firstNonEmpty(content, ev.Name)}
	case strings.Contains(t, "tool") && (strings.Contains(t, "result") || strings.Contains(t, "end") || strings.Contains(t, "done")):
		return codeagent.StreamEvent{Type: "tool_result", Content: firstNonEmpty(content, ev.Name)}
	case t == "stop" || t == "done" || t == "completed" || t == "complete":
		return codeagent.StreamEvent{Type: "stop", Done: true, Content: content}
	case t == "error":
		msg := firstNonEmpty(content, ev.Error, line)
		return codeagent.StreamEvent{Type: "stop", Done: true, Content: msg}
	default:
		if content != "" {
			return codeagent.StreamEvent{Type: "text", Content: content}
		}
		return codeagent.StreamEvent{Type: t, Content: ""}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func (a *geminiAgent) PreToolUseParams(raw any) (*hooks.PreToolUseParams, error) {
	return parseHookInput[hooks.PreToolUseParams](raw)
}

func (a *geminiAgent) PostToolUseParams(raw any) (*hooks.PostToolUseParams, error) {
	return parseHookInput[hooks.PostToolUseParams](raw)
}

func (a *geminiAgent) PostToolUseFailureParams(raw any) (*hooks.PostToolUseFailureParams, error) {
	return parseHookInput[hooks.PostToolUseFailureParams](raw)
}

func (a *geminiAgent) SessionStartParams(raw any) (*hooks.SessionStartParams, error) {
	return parseHookInput[hooks.SessionStartParams](raw)
}

func (a *geminiAgent) SessionEndParams(raw any) (*hooks.SessionEndParams, error) {
	return parseHookInput[hooks.SessionEndParams](raw)
}

func (a *geminiAgent) PrePromptInputParams(raw any) (*hooks.PrePromptInputParams, error) {
	return parseHookInput[hooks.PrePromptInputParams](raw)
}

func (a *geminiAgent) PostPromptInputParams(raw any) (*hooks.PostPromptInputParams, error) {
	return parseHookInput[hooks.PostPromptInputParams](raw)
}

func (a *geminiAgent) PreToolUseResult(raw any) (*hooks.PreToolUseResult, error) {
	return parseHookInput[hooks.PreToolUseResult](raw)
}

func (a *geminiAgent) PostToolUseResult(raw any) (*hooks.PostToolUseResult, error) {
	return parseHookInput[hooks.PostToolUseResult](raw)
}

func (a *geminiAgent) PostToolUseFailureResult(raw any) (*hooks.PostToolUseFailureResult, error) {
	return parseHookInput[hooks.PostToolUseFailureResult](raw)
}

func (a *geminiAgent) SessionStartResult(raw any) (*hooks.SessionStartResult, error) {
	return parseHookInput[hooks.SessionStartResult](raw)
}

func (a *geminiAgent) SessionEndResult(raw any) (*hooks.SessionEndResult, error) {
	return parseHookInput[hooks.SessionEndResult](raw)
}

func (a *geminiAgent) PrePromptInputResult(raw any) (*hooks.PrePromptInputResult, error) {
	return parseHookInput[hooks.PrePromptInputResult](raw)
}

func (a *geminiAgent) PostPromptInputResult(raw any) (*hooks.PostPromptInputResult, error) {
	return parseHookInput[hooks.PostPromptInputResult](raw)
}

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
			return nil, fmt.Errorf("gemini: parser: marshal input: %w", err)
		}
		data = b
	}

	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("gemini: parser: unmarshal %T: %w", result, err)
	}
	return &result, nil
}

func lookPath(name string) (string, error) {
	for _, candidate := range Config.Binary {
		if candidate == "" {
			continue
		}
		if candidate[0] == '/' {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return exec.LookPath(name)
}

func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		logger.Error("generateID: crypto/rand failed", "err", err)
		return "gemini-session"
	}
	return hex.EncodeToString(b)
}

func parseJSONStringSlice(raw string) []string {
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}
