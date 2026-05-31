package agyhooks

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/Shaik-Sirajuddin/memory/config"
	confhooks "github.com/Shaik-Sirajuddin/memory/config/hooks"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	agyconnector "github.com/Shaik-Sirajuddin/memory/connector/codeagent/agy"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/agy/settings"
	codehooks "github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
)

type agyHookTransformer struct {
	mu    sync.RWMutex
	index map[string]config.HookEntry
	order []string
}

type agySettingsFile struct {
	Hooks map[string][]agyHookMatcher `json:"hooks,omitempty"`
}

type agyHookMatcher struct {
	Matcher string            `json:"matcher,omitempty"`
	Hooks   []agyHookEntry `json:"hooks"`
}

type agyHookEntry struct {
	Type    string   `json:"type"`
	Command string   `json:"command,omitempty"`
	URL     string   `json:"url,omitempty"`
	Timeout *float64 `json:"timeout,omitempty"`
}

func New() codeagent.HookTransformer {
	return &agyHookTransformer{
		index: map[string]config.HookEntry{},
	}
}

func (t *agyHookTransformer) Add(name string, entry config.HookEntry) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, ok := t.index[name]; ok {
		return false
	}
	if err := addGlobalHook(name, entry); err != nil {
		return false
	}
	t.index[name] = entry
	t.order = append(t.order, name)
	return true
}

func (t *agyHookTransformer) GetHooks() []confhooks.Hook {
	hooks, err := globalHooks()
	if err != nil {
		return nil
	}
	return hooks
}

func addGlobalHook(name string, entry config.HookEntry) error {
	event := eventName(name, entry)
	agyEvent, ok := agyEventName(event)
	if !ok {
		return fmt.Errorf("agyhooks: unknown hook event: %s", event)
	}

	settingsFile, err := readGlobalSettings()
	if err != nil {
		return err
	}
	if settingsFile.Hooks == nil {
		settingsFile.Hooks = map[string][]agyHookMatcher{}
	}

	agyEntry := agyEntryFromConfig(entry)
	for _, matcher := range settingsFile.Hooks[agyEvent] {
		for _, existing := range matcher.Hooks {
			if sameAgyEntry(existing, agyEntry) {
				return fmt.Errorf("agyhooks: hook already exists: %s", name)
			}
		}
	}

	settingsFile.Hooks[agyEvent] = append(settingsFile.Hooks[agyEvent], agyHookMatcher{
		Hooks: []agyHookEntry{agyEntry},
	})
	return writeGlobalSettings(settingsFile)
}

func globalHooks() ([]confhooks.Hook, error) {
	settingsFile, err := readGlobalSettings()
	if err != nil {
		return nil, err
	}

	out := []confhooks.Hook{}
	for _, agyEvent := range agyEventOrder() {
		event, ok := abstractEventName(agyEvent)
		if !ok {
			continue
		}
		for _, matcher := range settingsFile.Hooks[agyEvent] {
			for _, hook := range matcher.Hooks {
				entry := configEntryFromAgy(hook)
				out = append(out, confhooks.Hook{
					Name:    fmt.Sprintf("agy.global.%s.%d", event, len(out)),
					Entry:   entry,
					Schemas: schemaForEvent(event),
				})
			}
		}
	}
	return out, nil
}

func (t *agyHookTransformer) GetHookResponse(eventName string, payload any) (confhooks.HookResponseSchema, error) {
	response, err := parseResponse(eventName, payload)
	if err != nil {
		return confhooks.HookResponseSchema{}, err
	}
	return confhooks.HookResponseSchema{EventName: eventName, Response: response}, nil
}

func (t *agyHookTransformer) GetHookResult(eventName string, raw any) (confhooks.HookResultSchema, error) {
	response, err := parseResponse(eventName, raw)
	if err != nil {
		return confhooks.HookResultSchema{}, err
	}
	return confhooks.HookResultSchema{EventName: eventName, Result: response}, nil
}

func parseResponse(eventName string, raw any) (confhooks.Response, error) {
	switch eventName {
	case string(codehooks.PreToolUse):
		result, err := agyconnector.ParseHookInput[codehooks.PreToolUseResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	case string(codehooks.PostToolUse):
		result, err := agyconnector.ParseHookInput[codehooks.PostToolUseResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	case string(codehooks.PostToolUseFailure):
		result, err := agyconnector.ParseHookInput[codehooks.PostToolUseFailureResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	case string(codehooks.SessionStart):
		result, err := agyconnector.ParseHookInput[codehooks.SessionStartResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	case string(codehooks.SessionEnd):
		result, err := agyconnector.ParseHookInput[codehooks.SessionEndResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	case string(codehooks.PrePrompt):
		result, err := agyconnector.ParseHookInput[codehooks.PrePromptInputResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	case string(codehooks.PostPrompt):
		result, err := agyconnector.ParseHookInput[codehooks.PostPromptInputResult](raw)
		if err != nil {
			return confhooks.Response{}, err
		}
		return responseFromOutput(result.HookOuput), nil
	default:
		return confhooks.Response{}, fmt.Errorf("agyhooks: unknown hook event: %s", eventName)
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

func eventName(name string, entry config.HookEntry) string {
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

func agyEventName(eventName string) (string, bool) {
	switch eventName {
	case string(codehooks.PreToolUse),
		string(codehooks.PostToolUse),
		string(codehooks.PostToolUseFailure),
		string(codehooks.SessionStart),
		string(codehooks.SessionEnd),
		string(codehooks.PrePrompt),
		string(codehooks.PostPrompt):
		return eventName, true
	default:
		return "", false
	}
}

func abstractEventName(agyEvent string) (string, bool) {
	switch agyEvent {
	case string(codehooks.PreToolUse),
		string(codehooks.PostToolUse),
		string(codehooks.PostToolUseFailure),
		string(codehooks.SessionStart),
		string(codehooks.SessionEnd),
		string(codehooks.PrePrompt),
		string(codehooks.PostPrompt):
		return agyEvent, true
	default:
		return "", false
	}
}

func agyEventOrder() []string {
	return []string{
		string(codehooks.PreToolUse),
		string(codehooks.PostToolUse),
		string(codehooks.PostToolUseFailure),
		string(codehooks.SessionStart),
		string(codehooks.SessionEnd),
		string(codehooks.PrePrompt),
		string(codehooks.PostPrompt),
	}
}

func agyEntryFromConfig(entry config.HookEntry) agyHookEntry {
	if entry.Url != nil {
		return agyHookEntry{
			Type:    "http",
			URL:     *entry.Url,
			Timeout: entry.Timeout,
		}
	}

	command := ""
	if entry.Command != nil {
		command = *entry.Command
	}
	if len(entry.Args) > 0 {
		command = strings.TrimSpace(command + " " + joinArgs(entry.Args))
	}
	return agyHookEntry{
		Type:    "command",
		Command: command,
		Timeout: entry.Timeout,
	}
}

func configEntryFromAgy(entry agyHookEntry) config.HookEntry {
	timeout := entry.Timeout
	switch entry.Type {
	case "http":
		url := entry.URL
		return config.HookEntry{Url: &url, Timeout: timeout}
	default:
		command := entry.Command
		return config.HookEntry{Command: &command, Timeout: timeout}
	}
}

func sameAgyEntry(a, b agyHookEntry) bool {
	return a.Type == b.Type &&
		a.Command == b.Command &&
		a.URL == b.URL &&
		sameTimeout(a.Timeout, b.Timeout)
}

func sameTimeout(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func readGlobalSettings() (agySettingsFile, error) {
	raw, err := settings.ReadUserSettingsRaw()
	if err != nil {
		return agySettingsFile{}, err
	}
	settingsFile := agySettingsFile{}
	if data, ok := raw["hooks"]; ok {
		if err := json.Unmarshal(data, &settingsFile.Hooks); err != nil {
			return agySettingsFile{}, fmt.Errorf("agyhooks: parse hooks: %w", err)
		}
	}
	return settingsFile, nil
}

func writeGlobalSettings(settingsFile agySettingsFile) error {
	return settings.UpdateUserSettingsRaw(func(raw map[string]json.RawMessage) error {
		if len(settingsFile.Hooks) == 0 {
			delete(raw, "hooks")
			return nil
		}
		data, err := json.Marshal(settingsFile.Hooks)
		if err != nil {
			return fmt.Errorf("agyhooks: marshal hooks: %w", err)
		}
		raw["hooks"] = data
		return nil
	})
}

func joinArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\n\"'\\$`") {
			quoted = append(quoted, strconv.Quote(arg))
			continue
		}
		quoted = append(quoted, arg)
	}
	return strings.Join(quoted, " ")
}

func schemaForEvent(eventName string) confhooks.HookSchema {
	switch eventName {
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
