package agy

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	codeagentutils "github.com/Shaik-Sirajuddin/memory/connector/codeagent/utils"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
)

// TODO : attach parser methods to AgyParser

// AgyParser implements [hooks.HookIOParser]
type AgyParser struct {
}

// ============================================================
// Agy stream-json wire format
// ============================================================
//
// `agy -p --output-format stream-json` emits newline-delimited JSON
// following the Anthropic Messages API streaming format:
//
//   message_start          → session metadata
//   content_block_start    → new content block
//   content_block_delta    → incremental text or tool input
//   content_block_stop     → block complete
//   message_delta          → stop reason / usage
//   message_stop           → stream finished

type agyStreamEvent struct {
	Type  string             `json:"type"`
	Index int                `json:"index"`
	Delta *agyStreamDelta `json:"delta,omitempty"`
}

type agyStreamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

// parseAgyLine converts one raw plain text line from agy output
// into a codeagent.StreamEvent.
func parseAgyLine(line string) codeagent.StreamEvent {
	return codeagent.StreamEvent{Type: "text", Content: line + "\n"}
}

// ============================================================
// Auth status parsing
// ============================================================

type agyAuthStatus struct {
	LoggedIn    bool   `json:"logged_in"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

func parseAuthStatus(raw string) codeagent.UserIdentify {
	var s agyAuthStatus
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		// Fallback: if exit 0 but can't parse JSON, assume authenticated.
		return codeagent.UserIdentify{Authenticated: true}
	}
	return codeagent.UserIdentify{
		Authenticated: s.LoggedIn,
		Email:         s.Email,
		DisplayName:   s.DisplayName,
	}
}

// ============================================================
// HookIOParser — parse Agy hook wire format → interface types
// ============================================================

func (a *AgyParser) PreToolUseParams(raw any) (*hooks.PreToolUseParams, error) {
	return parseHookInput[hooks.PreToolUseParams](raw)
}

func (a *AgyParser) PostToolUseParams(raw any) (*hooks.PostToolUseParams, error) {
	return parseHookInput[hooks.PostToolUseParams](raw)
}

func (a *AgyParser) PostToolUseFailureParams(raw any) (*hooks.PostToolUseFailureParams, error) {
	return parseHookInput[hooks.PostToolUseFailureParams](raw)
}

func (a *AgyParser) SessionStartParams(raw any) (*hooks.SessionStartParams, error) {
	return parseHookInput[hooks.SessionStartParams](raw)
}

func (a *AgyParser) SessionEndParams(raw any) (*hooks.SessionEndParams, error) {
	return parseHookInput[hooks.SessionEndParams](raw)
}

func (a *AgyParser) PrePromptInputParams(raw any) (*hooks.PrePromptInputParams, error) {
	return parseHookInput[hooks.PrePromptInputParams](raw)
}

func (a *AgyParser) PostPromptInputParams(raw any) (*hooks.PostPromptInputParams, error) {
	return parseHookInput[hooks.PostPromptInputParams](raw)
}

func (a *AgyParser) PreToolUseResult(raw any) (*hooks.PreToolUseResult, error) {
	return parseHookInput[hooks.PreToolUseResult](raw)
}

func (a *AgyParser) PostToolUseResult(raw any) (*hooks.PostToolUseResult, error) {
	return parseHookInput[hooks.PostToolUseResult](raw)
}

func (a *AgyParser) PostToolUseFailureResult(raw any) (*hooks.PostToolUseFailureResult, error) {
	return parseHookInput[hooks.PostToolUseFailureResult](raw)
}

func (a *AgyParser) SessionStartResult(raw any) (*hooks.SessionStartResult, error) {
	return parseHookInput[hooks.SessionStartResult](raw)
}

func (a *AgyParser) SessionEndResult(raw any) (*hooks.SessionEndResult, error) {
	return parseHookInput[hooks.SessionEndResult](raw)
}

func (a *AgyParser) PrePromptInputResult(raw any) (*hooks.PrePromptInputResult, error) {
	return parseHookInput[hooks.PrePromptInputResult](raw)
}

func (a *AgyParser) PostPromptInputResult(raw any) (*hooks.PostPromptInputResult, error) {
	return parseHookInput[hooks.PostPromptInputResult](raw)
}

// ============================================================
// Generic JSON round-trip parser
// ============================================================

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
			return nil, fmt.Errorf("agy: parser: marshal input: %w", err)
		}
		data = b
	}

	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("agy: parser: unmarshal %T: %w", result, err)
	}
	return &result, nil
}

// ============================================================
// Utilities
// ============================================================

func lookPath(name string) (string, error) {
	for _, candidate := range Config.Binary {
		if candidate == "" {
			continue
		}
		if strings.HasPrefix(candidate, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				candidate = home + candidate[1:]
			}
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
	if path, err := codeagentutils.LookPathNVM(name); err == nil {
		return path, nil
	}
	return exec.LookPath(name)
}

func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		logger.Error("generateID: crypto/rand failed", "err", err)
		return "agy-session"
	}
	return hex.EncodeToString(b)
}

// ============================================================
// CLI model discovery
// ============================================================
//
// Agy Code's /model command outputs an interactive TUI list in the form:
//
//   │  ❯ 1. Default (recommended) ✔  Sonnet 4.6 · Best for everyday tasks
//   │    2. Opus                     Opus 4.7 · Most capable for complex work
//   │    3. Haiku                    Haiku 4.5 · Fastest for quick answers
//
// We pipe "/model\n" to `agy --print` with a short timeout, then parse
// the output looking for "Sonnet|Opus|Haiku" followed by a version number,
// and map them to canonical model IDs.

// modelNameRe matches Gemini models
var modelNameRe = regexp.MustCompile(`(?i)(gemini|pro|flash)\s*[-\d\.]+`)

// modelIDMap maps a lowercase "name version" key to a canonical model ID.
// Updated as new Agy versions are released.
var modelIDMap = map[string]string{
	"gemini-1.5-pro":   "gemini-1-5-pro",
	"gemini-1.5-flash": "gemini-1-5-flash",
}

// discoverFromCLI pipes "/model\n" to `agy --print`, waits up to 5 s for
// output, and returns the parsed model IDs. Returns an error (and nil slice)
// when discovery fails or the output cannot be parsed.
func discoverFromCLI(workDir string) ([]codeagent.ModelID, error) {
	cmd := exec.Command("agy", "--print")
	cmd.Dir = workDir
	cmd.Stdin = bytes.NewBufferString("/model\n")

	// Use a timer to enforce a hard deadline — the interactive picker may
	// block waiting for further input.
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		defer close(done)
		out, runErr = cmd.Output()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill() //nolint:errcheck
		}
		<-done
	}

	if runErr != nil && len(out) == 0 {
		return nil, fmt.Errorf("discoverFromCLI: %w", runErr)
	}

	ids := parseModelList(string(out))
	if len(ids) == 0 {
		return nil, fmt.Errorf("discoverFromCLI: no models found in output")
	}
	return ids, nil
}

// parseModelList scans raw text for "Sonnet/Opus/Haiku N.N" patterns and
// returns the corresponding canonical model IDs in the order they appear.
// Duplicates are suppressed.
func parseModelList(raw string) []codeagent.ModelID {
	seen := map[string]bool{}
	var ids []codeagent.ModelID

	for _, line := range strings.Split(raw, "\n") {
		matches := modelNameRe.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			key := strings.ToLower(m[1] + " " + m[2])
			if canonID, ok := modelIDMap[key]; ok && !seen[canonID] {
				seen[canonID] = true
				ids = append(ids, codeagent.ModelID(canonID))
			}
		}
	}
	return ids
}
