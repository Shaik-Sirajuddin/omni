package codex

import (
	"fmt"
	"strings"
	"sync"

	omniconfig "github.com/Shaik-Sirajuddin/memory/config"
	confhooks "github.com/Shaik-Sirajuddin/memory/config/hooks"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	codehooks "github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
)

type codexHookTransformer struct {
	mu    sync.RWMutex
	index map[string]omniconfig.HookEntry
	order []string
}

func NewHookTransformer() codeagent.HookTransformer {
	return &codexHookTransformer{
		index: map[string]omniconfig.HookEntry{},
	}
}

func (t *codexHookTransformer) Add(name string, entry omniconfig.HookEntry) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, ok := t.index[name]; ok {
		return false
	}
	omniEvent := resolveEventName(name, entry)
	codexEvent, ok := codexEventName(omniEvent)
	if !ok {
		logger.Debug("HookTransformer.Add: no codex event mapping", "name", name, "omniEvent", omniEvent)
		return false
	}

	def := omniEntryToHookDef(entry)
	if def == nil {
		return false // URL-only entries not expressible as Codex command hooks
	}

	configPath, err := globalConfigPath()
	if err != nil {
		return false
	}

	// Build hooksByEvent from the in-memory index rather than reading the file.
	// File round-trips lose previously written hooks due to TOML encoding differences,
	// causing each Add() to overwrite the file with only the newly added hook.
	hooksByEvent := t.indexToHooksByEvent()

	// Deduplicate by command string.
	for _, m := range hooksByEvent[codexEvent] {
		for _, d := range m.Hooks {
			if d.Command == def.Command {
				return false
			}
		}
	}

	hooksByEvent[codexEvent] = append(hooksByEvent[codexEvent], codexHookMatcher{
		Hooks: []codexHookDef{*def},
	})

	if err := writeHooksConfig(configPath, hooksByEvent); err != nil {
		logger.Debug("HookTransformer.Add: write failed", "name", name, "codexEvent", codexEvent, "path", configPath, "err", err)
		return false
	}

	logger.Debug("HookTransformer.Add: hook written", "name", name, "codexEvent", codexEvent, "path", configPath)
	t.index[name] = entry
	t.order = append(t.order, name)
	return true
}

// indexToHooksByEvent builds the hooksByEvent map from the in-memory index.
// Uses t.order to preserve insertion order across registrations.
func (t *codexHookTransformer) indexToHooksByEvent() map[string][]codexHookMatcher {
	hbe := map[string][]codexHookMatcher{}
	for _, n := range t.order {
		e := t.index[n]
		omniEvent := resolveEventName(n, e)
		codexEvent, ok := codexEventName(omniEvent)
		if !ok {
			continue
		}
		def := omniEntryToHookDef(e)
		if def == nil {
			continue
		}
		hbe[codexEvent] = append(hbe[codexEvent], codexHookMatcher{Hooks: []codexHookDef{*def}})
	}
	return hbe
}

func (t *codexHookTransformer) GetHooks() []confhooks.Hook {
	configPath, err := globalConfigPath()
	if err != nil {
		return nil
	}
	hooksByEvent, err := readHooksConfig(configPath)
	if err != nil {
		return nil
	}

	var out []confhooks.Hook
	for codexEvent, matchers := range hooksByEvent {
		omniEvent, ok := omniEventFromCodex(codexEvent)
		if !ok {
			continue
		}
		for i, m := range matchers {
			for j, def := range m.Hooks {
				out = append(out, confhooks.Hook{
					Name:    fmt.Sprintf("codex.global.%s.%d.%d", codexEvent, i, j),
					Entry:   hookDefToOmniEntry(def),
					Schemas: schemaForEvent(omniEvent),
				})
			}
		}
	}
	return out
}

func (t *codexHookTransformer) GetHookResponse(eventName string, payload any) (confhooks.HookResponseSchema, error) {
	omniEvent := toOmniEvent(eventName)
	response, err := parseCodexHookPayload(omniEvent, payload)
	if err != nil {
		return confhooks.HookResponseSchema{}, err
	}
	return confhooks.HookResponseSchema{EventName: omniEvent, Response: response}, nil
}

func (t *codexHookTransformer) GetHookResult(eventName string, raw any) (confhooks.HookResultSchema, error) {
	omniEvent := toOmniEvent(eventName)
	response, err := parseCodexHookPayload(omniEvent, raw)
	if err != nil {
		return confhooks.HookResultSchema{}, err
	}
	return confhooks.HookResultSchema{EventName: omniEvent, Result: response}, nil
}

// toOmniEvent normalises an incoming event name to the omni standard.
// Accepts either a Codex CLI event name ("Stop") or an omni event name ("SessionEnd").
func toOmniEvent(event string) string {
	if omni, ok := omniEventFromCodex(event); ok {
		return omni
	}
	return event
}

func parseCodexHookPayload(eventName string, raw any) (confhooks.Response, error) {
	switch eventName {
	case string(codehooks.PreToolUse):
		result, err := parseHookInput[codehooks.PreToolUseResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	case string(codehooks.PostToolUse):
		result, err := parseHookInput[codehooks.PostToolUseResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	case string(codehooks.PostToolUseFailure):
		result, err := parseHookInput[codehooks.PostToolUseFailureResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	case string(codehooks.SessionStart):
		result, err := parseHookInput[codehooks.SessionStartResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	case string(codehooks.SessionEnd):
		result, err := parseHookInput[codehooks.SessionEndResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	case string(codehooks.PrePrompt):
		result, err := parseHookInput[codehooks.PrePromptInputResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	case string(codehooks.PostPrompt):
		result, err := parseHookInput[codehooks.PostPromptInputResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	default:
		return confhooks.Response{}, fmt.Errorf("codexhooks: unknown hook event: %s", eventName)
	}
}

func responseFromOutput(output codehooks.HookOuput) confhooks.Response {
	return confhooks.Response{
		Continue:       output.Continue,
		StopReason:     output.StopReason,
		SuppressOutput: output.SuppressOutput,
		SystemMessage:  output.SystemMessage,
	}
}

// ============================================================
// omniconfig.HookEntry ↔ codexHookDef conversion
// ============================================================

// omniEntryToHookDef converts an omni HookEntry to a Codex hook definition.
// Returns nil for URL-only entries — Codex CLI only supports command hooks.
//
// Limitation: codexHookDef.Command is a single shell string (no separate Args
// field). Command and Args are joined with spaces, so individual args that
// contain spaces are not round-trip safe. All omni default hooks use simple
// flag args (e.g. --event SessionStart) with no embedded spaces, so this is
// acceptable for the current use case.
func omniEntryToHookDef(entry omniconfig.HookEntry) *codexHookDef {
	if entry.Command == nil {
		return nil
	}
	parts := append([]string{*entry.Command}, entry.Args...)
	def := &codexHookDef{Type: "command", Command: strings.Join(parts, " ")}
	if entry.Timeout != nil {
		def.Timeout = int(*entry.Timeout)
	}
	return def
}

// hookDefToOmniEntry converts a Codex hook definition back to an omni HookEntry.
// The Codex config stores command + args as one shell string; we split on
// whitespace to restore the original Command + Args fields, preserving the
// entryKey invariant used by the registrar's verify() check.
// Args containing spaces are not supported (see omniEntryToHookDef).
func hookDefToOmniEntry(def codexHookDef) omniconfig.HookEntry {
	parts := strings.Fields(def.Command)
	var cmd string
	var args []string
	if len(parts) > 0 {
		cmd = parts[0]
		args = parts[1:]
	}
	var timeout *float64
	if def.Timeout > 0 {
		t := float64(def.Timeout)
		timeout = &t
	}
	entry := omniconfig.HookEntry{Command: &cmd, Timeout: timeout}
	if len(args) > 0 {
		entry.Args = args
	}
	return entry
}

// ============================================================
// Event name helpers
// ============================================================

func resolveEventName(name string, entry omniconfig.HookEntry) string {
	for i, arg := range entry.Args {
		if arg == "--event" && i+1 < len(entry.Args) {
			return entry.Args[i+1]
		}
		if value, ok := strings.CutPrefix(arg, "--event="); ok {
			return value
		}
	}
	if value, ok := strings.CutPrefix(name, "omni."); ok {
		return value
	}
	return name
}

// supportedEvent returns true when omniEvent has a Codex CLI equivalent.
func supportedEvent(event string) bool {
	_, ok := codexEventName(event)
	return ok
}

func schemaForEvent(event string) confhooks.HookSchema {
	switch event {
	case string(codehooks.PreToolUse):
		return &confhooks.PreToolUseSchema{}
	case string(codehooks.PostToolUse):
		return &confhooks.PostToolUseSchema{}
	case string(codehooks.PostToolUseFailure):
		return &confhooks.PostToolUseFailureSchema{}
	case string(codehooks.SessionStart):
		return &confhooks.SessionStartSchema{}
	case string(codehooks.SessionEnd):
		return &confhooks.SessionEndSchema{}
	case string(codehooks.PrePrompt):
		return &confhooks.PrePromptSchema{}
	case string(codehooks.PostPrompt):
		return &confhooks.PostPromptSchema{}
	default:
		return nil
	}
}
