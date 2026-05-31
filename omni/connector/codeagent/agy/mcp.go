package agy

import (
	"fmt"

	codeagent "github.com/Shaik-Sirajuddin/memory/connector/codeagent"
)

func (a *agyAgent) AddMCP(_ codeagent.AddMCPParams) (*codeagent.AddMCPResult, error) {
	return nil, fmt.Errorf("agy: MCP not supported")
}

func (a *agyAgent) ListMCP(_ codeagent.ListMCPParams) (*codeagent.ListMCPResult, error) {
	return &codeagent.ListMCPResult{}, nil
}

func (a *agyAgent) DeleteMCP(_ codeagent.DeleteMCPParams) (*codeagent.DeleteMCPResult, error) {
	return nil, fmt.Errorf("agy: MCP not supported")
}

func (a *agyAgent) SetMCPToolPrompt(_ codeagent.SetMCPToolPromptParams) (*codeagent.SetMCPToolPromptResult, error) {
	return nil, fmt.Errorf("agy: MCP not supported")
}
