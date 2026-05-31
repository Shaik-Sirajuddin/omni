package hookoperator

import (
	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
)

// DefaultHook pairs a lifecycle event with the hook entry the operator registers
// on every codeagent.
type DefaultHook struct {
	Name  string
	Event hooks.HookID
	Entry config.HookEntry
}

// DefaultHooks returns the full set of hook entries (one per event) for a provider.
// Each entry routes the codeagent event to:
//
//	<binaryPath> hook --event <eventName>
//
// The agentID is resolved at callback time from the session_id in the payload.
func DefaultHooks(binaryPath string) []DefaultHook {
	entry := func(event hooks.HookID) config.HookEntry {
		return config.HookEntry{
			Command: &binaryPath,
			Args:    []string{"hook", "--event", string(event)},
		}
	}

	return []DefaultHook{
		{Name: "hookop.PreToolUse", Event: hooks.PreToolUse, Entry: entry(hooks.PreToolUse)},
		{Name: "hookop.PostToolUse", Event: hooks.PostToolUse, Entry: entry(hooks.PostToolUse)},
		{Name: "hookop.PostToolUseFailure", Event: hooks.PostToolUseFailure, Entry: entry(hooks.PostToolUseFailure)},
		{Name: "hookop.SessionStart", Event: hooks.SessionStart, Entry: entry(hooks.SessionStart)},
		{Name: "hookop.SessionEnd", Event: hooks.SessionEnd, Entry: entry(hooks.SessionEnd)},
		{Name: "hookop.UserPromptSubmit", Event: hooks.PrePrompt, Entry: entry(hooks.PrePrompt)},
		{Name: "hookop.Stop", Event: hooks.PostPrompt, Entry: entry(hooks.PostPrompt)},
	}
}

// DefaultHookNames returns the registration keys for all operator default hooks.
func DefaultHookNames() []string {
	names := make([]string, 0, len(hooks.Hooks))
	for _, id := range hooks.Hooks {
		names = append(names, "hookop."+string(id))
	}
	return names
}
