package gemini

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/omni/connector/codeagent"
	"github.com/Shaik-Sirajuddin/omni/connector/codeagent/hooks"
	sandbox "github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermissionMappings(t *testing.T) {
	t.Run("ApprovalModeFlag", func(t *testing.T) {
		tests := []struct {
			name string
			in   codeagent.PermissionMode
			want string
		}{
			{name: "default-empty", in: "", want: "default"},
			{name: "default", in: codeagent.PermissionDefault, want: "default"},
			{name: "plan", in: codeagent.PermissionPlan, want: "plan"},
			{name: "accept-edits", in: codeagent.PermissionAcceptEdits, want: "auto_edit"},
			{name: "auto", in: codeagent.PermissionAuto, want: "auto_edit"},
			{name: "dont-ask", in: codeagent.PermissionDontAsk, want: "yolo"},
			{name: "bypass", in: codeagent.PermissionBypassPermissions, want: "yolo"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assert.Equal(t, tt.want, approvalModeFlag(tt.in), "Approval mode mapping should match expected value")
			})
		}
	})

	t.Run("PermissionFromApprovalMode", func(t *testing.T) {
		tests := []struct {
			name string
			in   string
			want codeagent.PermissionMode
		}{
			{name: "default", in: "default", want: codeagent.PermissionDefault},
			{name: "plan", in: "plan", want: codeagent.PermissionPlan},
			{name: "auto-edit", in: "auto_edit", want: codeagent.PermissionAcceptEdits},
			{name: "yolo", in: "yolo", want: codeagent.PermissionBypassPermissions},
			{name: "unknown", in: "zzz", want: ""},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assert.Equal(t, tt.want, permissionFromApprovalMode(tt.in), "Reverse permission mapping should match expected value")
			})
		}
	})
}

func TestArgsBuilding(t *testing.T) {
	t.Run("ExecArgsShouldIncludeExpectedFlags", func(t *testing.T) {
		sbx := &sandbox.Config{
			AgentPolicy: &sandbox.Policy{
				FSPolicy: sandbox.FSPolicy(sandbox.Inherit),
			},
		}
		args := buildExecArgs(
			"hello",
			"gemini-2.5-flash",
			codeagent.PermissionAcceptEdits,
			"system",
			codeagent.OutputFormatStreamJSON,
			3,
			sbx,
		)

		expectedSubset := []string{
			"hello",
			"--model", "gemini-2.5-flash",
			"--approval-mode", "auto_edit",
			"--system-prompt", "system",
			"--max-turns", "3",
			"--yolo",
			"--acp",
		}
		assert.Subset(t, args, expectedSubset, "Exec args should include all expected flags")
	})
}

func TestParserBehavior(t *testing.T) {
	t.Run("ParseGeminiLine", func(t *testing.T) {
		tests := []struct {
			name      string
			line      string
			wantType  string
			wantDone  bool
			wantValue string
		}{
			{name: "plain-text", line: "hello", wantType: "text", wantValue: "hello"},
			{name: "text-delta", line: `{"type":"output_text_delta","delta":"abc"}`, wantType: "text", wantValue: "abc"},
			{name: "tool-use", line: `{"type":"tool_call_start","name":"bash"}`, wantType: "tool_use", wantValue: "bash"},
			{name: "tool-result", line: `{"type":"tool_result_done","content":"ok"}`, wantType: "tool_result", wantValue: "ok"},
			{name: "stop", line: `{"type":"completed","content":"done"}`, wantType: "stop", wantDone: true, wantValue: "done"},
			{name: "error", line: `{"type":"error","error":"boom"}`, wantType: "stop", wantDone: true, wantValue: "boom"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				ev := parseGeminiLine(tt.line)
				assert.Equal(t, tt.wantType, ev.Type, "Parsed event type should match expected value")
				assert.Equal(t, tt.wantDone, ev.Done, "Parsed event done flag should match expected value")
				assert.Equal(t, tt.wantValue, ev.Content, "Parsed event content should match expected value")
			})
		}
	})
}

func TestSettingsBehavior(t *testing.T) {
	t.Run("ReadWriteShouldUseGeneratedSettingsSchema", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".gemini", "settings.json")

		input := `{"model":{"name":"m1"},"general":{"defaultApprovalMode":"plan"},"tools":{"sandbox":"read-only"}}`
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755), "Settings directory creation should succeed")
		require.NoError(t, os.WriteFile(path, []byte(input), 0o644), "Writing initial settings should succeed")

		f, err := readSettingsFile(path)
		require.NoError(t, err, "Reading settings should succeed")
		require.NotNil(t, f.Model.Name, "Model name should be parsed from generated settings")
		assert.Equal(t, "m1", *f.Model.Name, "Model value should be parsed from settings")
		assert.Equal(t, SettingsSchemaJsonGeneralDefaultApprovalMode("plan"), f.General.DefaultApprovalMode, "Approval mode should be parsed from settings")
		assert.Equal(t, "read-only", f.Tools.Sandbox, "Sandbox value should be parsed from settings")

		f.Model.Name = stringPtr("m2")
		require.NoError(t, writeSettingsFile(path, f), "Writing updated settings should succeed")

		out, err := os.ReadFile(path)
		require.NoError(t, err, "Reading updated settings file should succeed")

		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw), "Updated settings JSON should parse successfully")
		modelRaw, ok := raw["model"].(map[string]any)
		require.True(t, ok, "Updated settings model should be an object")
		assert.Equal(t, "m2", modelRaw["name"], "Updated model value should be persisted")
	})

	t.Run("WorkspaceSyncAndSandboxHelpers", func(t *testing.T) {
		workDir := t.TempDir()
		require.NoError(t, syncModelAndModeConfig(workDir, "gemini-2.5-pro", codeagent.PermissionAcceptEdits), "Workspace model and mode sync should succeed")

		settingsPath := filepath.Join(workDir, ".gemini", "settings.json")
		f, err := readSettingsFile(settingsPath)
		require.NoError(t, err, "Reading workspace settings should succeed")
		require.NotNil(t, f.Model.Name, "Workspace settings model should be set")
		assert.Equal(t, "gemini-2.5-pro", *f.Model.Name, "Workspace settings model should be synced")
		assert.Equal(t, SettingsSchemaJsonGeneralDefaultApprovalMode("auto_edit"), f.General.DefaultApprovalMode, "Workspace approval mode should be synced")

		assert.Equal(t, "", sandboxFlagValue(nil), "Nil sandbox should map to empty flag")
		assert.Equal(t, "read-only", sandboxFlagValue(&sandbox.Config{}), "Read-only sandbox should map to read-only flag")
		assert.Equal(t, "danger-full-access", sandboxFlagValue(&sandbox.Config{
			AgentPolicy: &sandbox.Policy{
				FSPolicy: sandbox.FSPolicy(sandbox.Inherit),
			},
		}), "Extended sandbox should map to full access flag")
	})

	t.Run("DefaultsAndUpdateDefaultsShouldPersist", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		agent := &geminiAgent{
			workDir:  t.TempDir(),
			model:    "initial-model",
			permMode: codeagent.PermissionDefault,
		}

		cfg, err := agent.Defaults()
		require.NoError(t, err, "Defaults lookup should succeed with in-memory fallback")
		assert.Equal(t, "initial-model", cfg.Model.Model, "Defaults model should match in-memory value when global settings are missing")

		err = agent.UpdateDefaults(&codeagent.Config{
			Model:          codeagent.Model{Provider: Gemini, Model: "new-model"},
			PermissionMode: codeagent.PermissionAcceptEdits,
			Sandbox:        &sandbox.Config{},
		})
		require.NoError(t, err, "Updating defaults should persist global settings")

		global := filepath.Join(home, ".gemini", "settings.json")
		f, err := readSettingsFile(global)
		require.NoError(t, err, "Reading persisted global settings should succeed")
		require.NotNil(t, f.Model.Name, "Persisted global model should be set")
		assert.Equal(t, "new-model", *f.Model.Name, "Persisted global model should be updated")
		assert.Equal(t, SettingsSchemaJsonGeneralDefaultApprovalMode("auto_edit"), f.General.DefaultApprovalMode, "Persisted global approval mode should be updated")
		assert.Equal(t, "read-only", f.Tools.Sandbox, "Persisted global sandbox should be updated")

		cfg, err = agent.Defaults()
		require.NoError(t, err, "Defaults lookup should succeed after persistence")
		assert.Equal(t, "new-model", cfg.Model.Model, "Defaults model should reflect global persisted value")
		assert.Equal(t, codeagent.PermissionAcceptEdits, cfg.PermissionMode, "Defaults permission mode should reflect global persisted value")
	})
}

func TestHookBehavior(t *testing.T) {
	t.Run("HookDefinitionRoundTripAndSync", func(t *testing.T) {
		workDir := t.TempDir()
		u := "https://example.com/hook"
		items := []*hooks.HookData{
			{
				UID:  "cmd-1",
				Path: hooks.HookPath{WorkspaceDir: &workDir},
				Info: &hooks.HookInfo{
					ID:      hooks.PreToolUse,
					Type:    hooks.CMD,
					Command: "echo",
					Args:    []string{"a"},
					Timeout: 5,
				},
			},
			{
				UID:  "web-1",
				Path: hooks.HookPath{WorkspaceDir: &workDir},
				Info: &hooks.HookInfo{
					ID:      hooks.PostPrompt,
					Type:    hooks.Webhook,
					Url:     &u,
					Timeout: 7,
				},
			},
		}

		require.NoError(t, syncHooksToSettings(workDir, items), "Hook sync should persist workspace hook settings")

		p := filepath.Join(workDir, ".gemini", "settings.json")
		f, err := readSettingsFile(p)
		require.NoError(t, err, "Reading workspace hook settings should succeed")
		assert.NotEmpty(t, f.Hooks, "Hook settings should contain synced hooks")

		gotCmd := hookDataToDefinition(items[0])
		require.NotNil(t, gotCmd.Type, "Command hook definition type should be set")
		require.NotNil(t, gotCmd.Command, "Command hook definition command should be set")
		require.NotNil(t, gotCmd.Name, "Command hook definition name should be set")
		assert.Equal(t, "command", *gotCmd.Type, "Command hook definition type should be command")
		assert.Equal(t, "echo a", *gotCmd.Command, "Command hook definition command should match source hook and args")
		assert.Equal(t, "cmd-1", *gotCmd.Name, "Command hook definition UID should map to name")

		back := hookDefinitionToData("web-1", hooks.PostPrompt, HookDefinitionArrayElemHooksElem{Type: stringPtr("webhook"), Command: stringPtr(u)}, hooks.HookPath{WorkspaceDir: &workDir})
		assert.Equal(t, hooks.Webhook, back.Info.Type, "Webhook round-trip should retain webhook type")
		require.NotNil(t, back.Info.Url, "Webhook round-trip URL should be set")
		assert.Equal(t, u, *back.Info.Url, "Webhook round-trip URL should match source URL")
	})
}

func TestSettingsResolverInterface(t *testing.T) {
	t.Run("AgentShouldReadWriteUserAndWorkspaceSettings", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		agent := &geminiAgent{
			workDir:   t.TempDir(),
			permMode:  codeagent.PermissionDefault,
			model:     "gemini-2.5-flash",
			settings:  newSettingsResolver(),
			sbx:       nil,
			sessionID: "s1",
		}

		err := agent.SaveDefaultSettings(&codeagent.Settings{
			Provider: Gemini,
			Config: codeagent.Config{
				Model:          codeagent.Model{Provider: Gemini, Model: "gemini-2.5-pro"},
				PermissionMode: codeagent.PermissionAcceptEdits,
				Sandbox: &sandbox.Config{
					AgentPolicy: &sandbox.Policy{FSPolicy: sandbox.FSPolicy(sandbox.Inherit)},
				},
			},
		})
		require.NoError(t, err, "Saving default settings should succeed")

		userSettings, err := agent.GetUserSettings()
		require.NoError(t, err, "Reading user settings should succeed")
		assert.Equal(t, "gemini-2.5-pro", userSettings.Config.Model.Model, "User settings model should match persisted value")
		assert.Equal(t, codeagent.PermissionAcceptEdits, userSettings.Config.PermissionMode, "User permission should map from persisted approval mode")
		require.NotNil(t, userSettings.Config.Sandbox, "User sandbox should map from persisted sandbox flag")

		ws := t.TempDir()
		require.NoError(t, syncModelAndModeConfig(ws, "gemini-2.0-flash", codeagent.PermissionPlan), "Workspace settings sync should succeed")
		workspaceSettings, err := agent.GetWorkspaceSettings(sandbox.WorkspaceDir(ws))
		require.NoError(t, err, "Reading workspace settings should succeed")
		assert.Equal(t, "gemini-2.0-flash", workspaceSettings.Config.Model.Model, "Workspace model should match workspace settings file")
		assert.Equal(t, codeagent.PermissionPlan, workspaceSettings.Config.PermissionMode, "Workspace permission should match workspace approval mode")
	})
}

func TestSessionCommandsBehavior(t *testing.T) {
	t.Run("ParseSessionListShouldExtractSessions", func(t *testing.T) {
		raw := `Available sessions:
1. Fix login flow [abc123]
2. Add tests [def456]
`
		sessions := parseSessionList(raw, "/tmp/work", "gemini-2.5-flash")
		require.Len(t, sessions, 2, "Parsed session list should contain two entries")
		assert.Equal(t, "abc123", sessions[0].ID, "First session id should be parsed from bracket section")
		assert.Equal(t, "Fix login flow", sessions[0].Name, "First session name should be parsed from numbered line")
		assert.Equal(t, codeagent.Provider("gemini"), sessions[0].Provider, "Parsed session provider should be gemini")
		assert.Equal(t, "gemini-2.5-flash", sessions[0].Model, "Parsed session model should match parser input")
	})

	t.Run("DeleteShouldRejectEmptyID", func(t *testing.T) {
		agent := &geminiAgent{workDir: t.TempDir()}
		_, err := agent.Delete(codeagent.DeleteSessionParams{ID: ""})
		require.Error(t, err, "Delete should return an error when session id is empty")
		assert.Contains(t, err.Error(), "empty session id", "Delete error should mention empty session id")
	})

	t.Run("CreateShouldProbeVersionAndPersistSessionConfig", func(t *testing.T) {
		workDir := t.TempDir()
		binDir := t.TempDir()
		logPath := filepath.Join(t.TempDir(), "gemini-create.log")
		installFakeGeminiBinary(t, binDir, logPath)
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		agent := &geminiAgent{workDir: workDir}
		result, err := agent.Create(codeagent.CreateSessionParams{
			ID:             "session-create",
			Name:           "create-test",
			Model:          ModelGemini25Pro,
			PermissionMode: codeagent.PermissionAcceptEdits,
			SystemPrompt:   "system prompt",
		})
		require.NoError(t, err, "Create should succeed with a reachable gemini binary")
		require.NotNil(t, result, "Create result should be returned")
		assert.Equal(t, "actual-session-create", result.ID, "Create result id should match discovered Gemini session id")
		assert.Equal(t, "create-test", result.Name, "Create result name should match request name")

		logLines := readLogLines(t, logPath)
		require.NotEmpty(t, logLines, "Create should invoke the gemini binary")
		assert.Subset(t, logLines, []string{
			"--version",
			"--help",
			"-p reply-with-hi-session-create",
			"--list-sessions",
		}, "Create should probe auth, seed a prompt, and list sessions")

		settingsPath := filepath.Join(workDir, ".gemini", "settings.json")
		settings, err := readSettingsFile(settingsPath)
		require.NoError(t, err, "Create should persist workspace settings")
		require.NotNil(t, settings.Model.Name, "Create should persist a model name")
		assert.Equal(t, string(ModelGemini25Pro), *settings.Model.Name, "Create should persist the requested model")
		assert.Equal(t, SettingsSchemaJsonGeneralDefaultApprovalMode("auto_edit"), settings.General.DefaultApprovalMode, "Create should persist the mapped approval mode")
	})

	t.Run("ResumeShouldAttachToShellAndStopCleanly", func(t *testing.T) {
		workDir := t.TempDir()
		binDir := t.TempDir()
		logPath := filepath.Join(t.TempDir(), "gemini-resume.log")
		installFakeGeminiBinary(t, binDir, logPath)
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("SHELL", "/bin/bash")

		stdoutBuffer := &bytes.Buffer{}
		stderrBuffer := &bytes.Buffer{}
		previousStdout := interactiveStdout
		previousStderr := interactiveStderr
		previousStdin := interactiveStdin
		interactiveStdout = stdoutBuffer
		interactiveStderr = stderrBuffer
		interactiveStdin = strings.NewReader("")
		t.Cleanup(func() {
			interactiveStdout = previousStdout
			interactiveStderr = previousStderr
			interactiveStdin = previousStdin
		})

		agent := &geminiAgent{workDir: workDir}
		result, err := agent.Resume(codeagent.ResumeSessionParams{ID: "resume-123"})
		require.NoError(t, err, "Resume should start an interactive shell command")
		require.NotNil(t, result, "Resume result should be returned")
		assert.Equal(t, "resume-123", result.SessionID, "Resume session id should match the requested id")
		assert.NotEmpty(t, result.ProcessID, "Resume process id should be populated")

		require.Eventually(t, func() bool {
			return len(readLogLines(t, logPath)) > 0
		}, 3*time.Second, 50*time.Millisecond, "Resume should invoke the gemini binary through the shell")

		logLines := readLogLines(t, logPath)
		assert.Contains(t, logLines[0], "--resume", "Resume command should include the resume flag")
		assert.Contains(t, logLines[0], "resume-123", "Resume command should include the requested session id")

		require.Eventually(t, func() bool {
			return strings.Contains(stdoutBuffer.String(), "interactive-started")
		}, 3*time.Second, 50*time.Millisecond, "Resume should attach stdout to the current terminal writer")

		agent.Stop()
		assert.Eventually(t, func() bool {
			agent.mu.RLock()
			defer agent.mu.RUnlock()
			return agent.activeCmd == nil
		}, 2*time.Second, 50*time.Millisecond, "Stop should clear the active command after terminating the process")
	})
}

func installFakeGeminiBinary(t *testing.T, dir, logPath string) {
	t.Helper()

	script := strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		"printf '%s\\n' \"$*\" >> \"" + logPath + "\"",
		"if [[ \"${1:-}\" == \"--version\" ]]; then",
		"  printf '0.37.0\\n'",
		"  exit 0",
		"fi",
		"if [[ \"${1:-}\" == \"--help\" ]]; then",
		"  printf 'Gemini CLI help\\n'",
		"  exit 0",
		"fi",
		"if [[ \"${1:-}\" == \"-p\" ]]; then",
		"  printf 'hi\\n'",
		"  exit 0",
		"fi",
		"if [[ \"${1:-}\" == \"--list-sessions\" ]]; then",
		"  printf 'available sessions for this project (1):\\n'",
		"  printf '  1. reply-with-hi-session-create (Just now) [actual-session-create]\\n'",
		"  exit 0",
		"fi",
		"if [[ \"${1:-}\" == \"--resume\" ]]; then",
		"  printf 'interactive-started\\n'",
		"  read -r -t 30 _ || true",
		"  exit 0",
		"fi",
		"printf 'unexpected args: %s\\n' \"$*\" >&2",
		"exit 1",
	}, "\n")

	path := filepath.Join(dir, "gemini")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755), "Fake gemini binary should be written")
}

func readLogLines(t *testing.T, logPath string) []string {
	t.Helper()

	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		require.NoError(t, err, "Fake gemini log should be readable")
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}
