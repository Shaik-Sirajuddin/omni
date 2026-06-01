package gemini

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Shaik-Sirajuddin/omni/connector/codeagent"
	sandbox "github.com/Shaik-Sirajuddin/omni/sandbox/provider"
)

// sandboxFlagValue maps sandbox policy to Gemini settings value.
// sandbox disabled — always returns "" so no sandbox flag is applied.
func sandboxFlagValue(_ *sandbox.Config) string {
	// if s == nil { return "" }
	// if s.AgentPolicy != nil && sandbox.AgentFSPolicy(s.AgentPolicy.FSPolicy) == sandbox.Inherit { return "danger-full-access" }
	// return "read-only"
	return "" // sandbox disabled
}

func (a *geminiAgent) GetSessionSandbox(_ codeagent.GetSessionSandboxParams) (*codeagent.GetSessionSandboxResult, error) {
	// sandbox disabled
	// return &codeagent.GetSessionSandboxResult{Sandbox: a.sbx}, nil
	return &codeagent.GetSessionSandboxResult{}, nil
}

func (a *geminiAgent) UpdateSessionSandbox(_ codeagent.UpdateSessionSandboxParams) (*codeagent.UpdateSessionSandboxResult, error) {
	// sandbox disabled
	// a.mu.Lock(); a.sbx = p.Sandbox; a.mu.Unlock(); syncSandboxConfig
	return &codeagent.UpdateSessionSandboxResult{}, nil
}

func syncModelAndModeConfig(workDir, model string, mode codeagent.PermissionMode) error {
	return writeWorkspaceSettings(workDir, func(s *SettingsSchemaJson) {
		if model != "" {
			s.Model.Name = stringPtr(model)
		}
		if am := approvalModeFlag(mode); am != "" {
			s.General.DefaultApprovalMode = SettingsSchemaJsonGeneralDefaultApprovalMode(am)
		}
	})
}

func (a *geminiAgent) syncSandboxConfig() error {
	// sandbox disabled — no-op
	// flag := sandboxFlagValue(a.sbx); writeWorkspaceSettings(a.workDir, func(s) { s.Tools.Sandbox = flag })
	return nil
}

func writeWorkspaceSettings(workDir string, mutateFn func(*SettingsSchemaJson)) error {
	settingsDir := filepath.Join(workDir, ".gemini")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return fmt.Errorf("gemini: mkdir %s: %w", settingsDir, err)
	}

	settingsPath := filepath.Join(settingsDir, "settings.json")
	settings, err := readSettingsFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	mutateFn(&settings)
	return writeSettingsFile(settingsPath, settings)
}
