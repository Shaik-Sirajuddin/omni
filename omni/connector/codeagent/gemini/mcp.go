package gemini

import (
	"fmt"

	codeagent "github.com/Shaik-Sirajuddin/memory/connector/codeagent"
)

func (a *geminiAgent) AddMCP(_ codeagent.AddMCPParams) (*codeagent.AddMCPResult, error) {
	return nil, fmt.Errorf("gemini: MCP not supported")
}

func (a *geminiAgent) ListMCP(_ codeagent.ListMCPParams) (*codeagent.ListMCPResult, error) {
	return &codeagent.ListMCPResult{}, nil
}

func (a *geminiAgent) DeleteMCP(_ codeagent.DeleteMCPParams) (*codeagent.DeleteMCPResult, error) {
	return nil, fmt.Errorf("gemini: MCP not supported")
}

func (a *geminiAgent) SetMCPToolPrompt(_ codeagent.SetMCPToolPromptParams) (*codeagent.SetMCPToolPromptResult, error) {
	return nil, fmt.Errorf("gemini: MCP not supported")
}
