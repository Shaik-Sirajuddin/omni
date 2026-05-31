//go:build integration

package engine

import "context"

// RegisterAgentForTest pre-seeds agent state so executeLoop does not need a
// real agent store to resolve the agent.
func RegisterAgentForTest(e *ProcessingEngine, agentID, name, workspace, team string) {
	e.state.SetAgent(agentID, AgentState{
		Agent: Agent{
			AgentID:   agentID,
			Name:      name,
			Workspace: workspace,
			Team:      team,
		},
		Status: AgentStatusReady,
	})
	e.state.SetPending(agentID, false)
}

// StartForTest sets the engine's lifetime context without starting background
// services, so tests can call MessageArrived / AgentCallback / OnStop without
// a full Run().
func StartForTest(e *ProcessingEngine, ctx context.Context) {
	e.ctx = ctx
}
