package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Binary shells out to the real omni binary.
type Binary struct {
	binaryPath string
}

// New returns a Binary-backed OmniCLI using the binary at binaryPath.
func New(binaryPath string) *Binary {
	return &Binary{binaryPath: binaryPath}
}

func (c *Binary) ExecInSession(ctx context.Context, agentID, agentName, workspace, prompt string) error {
	logger.Debug("exec in session", "agent_id", agentID, "agent_name", agentName, "workspace", workspace, "binary", c.binaryPath)

	cmd := exec.CommandContext(ctx, c.binaryPath, "agent", "exec", agentName, "--bg", "--prompt", prompt)
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("exec in session failed", "agent_id", agentID, "agent_name", agentName, "err", err, "output", string(out))
		return fmt.Errorf("exec in session %q: %w", agentName, err)
	}

	logger.Debug("exec in session done", "agent_id", agentID, "agent_name", agentName, "output", string(out))
	return nil
}

func (c *Binary) GetPromptState(ctx context.Context, agentID string) (string, error) {
	logger.Debug("get prompt state", "agent_id", agentID)

	cmd := exec.CommandContext(ctx, c.binaryPath, "agent", "prompt-state", "--agent", agentID)
	out, err := cmd.Output()
	if err != nil {
		logger.Error("get prompt state failed", "agent_id", agentID, "err", err)
		return "", fmt.Errorf("get prompt state for %q: %w", agentID, err)
	}

	state := strings.TrimSpace(string(out))
	logger.Debug("prompt state retrieved", "agent_id", agentID, "empty", state == "")
	return state, nil
}
