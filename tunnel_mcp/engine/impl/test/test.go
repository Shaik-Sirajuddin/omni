package test

import (
	"context"
)

// OmniCLI is a test double for engine.OmniCLI.
// Configure ExecResponses and PromptStateResponses before use;
// each call pops the next entry. Exhausted slices return zero values.
type OmniCLI struct {
	ExecResponses        []error
	PromptStateResponses []string

	execIdx   int
	promptIdx int
}

// New returns a zero-configured test OmniCLI (all calls succeed, prompt state empty).
func New() *OmniCLI {
	return &OmniCLI{}
}

func (c *OmniCLI) ExecInSession(_ context.Context, agentID, _, workspace, prompt string) error {
	logger.Debug("test exec in session", "agent_id", agentID, "workspace", workspace, "call_index", c.execIdx)

	if c.execIdx >= len(c.ExecResponses) {
		c.execIdx++
		return nil
	}
	err := c.ExecResponses[c.execIdx]
	c.execIdx++
	return err
}

func (c *OmniCLI) GetPromptState(_ context.Context, agentID string) (string, error) {
	logger.Debug("test get prompt state", "agent_id", agentID, "call_index", c.promptIdx)

	if c.promptIdx >= len(c.PromptStateResponses) {
		return "", nil
	}
	state := c.PromptStateResponses[c.promptIdx]
	c.promptIdx++
	return state, nil
}
