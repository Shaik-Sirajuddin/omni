//go:build integration

package engine

import (
	"context"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
)

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

// SetSessionForTest seeds a CodeSession.SessionID on agentID so warmup sentinel
// tests can simulate an agent that has already started a session.
func SetSessionForTest(e *ProcessingEngine, agentID, sessionID string) {
	state, _ := e.state.GetAgent(agentID)
	state.CodeSession.SessionID = sessionID
	e.state.SetAgent(agentID, state)
}

// PickNextMessagesForTest exposes pickNextMessages for direct whitebox tests of
// the message-batching and mixed-type bundling logic.
func PickNextMessagesForTest(e *ProcessingEngine, agentID string) ([]*message.Message, error) {
	return e.pickNextMessages(agentID, nil)
}

// PickNextMessagesWithBypassForTest exposes pickNextMessages in bypass mode (T2).
func PickNextMessagesWithBypassForTest(e *ProcessingEngine, agentID string, bypassTask TaskKey) ([]*message.Message, error) {
	return e.pickNextMessages(agentID, &bypassTask)
}

// SetTaskMuxForTest seeds TaskMux state for T1/T2 tests.
func SetTaskMuxForTest(e *ProcessingEngine, agentID string, key *TaskKey) {
	e.state.SetTaskMux(agentID, key)
}

// GetAgentStateForTest returns the current AgentState for agentID.
func GetAgentStateForTest(e *ProcessingEngine, agentID string) (AgentState, bool) {
	return e.state.GetAgent(agentID)
}
