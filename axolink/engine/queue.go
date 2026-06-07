package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
)

// messageColumns is the single canonical column list for message SELECTs. Every
// query goes through MessageQueue.ForAgent so this list is defined exactly once
// (it was previously copy-pasted across ~6 RawQuery call sites).
const messageColumns = `id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
	responded_to, prompt, schema, refs, workspace, task_id, creator_agent_id, status, retries, queue_time, delivery_time, sent_time, group_id`

// QueryOpts shapes a grouped retrieval through MessageQueue.ForAgent. The zero
// value (with an empty agentID) selects all messages ordered by sent_time.
type QueryOpts struct {
	Status         message.Status      // single-status filter ("" = ignore)
	StatusIn       []message.Status    // multi-status filter (takes precedence over Status)
	RequestType    message.RequestType // request_type = ? ("" = ignore)
	NotRequestType message.RequestType // request_type != ? ("" = ignore)
	Task           *TaskKey            // hard filter: task_id = ? AND creator_agent_id = ?
	TaskFirst      *TaskKey            // order rows matching this task first (T1)
	StaleBefore    int64               // queue_time > 0 AND queue_time < ? (0 = ignore)
	Limit          int                 // 0 = no limit
}

// MessageQueue is the single source of truth for message persistence and
// retrieval. It wraps the message store; every read/write of message rows in the
// engine routes through it (no direct store access elsewhere).
type MessageQueue interface {
	// writes
	Enqueue(ctx context.Context, m *message.Message) error
	EnqueueGroup(ctx context.Context, groupID string, msgs []*message.Message) error
	Update(ctx context.Context, m *message.Message) error
	Advance(ctx context.Context, ids []string, to message.Status) error

	// reads
	Get(ctx context.Context, id string) (*message.Message, error)
	ForAgent(ctx context.Context, agentID string, opt QueryOpts) ([]*message.Message, error)
	Group(ctx context.Context, groupID string) ([]*message.Message, error)

	// bootstrap / housekeeping
	PendingAgents(ctx context.Context) ([]string, error)
	WorkspaceForAgent(ctx context.Context, agentID string) (string, error)
}

type sqlQueue struct {
	store message.MessageStore
}

func newMessageQueue(store message.MessageStore) MessageQueue {
	return &sqlQueue{store: store}
}

func (q *sqlQueue) Enqueue(ctx context.Context, m *message.Message) error {
	return q.store.InsertMessage(ctx, m)
}

func (q *sqlQueue) EnqueueGroup(ctx context.Context, groupID string, msgs []*message.Message) error {
	return q.store.InsertMessagesGroup(ctx, groupID, msgs)
}

func (q *sqlQueue) Update(ctx context.Context, m *message.Message) error {
	return q.store.UpdateMessage(ctx, m)
}

func (q *sqlQueue) Get(ctx context.Context, id string) (*message.Message, error) {
	return q.store.GetMessage(ctx, id)
}

func (q *sqlQueue) Group(ctx context.Context, groupID string) ([]*message.Message, error) {
	return q.store.GetMessages(ctx, groupID)
}

func (q *sqlQueue) PendingAgents(ctx context.Context) ([]string, error) {
	return q.store.GetPendingAgents(ctx)
}

func (q *sqlQueue) WorkspaceForAgent(ctx context.Context, agentID string) (string, error) {
	return q.store.GetWorkspaceForAgent(ctx, agentID)
}

// Advance transitions each id to the target status. queue_time is managed for the
// transitions that need it (InQueue clears it so the sweep doesn't immediately
// re-flag the message). Other status-bearing fields (delivery_time, retries) are
// owned by the caller via Update.
func (q *sqlQueue) Advance(ctx context.Context, ids []string, to message.Status) error {
	for _, id := range ids {
		m, err := q.store.GetMessage(ctx, id)
		if err != nil {
			return err
		}
		m.Status = to
		if to == message.StatusInQueue {
			m.QueueTime = 0
		}
		if err := q.store.UpdateMessage(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (q *sqlQueue) ForAgent(ctx context.Context, agentID string, opt QueryOpts) ([]*message.Message, error) {
	var conds []string
	var args []any

	if agentID != "" {
		conds = append(conds, `"to" = ?`)
		args = append(args, agentID)
	}
	if len(opt.StatusIn) > 0 {
		ph := make([]string, len(opt.StatusIn))
		for i, s := range opt.StatusIn {
			ph[i] = "?"
			args = append(args, string(s))
		}
		conds = append(conds, "status IN ("+strings.Join(ph, ", ")+")")
	} else if opt.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, string(opt.Status))
	}
	if opt.RequestType != "" {
		conds = append(conds, "request_type = ?")
		args = append(args, string(opt.RequestType))
	}
	if opt.NotRequestType != "" {
		conds = append(conds, "request_type != ?")
		args = append(args, string(opt.NotRequestType))
	}
	if opt.Task != nil {
		conds = append(conds, "task_id = ? AND creator_agent_id = ?")
		args = append(args, opt.Task.TaskID, opt.Task.CreatorAgentID)
	}
	if opt.StaleBefore > 0 {
		conds = append(conds, "queue_time > 0 AND queue_time < ?")
		args = append(args, opt.StaleBefore)
	}

	query := "SELECT " + messageColumns + " FROM messages"
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	if opt.TaskFirst != nil {
		query += " ORDER BY CASE WHEN task_id = ? AND creator_agent_id = ? THEN 0 ELSE 1 END, sent_time ASC"
		args = append(args, opt.TaskFirst.TaskID, opt.TaskFirst.CreatorAgentID)
	} else {
		query += " ORDER BY sent_time ASC"
	}
	if opt.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", opt.Limit)
	}

	return q.store.RawQuery(ctx, query, args...)
}
