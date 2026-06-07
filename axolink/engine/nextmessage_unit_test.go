//go:build unit

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMQ is a minimal MessageQueue for testing NextMessage decisions without a DB.
type fakeMQ struct {
	processing  []*message.Message
	staleQueued []*message.Message
	inqueue     []*message.Message
	updates     []*message.Message // captured Update calls
}

func (f *fakeMQ) ForAgent(_ context.Context, _ string, opt QueryOpts) ([]*message.Message, error) {
	switch {
	case opt.Status == message.StatusProcessing:
		return f.processing, nil
	case opt.Status == statusQueued && opt.StaleBefore > 0:
		return f.staleQueued, nil
	case opt.Status == message.StatusInQueue:
		return f.inqueue, nil
	}
	return nil, nil
}
func (f *fakeMQ) Update(_ context.Context, m *message.Message) error {
	f.updates = append(f.updates, m)
	return nil
}
func (f *fakeMQ) Advance(context.Context, []string, message.Status) error        { return nil }
func (f *fakeMQ) Get(context.Context, string) (*message.Message, error)          { return nil, nil }
func (f *fakeMQ) Enqueue(context.Context, *message.Message) error                { return nil }
func (f *fakeMQ) EnqueueGroup(context.Context, string, []*message.Message) error { return nil }
func (f *fakeMQ) Group(context.Context, string) ([]*message.Message, error)      { return nil, nil }
func (f *fakeMQ) PendingAgents(context.Context) ([]string, error)                { return nil, nil }
func (f *fakeMQ) WorkspaceForAgent(context.Context, string) (string, error)      { return "", nil }

func mkMsg(id string, rt message.RequestType, retries int) *message.Message {
	return &message.Message{ID: id, RequestType: rt, Retries: retries}
}

func TestNextMessageOnStop_DeliverRecallForce(t *testing.T) {
	fq := &fakeMQ{}
	n := newNextMessage(fq, newEngineState(), nil, time.Second)
	processing := []*message.Message{
		mkMsg("instant", reqTypeInstant, 0),  // instant -> always deliver
		mkMsg("confirmed", reqTypeQuery, 0),  // confirmed -> deliver
		mkMsg("recall", reqTypeQuery, 1),     // unconfirmed, retries<=3 -> recall
		mkMsg("exhausted", reqTypeQuery, 99), // unconfirmed, retries>3 -> force
	}
	out, err := n.OnStop(context.Background(), "ag", processing, func(id string) bool { return id == "confirmed" })
	require.NoError(t, err)

	var deliverIDs []string
	for _, m := range out.Deliver {
		deliverIDs = append(deliverIDs, m.ID)
	}
	assert.ElementsMatch(t, []string{"instant", "confirmed"}, deliverIDs)

	require.Len(t, out.Force, 1)
	assert.Equal(t, "exhausted", out.Force[0].ID)

	require.NotNil(t, out.Recall)
	assert.False(t, out.Settle)

	// recall retries incremented and persisted through the queue
	require.Len(t, fq.updates, 1)
	assert.Equal(t, "recall", fq.updates[0].ID)
	assert.Equal(t, 2, fq.updates[0].Retries)
}

func TestNextMessageOnStop_SettleWhenAllResolved(t *testing.T) {
	fq := &fakeMQ{}
	n := newNextMessage(fq, newEngineState(), nil, time.Second)
	processing := []*message.Message{mkMsg("a", reqTypeInstant, 0)}
	out, err := n.OnStop(context.Background(), "ag", processing, func(string) bool { return false })
	require.NoError(t, err)
	assert.Nil(t, out.Recall)
	assert.True(t, out.Settle)
	require.Len(t, out.Deliver, 1)
	assert.Empty(t, out.Force)
}

func TestNextMessageOnPromptSubmit_FailsOrphansKeepsBatch(t *testing.T) {
	fq := &fakeMQ{
		processing:  []*message.Message{mkMsg("m1", reqTypeQuery, 0), mkMsg("m2", reqTypeQuery, 0)},
		staleQueued: []*message.Message{mkMsg("m3", reqTypeQuery, 0)},
	}
	n := newNextMessage(fq, newEngineState(), nil, time.Second)
	out, err := n.OnPromptSubmit(context.Background(), "ag", []string{"m1"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"m2", "m3"}, out.Fail) // m1 is current batch -> not failed
	assert.Equal(t, []string{"m1"}, out.Process)
}

func TestNextMessagePlan_BundlesExecuteCoTask(t *testing.T) {
	fq := &fakeMQ{
		inqueue: []*message.Message{
			{ID: "e1", RequestType: reqTypeExecute, TaskID: "t", CreatorAgentID: "c", From: "s"},
			{ID: "q1", RequestType: reqTypeQuery, TaskID: "t", CreatorAgentID: "c", From: "s"},
			{ID: "q2", RequestType: reqTypeQuery, TaskID: "other", From: "s"},
		},
	}
	n := newNextMessage(fq, newEngineState(), nil, time.Second)
	msgs, err := n.Plan(context.Background(), "ag", nil)
	require.NoError(t, err)
	var ids []string
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	assert.Equal(t, []string{"e1", "q1"}, ids) // bundles co-task q1; excludes other-task q2
}
