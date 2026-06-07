//go:build unit

package engine

import (
	"context"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
)

func TestSessionGenerationGuard(t *testing.T) {
	ctx := context.Background()

	// When generation is unchanged and status is Running, onSessionEnd resets to Ready.
	t.Run("Running Status Reset To Ready When Generation Unchanged", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-gen", "ag-gen", "/ws", "team")
		SetAgentStatusForTest(proc, "ag-gen", AgentStatusRunning)

		gen, _ := GetAgentStateForTest(proc, "ag-gen")
		capturedGen := gen.CodeSession.SessionGeneration

		OnSessionEndForTest(proc, "ag-gen", nil, false, capturedGen)

		state, _ := GetAgentStateForTest(proc, "ag-gen")
		assert.Equal(t, AgentStatusReady, state.Status,
			"onSessionEnd must reset Running→Ready when generation is unchanged")
	})

	// When generation advanced (markDelivered ran), onSessionEnd must NOT reset Running→Ready.
	t.Run("Running Status Preserved When Generation Advanced", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-adv", "ag-adv", "/ws", "team")
		SetAgentStatusForTest(proc, "ag-adv", AgentStatusRunning)

		// Capture generation BEFORE advancing it — this simulates what executeLoop does.
		genBefore, _ := GetAgentStateForTest(proc, "ag-adv")
		myGeneration := genBefore.CodeSession.SessionGeneration

		// Simulate markDelivered advancing the generation (new session started).
		IncrementGenerationForTest(proc, "ag-adv")

		OnSessionEndForTest(proc, "ag-adv", nil, false, myGeneration)

		state, _ := GetAgentStateForTest(proc, "ag-adv")
		assert.Equal(t, AgentStatusRunning, state.Status,
			"onSessionEnd must NOT reset Running→Ready when generation advanced (newer session took over)")
	})

	// execFailed=true with no messages — nothing to re-queue, agent stays Stopped.
	t.Run("ExecFailed_NoMessages_SetsStopped", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		proc := New(msgStore, WithTestBinary(newLocalBlockingCLI()))
		StartForTest(proc, ctx)
		RegisterAgentForTest(proc, "ag-fail", "ag-fail", "/ws", "team")
		SetAgentStatusForTest(proc, "ag-fail", AgentStatusRunning)

		state, _ := GetAgentStateForTest(proc, "ag-fail")
		OnSessionEndForTest(proc, "ag-fail", nil, true, state.CodeSession.SessionGeneration)

		after, _ := GetAgentStateForTest(proc, "ag-fail")
		assert.Equal(t, AgentStatusStopped, after.Status, "exec failure with no messages must set Stopped")
	})
}

// ─── TestPreprocessingRecall ──────────────────────────────────────────────────
