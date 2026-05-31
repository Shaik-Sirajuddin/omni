package codex

import "encoding/json"

// HookOutput is the JSON structure written to stdout when Codex fires a hook.
// Field names match the camelCase schema Codex CLI expects when parsing hook responses.
type HookOutput struct {
	Continue       bool    `json:"continue"`
	StopReason     *string `json:"stopReason,omitempty"`
	SuppressOutput bool    `json:"suppressOutput,omitempty"`
	SystemMessage  *string `json:"systemMessage,omitempty"`
}

// MarshalHookOutput serialises a HookOutput to the JSON bytes that Codex CLI
// expects on stdout after running a hook command.
func MarshalHookOutput(out HookOutput) ([]byte, error) {
	return json.Marshal(out)
}
