//go:build integration

package engine_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/engine"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── TestProcessingEngineIntegration ─────────────────────────────────────────

func TestProcessingEngineIntegration(t *testing.T) {
	ctx := context.Background()

	// Message Callback Cycle:
	// 1. Two messages queued for "tengine".
	// 2. MessageArrived → executeLoop picks msg-1 → StatusCallback fires → ExecInSession blocks.
	// 3. Test receives first StatusCallback, asserts payload.
	// 4. Test waits for ExecInSession entry (which carries its per-call release channel).
	// 5. Test releases first exec via entry.release → ExecInSession returns → onSessionEnd runs.
	// 6. Test calls AgentCallback to mark msg-1 delivered → engine picks msg-2.
	// 7. Second StatusCallback fires; test receives, releases second exec.
	// 8. Asserts msg-1 is delivered and DeliveryTime is set.
	t.Run("Message Callback Cycle", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		omni := newBlockingOmniCLI()
		statusCB := newWaitableStatusCallback()

		processor := engine.New(msgStore,
			engine.WithTestBinary(omni),
			engine.WithStatusCallback(statusCB),
		)
		engine.StartForTest(processor, ctx)
		engine.RegisterAgentForTest(processor, "tengine", "tengine", "/ws", "team")

		firstMessage := testEngineMessage("msg-engine-1", "tengine", "head", 1000, false)
		secondMessage := testEngineMessage("msg-engine-2", "tengine", "head", 2000, false)
		insertEngineMessages(t, ctx, msgStore, firstMessage, secondMessage)

		// Trigger first delivery; ExecInSession blocks inside the executeLoop goroutine.
		processor.MessageArrived(ctx, "head", "tengine")

		// StatusCallback fires before ExecInSession; receive and assert it.
		cb1 := statusCB.waitForCallback(t)
		assert.Equal(t, firstMessage.ID, cb1.messageID, "first StatusCallback must carry first message ID")
		assert.Equal(t, "tengine", cb1.agentName, "first StatusCallback must carry agent name")

		// Receive the exec entry (contains the per-call release channel).
		e1 := omni.waitForExec(t)
		assert.Equal(t, "tengine", e1.agentID)
		assert.Equal(t, []string{"tengine"}, omni.execCalls(), "one ExecInSession before first release")

		// Release first exec → ExecInSession returns → onSessionEnd runs.
		close(e1.relCh)

		// AgentCallback marks msg-1 delivered and triggers executeLoop for msg-2.
		// Give onSessionEnd time to mark msg-1's status before AgentCallback queries it.
		time.Sleep(10 * time.Millisecond)
		processor.AgentCallback(ctx, engine.AgentCallbackRequest{
			Source:  engine.MessageRef{MessageID: firstMessage.ID},
			AgentID: "tengine",
		}, false)

		// Second StatusCallback fires when msg-2 is picked.
		cb2 := statusCB.waitForCallback(t)
		assert.Equal(t, secondMessage.ID, cb2.messageID, "second StatusCallback must carry second message ID")

		// Receive and release second exec.
		e2 := omni.waitForExec(t)
		assert.Equal(t, "tengine", e2.agentID)
		assert.Equal(t, []string{"tengine", "tengine"}, omni.execCalls(), "two ExecInSession calls total")
		close(e2.relCh)

		// Allow onSessionEnd for msg-2 to settle before asserting msg-1.
		time.Sleep(10 * time.Millisecond)

		// msg-1 must be delivered by the AgentCallback → handlePostExec path.
		delivered, err := msgStore.GetMessage(ctx, firstMessage.ID)
		require.NoError(t, err)
		assert.Equal(t, message.StatusDelivered, delivered.Status, "AgentCallback must mark msg-1 delivered")
		assert.NotNil(t, delivered.DeliveryTime, "AgentCallback must set DeliveryTime on msg-1")
	})
}

// ─── blockingOmniCLI ─────────────────────────────────────────────────────────

// execEntry carries the agentID, prompt, and a per-call release channel for one ExecInSession call.
// The test closes entry.relCh to unblock the blocking call.
type execEntry struct {
	agentID string
	prompt  string
	relCh   chan struct{}
}

// blockingOmniCLI blocks each ExecInSession call until the test closes the
// per-call release channel returned by waitForExec. Each call gets its own
// independent channel so concurrent or sequential calls cannot interfere.
type blockingOmniCLI struct {
	mu     sync.Mutex
	execs  []string
	execCh chan execEntry // buffered; one entry per ExecInSession call
}

func newBlockingOmniCLI() *blockingOmniCLI {
	return &blockingOmniCLI{execCh: make(chan execEntry, 10)}
}

func (c *blockingOmniCLI) ExecInSession(_ context.Context, agentID, _, _, prompt string) error {
	rel := make(chan struct{}) // per-call release channel
	c.mu.Lock()
	c.execs = append(c.execs, agentID)
	c.mu.Unlock()
	c.execCh <- execEntry{agentID: agentID, prompt: prompt, relCh: rel}
	<-rel // block until test closes this channel
	return nil
}

func (c *blockingOmniCLI) GetPromptState(_ context.Context, _ string) (string, error) {
	return "", nil
}

// waitForExec blocks until an ExecInSession is called and returns the entry.
// The caller must close entry.relCh to unblock that specific call.
func (c *blockingOmniCLI) waitForExec(t *testing.T) execEntry {
	t.Helper()
	select {
	case e := <-c.execCh:
		return e
	case <-time.After(2 * time.Second):
		require.FailNow(t, "ExecInSession not called within timeout")
		return execEntry{}
	}
}

func (c *blockingOmniCLI) execCalls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.execs))
	copy(out, c.execs)
	return out
}

// ─── waitableStatusCallback ──────────────────────────────────────────────────

type callbackPayload struct {
	messageID string
	agentName string
	teamName  string
}

// waitableStatusCallback captures SendStatusCallback invocations and exposes a
// blocking Wait() so tests can synchronise on the next callback.
type waitableStatusCallback struct {
	ch chan callbackPayload
}

func newWaitableStatusCallback() *waitableStatusCallback {
	return &waitableStatusCallback{ch: make(chan callbackPayload, 10)}
}

func (w *waitableStatusCallback) SendStatusCallback(_ context.Context, messageID, agentName, teamName string) {
	w.ch <- callbackPayload{messageID, agentName, teamName}
}

func (w *waitableStatusCallback) SendStatusCallbackBatch(_ context.Context, messageIDs []string, agentName, teamName string) {
	for _, id := range messageIDs {
		w.ch <- callbackPayload{id, agentName, teamName}
	}
}

func (w *waitableStatusCallback) waitForCallback(t *testing.T) callbackPayload {
	t.Helper()
	select {
	case p := <-w.ch:
		return p
	case <-time.After(2 * time.Second):
		require.FailNow(t, "StatusCallback not received within timeout")
		return callbackPayload{}
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func testEngineMessage(id, to, by string, sentTime int64, shouldReply bool) *message.Message {
	return &message.Message{
		ID:          id,
		To:          to,
		From:        by,
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: message.RequestTypeExecute,
		ShouldReply: shouldReply,
		Prompt:      "execute test command",
		Refs:        "{}",
		Status:      message.StatusInQueue,
		SentTime:    sentTime,
	}
}

func insertEngineMessages(t *testing.T, ctx context.Context, msgStore message.MessageStore, msgs ...*message.Message) {
	t.Helper()
	for _, msg := range msgs {
		require.NoError(t, msgStore.InsertMessage(ctx, msg), "InsertMessage should seed engine integration test data")
	}
}
