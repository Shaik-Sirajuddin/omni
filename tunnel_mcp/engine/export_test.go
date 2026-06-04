//go:build integration

package engine

import (
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
)

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
