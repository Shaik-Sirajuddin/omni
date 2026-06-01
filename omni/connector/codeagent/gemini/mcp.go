package gemini

import (
	codeagent "github.com/Shaik-Sirajuddin/omni/connector/codeagent"
)

func (a *geminiAgent) AddMCP(_ codeagent.AddMCPParams) (*codeagent.AddMCPResult, error) {
	return nil, codeagent.ErrMCPNotSupported
}

func (a *geminiAgent) ListMCP(_ codeagent.ListMCPParams) (*codeagent.ListMCPResult, error) {
	return &codeagent.ListMCPResult{}, nil
}

func (a *geminiAgent) DeleteMCP(_ codeagent.DeleteMCPParams) (*codeagent.DeleteMCPResult, error) {
	return nil, codeagent.ErrMCPNotSupported
}

func (a *geminiAgent) SetMCPToolPrompt(_ codeagent.SetMCPToolPromptParams) (*codeagent.SetMCPToolPromptResult, error) {
	return nil, codeagent.ErrMCPNotSupported
}
