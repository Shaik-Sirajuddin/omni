//go:build unit || integration

package engine

import (
	"context"
	"math"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
)

// StartForTest sets the engine's lifetime context without starting background services.
func StartForTest(e *ProcessingEngine, ctx context.Context) {
	e.ctx = ctx
}

// RegisterAgentForTest pre-seeds agent state so executeLoop can resolve the agent.
func RegisterAgentForTest(e *ProcessingEngine, agentID, name, workspace, team string) {
	e.state.SetAgent(agentID, AgentState{
		Agent:  Agent{AgentID: agentID, Name: name, Workspace: workspace, Team: team},
		Status: AgentStatusReady,
	})
	e.state.SetPending(agentID, false)
}

// SetSessionForTest seeds a CodeSession.SessionID on agentID.
func SetSessionForTest(e *ProcessingEngine, agentID, sessionID string) {
	state, _ := e.state.GetAgent(agentID)
	state.CodeSession.SessionID = sessionID
	e.state.SetAgent(agentID, state)
}

// OnSessionEndForTest calls onSessionEnd with the given generation value.
func OnSessionEndForTest(e *ProcessingEngine, agentID string, msgs []*message.Message, execFailed bool, generation int) {
	e.onSessionEnd(agentID, msgs, execFailed, generation)
}

// GetAgentStateForTest returns a copy of the current AgentState for agentID.
func GetAgentStateForTest(e *ProcessingEngine, agentID string) (AgentState, bool) {
	return e.state.GetAgent(agentID)
}

// SetAgentStatusForTest sets the agent status directly.
func SetAgentStatusForTest(e *ProcessingEngine, agentID string, status AgentStatus) {
	state, _ := e.state.GetAgent(agentID)
	state.Status = status
	e.state.SetAgent(agentID, state)
}

// IncrementGenerationForTest increments SessionGeneration so onSessionEnd detects a newer session.
func IncrementGenerationForTest(e *ProcessingEngine, agentID string) {
	state, _ := e.state.GetAgent(agentID)
	state.CodeSession.SessionGeneration++
	e.state.SetAgent(agentID, state)
}

// RunQueueSweepOnceForTest runs one sweep pass treating all queued messages as stale.
// Uses MaxInt64 cutoff so every queued message is eligible regardless of queue_time.
func RunQueueSweepOnceForTest(e *ProcessingEngine, ctx context.Context) {
	cutoff := int64(math.MaxInt64)
	stale, err := e.msgStore.RawQuery(ctx,
		`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id, task_id, creator_agent_id, schema
		 FROM messages WHERE status = ? AND queue_time > 0 AND queue_time < ?`,
		string(statusQueued), cutoff,
	)
	if err != nil {
		return
	}
	for _, msg := range stale {
		if msg.Retries < maxQueueRetries {
			msg.Status = message.StatusInQueue
			msg.QueueTime = 0
			_ = e.msgStore.UpdateMessage(ctx, msg)
		} else {
			msg.Status = message.StatusFailed
			_ = e.msgStore.UpdateMessage(ctx, msg)
		}
	}
}

// setDeliveryWindowForTest overrides the delivery window after construction.
func setDeliveryWindowForTest(e *ProcessingEngine, d time.Duration) {
	e.deliveryWindow = d
}
