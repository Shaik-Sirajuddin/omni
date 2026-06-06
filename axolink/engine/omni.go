package engine

import "context"

// OmniCLI is the interface for interacting with the omni binary.
type OmniCLI interface {
	// ExecInSession triggers agent execution via `omni agent exec`.
	// agentName is the omni-registered name; agentID is the internal UUID used only for logging.
	ExecInSession(ctx context.Context, agentID, agentName, workspace, prompt string) error
	// GetPromptState returns the current pending prompt for the agent.
	// An empty string means no prompt is pending.
	GetPromptState(ctx context.Context, agentID string) (string, error)
}
