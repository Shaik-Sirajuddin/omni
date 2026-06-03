package message

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/database"
)

// MessageStore persists and retrieves messages.
type MessageStore interface {
	InsertMessage(ctx context.Context, msg *Message) error
	UpdateMessage(ctx context.Context, msg *Message) error
	InsertMessagesGroup(ctx context.Context, groupID string, msgs []*Message) error
	GetMessage(ctx context.Context, id string) (*Message, error)
	GetMessages(ctx context.Context, groupID string) ([]*Message, error)
	GetConversationMessages(ctx context.Context, from, to string, page Page) ([]*Message, error)

	// GetPendingAgents returns distinct agent IDs that have messages in
	// in_queue, queued, or processing status. Used on engine restart to
	// re-hydrate agents with orphaned work.
	GetPendingAgents(ctx context.Context) ([]string, error)
	GetWorkspaceForAgent(ctx context.Context, agentID string) (string, error)

	// RawQuery runs a caller-supplied SELECT and scans each row into a Message.
	// The query must select the full message column list in declaration order.
	RawQuery(ctx context.Context, query string, args ...any) ([]*Message, error)
	// RawExec runs a caller-supplied write statement (INSERT / UPDATE / DELETE).
	RawExec(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type sqlMessageStore struct {
	db *sql.DB
}

// New returns a MessageStore backed by the provided database.
func New(db *sql.DB) MessageStore {
	return &sqlMessageStore{db: db}
}

// WithTestDB returns a MessageStore backed by a fresh in-memory SQLite database.
// The database is closed automatically when the test finishes.
func WithTestDB(t *testing.T) MessageStore {
	t.Helper()
	return New(database.WithTestDB(t))
}

func (s *sqlMessageStore) InsertMessage(ctx context.Context, msg *Message) error {
	logger.Debug("insert message", "id", msg.ID, "to", msg.To, "from", msg.From, "request_type", msg.RequestType)

	if len(msg.Prompt) > MaxPromptBytes {
		return fmt.Errorf("prompt exceeds max size of %d bytes", MaxPromptBytes)
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO messages
		 (id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		  responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id,
		  task_id, creator_agent_id, schema)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.To, msg.From, string(msg.FromSpec), string(msg.ToSpec),
		string(msg.RequestType), boolToInt(msg.IsResponse), boolToInt(msg.ShouldReply),
		msg.RespondedTo, msg.Prompt, msg.Refs, msg.Workspace, string(msg.Status),
		msg.Retries, msg.QueueTime, msg.DeliveryTime, msg.SentTime, msg.GroupID,
		msg.TaskID, msg.CreatorAgentID, msg.Schema,
	)
	if err != nil {
		logger.Error("insert message failed", "err", err, "id", msg.ID)
		return err
	}

	logger.Debug("message inserted", "id", msg.ID)
	return nil
}

func (s *sqlMessageStore) UpdateMessage(ctx context.Context, msg *Message) error {
	logger.Debug("update message", "id", msg.ID, "status", msg.Status, "retries", msg.Retries)

	if len(msg.Prompt) > MaxPromptBytes {
		return fmt.Errorf("prompt exceeds max size of %d bytes", MaxPromptBytes)
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE messages
		 SET "to" = ?, "from" = ?, from_spec = ?, to_spec = ?, request_type = ?,
		     is_response = ?, should_reply = ?, responded_to = ?, prompt = ?,
		     refs = ?, workspace = ?, status = ?, retries = ?, queue_time = ?, delivery_time = ?, sent_time = ?, group_id = ?,
		     task_id = ?, creator_agent_id = ?, schema = ?
		 WHERE id = ?`,
		msg.To, msg.From, string(msg.FromSpec), string(msg.ToSpec), string(msg.RequestType),
		boolToInt(msg.IsResponse), boolToInt(msg.ShouldReply), msg.RespondedTo, msg.Prompt,
		msg.Refs, msg.Workspace, string(msg.Status), msg.Retries, msg.QueueTime, msg.DeliveryTime, msg.SentTime, msg.GroupID,
		msg.TaskID, msg.CreatorAgentID, msg.Schema,
		msg.ID,
	)
	if err != nil {
		logger.Error("update message failed", "err", err, "id", msg.ID)
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("message %q not found", msg.ID)
	}

	logger.Debug("message updated", "id", msg.ID)
	return nil
}

func (s *sqlMessageStore) InsertMessagesGroup(ctx context.Context, groupID string, msgs []*Message) error {
	logger.Debug("insert messages group", "group_id", groupID, "count", len(msgs))

	if len(msgs) > MaxGroupMessages {
		return fmt.Errorf("group exceeds max of %d messages", MaxGroupMessages)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		logger.Error("begin transaction failed", "err", err)
		return err
	}
	defer tx.Rollback()

	now := msgs[0].SentTime
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO message_groups (id, created_at, count) VALUES (?, ?, ?)`,
		groupID, now, len(msgs),
	); err != nil {
		logger.Error("insert message group failed", "err", err, "group_id", groupID)
		return err
	}

	for _, msg := range msgs {
		if len(msg.Prompt) > MaxPromptBytes {
			return fmt.Errorf("message %q prompt exceeds max size", msg.ID)
		}
		msg.GroupID = groupID
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO messages
			 (id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
			  responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id,
			  task_id, creator_agent_id, schema)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			msg.ID, msg.To, msg.From, string(msg.FromSpec), string(msg.ToSpec),
			string(msg.RequestType), boolToInt(msg.IsResponse), boolToInt(msg.ShouldReply),
			msg.RespondedTo, msg.Prompt, msg.Refs, msg.Workspace, string(msg.Status),
			msg.Retries, msg.QueueTime, msg.DeliveryTime, msg.SentTime, msg.GroupID,
			msg.TaskID, msg.CreatorAgentID, msg.Schema,
		); err != nil {
			logger.Error("insert group message failed", "err", err, "id", msg.ID)
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		logger.Error("commit group transaction failed", "err", err, "group_id", groupID)
		return err
	}

	logger.Debug("messages group inserted", "group_id", groupID, "count", len(msgs))
	return nil
}

func (s *sqlMessageStore) GetMessage(ctx context.Context, id string) (*Message, error) {
	logger.Debug("get message", "id", id)

	row := s.db.QueryRowContext(ctx,
		`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id,
		        task_id, creator_agent_id, schema
		 FROM messages WHERE id = ?`, id,
	)
	msg, err := scanMessage(row)
	if err != nil {
		logger.Error("get message failed", "err", err, "id", id)
		return nil, err
	}

	logger.Debug("message retrieved", "id", id, "status", msg.Status)
	return msg, nil
}

func (s *sqlMessageStore) GetMessages(ctx context.Context, groupID string) ([]*Message, error) {
	logger.Debug("get messages by group", "group_id", groupID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id,
		        task_id, creator_agent_id, schema
		 FROM messages WHERE group_id = ? ORDER BY sent_time ASC`, groupID,
	)
	if err != nil {
		logger.Error("get messages failed", "err", err, "group_id", groupID)
		return nil, err
	}
	defer rows.Close()

	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}

	logger.Debug("messages retrieved", "group_id", groupID, "count", len(msgs))
	return msgs, nil
}

func (s *sqlMessageStore) GetConversationMessages(ctx context.Context, from, to string, page Page) ([]*Message, error) {
	logger.Debug("get conversation messages", "from", from, "to", to, "offset", page.Offset, "limit", page.Limit)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id,
		        task_id, creator_agent_id, schema
		 FROM messages
		 WHERE (("to" = ? AND "from" = ?) OR ("to" = ? AND "from" = ?))
		 ORDER BY sent_time ASC
		 LIMIT ? OFFSET ?`,
		to, from, from, to, page.Limit, page.Offset,
	)
	if err != nil {
		logger.Error("get conversation messages failed", "err", err, "from", from, "to", to)
		return nil, err
	}
	defer rows.Close()

	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}

	logger.Debug("conversation messages retrieved", "from", from, "to", to, "count", len(msgs))
	return msgs, nil
}

func (s *sqlMessageStore) GetWorkspaceForAgent(ctx context.Context, agentID string) (string, error) {
	logger.Debug("get workspace for agent", "agent_id", agentID)

	var workspace string
	err := s.db.QueryRowContext(ctx,
		`SELECT workspace FROM messages
		 WHERE "to" = ? AND workspace != ''
		 ORDER BY sent_time DESC
		 LIMIT 1`,
		agentID,
	).Scan(&workspace)
	if err != nil {
		logger.Error("get workspace for agent failed", "err", err, "agent_id", agentID)
		return "", err
	}

	logger.Debug("workspace for agent retrieved", "agent_id", agentID, "workspace", workspace)
	return workspace, nil
}

func (s *sqlMessageStore) GetPendingAgents(ctx context.Context) ([]string, error) {
	logger.Debug("get pending agents")

	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT "to" FROM messages WHERE status IN (?, ?, ?)`,
		string(StatusInQueue), string(StatusQueued), string(StatusProcessing),
	)
	if err != nil {
		logger.Error("get pending agents failed", "err", err)
		return nil, err
	}
	defer rows.Close()

	var agents []string
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			return nil, err
		}
		agents = append(agents, agentID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	logger.Debug("pending agents retrieved", "count", len(agents))
	return agents, nil
}

func (s *sqlMessageStore) RawQuery(ctx context.Context, query string, args ...any) ([]*Message, error) {
	logger.Debug("raw query", "query", query, "args", args)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		logger.Error("raw query failed", "err", err, "query", query)
		return nil, err
	}
	defer rows.Close()

	msgs, err := scanMessages(rows)
	if err != nil {
		logger.Error("raw query scan failed", "err", err)
		return nil, err
	}

	logger.Debug("raw query complete", "count", len(msgs))
	return msgs, nil
}

func (s *sqlMessageStore) RawExec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	logger.Debug("raw exec", "query", query, "args", args)

	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		logger.Error("raw exec failed", "err", err, "query", query)
		return nil, err
	}

	logger.Debug("raw exec complete")
	return res, nil
}

// --- helpers ---

func scanMessage(row *sql.Row) (*Message, error) {
	var (
		id, to, from, fromSpec, toSpec, reqType string
		respondedTo, prompt, refs, workspace    string
		status, groupID                         string
		taskID, creatorAgentID, schema          string
		isResponse, shouldReply                 int
		retries                                 int
		queueTime, sentTime                     int64
		deliveryTime                            *int64
	)
	err := row.Scan(
		&id, &to, &from, &fromSpec, &toSpec, &reqType,
		&isResponse, &shouldReply, &respondedTo, &prompt, &refs, &workspace, &status,
		&retries, &queueTime, &deliveryTime, &sentTime, &groupID,
		&taskID, &creatorAgentID, &schema,
	)
	if err != nil {
		return nil, err
	}
	return &Message{
		ID: id, To: to, From: from,
		FromSpec: Spec(fromSpec), ToSpec: Spec(toSpec),
		RequestType: RequestType(reqType),
		IsResponse:  isResponse == 1, ShouldReply: shouldReply == 1,
		RespondedTo: respondedTo, Prompt: prompt, Refs: refs,
		Workspace: workspace,
		Status:    Status(status), Retries: retries,
		QueueTime:      queueTime,
		DeliveryTime:   deliveryTime, SentTime: sentTime, GroupID: groupID,
		TaskID:         taskID,
		CreatorAgentID: creatorAgentID,
		Schema:         schema,
	}, nil
}

func scanMessages(rows *sql.Rows) ([]*Message, error) {
	var msgs []*Message
	for rows.Next() {
		var (
			id, to, from, fromSpec, toSpec, reqType string
			respondedTo, prompt, refs, workspace    string
			status, groupID                         string
			taskID, creatorAgentID, schema          string
			isResponse, shouldReply                 int
			retries                                 int
			queueTime, sentTime                     int64
			deliveryTime                            *int64
		)
		if err := rows.Scan(
			&id, &to, &from, &fromSpec, &toSpec, &reqType,
			&isResponse, &shouldReply, &respondedTo, &prompt, &refs, &workspace, &status,
			&retries, &queueTime, &deliveryTime, &sentTime, &groupID,
			&taskID, &creatorAgentID, &schema,
		); err != nil {
			return nil, err
		}
		msgs = append(msgs, &Message{
			ID: id, To: to, From: from,
			FromSpec: Spec(fromSpec), ToSpec: Spec(toSpec),
			RequestType: RequestType(reqType),
			IsResponse:  isResponse == 1, ShouldReply: shouldReply == 1,
			RespondedTo: respondedTo, Prompt: prompt, Refs: refs,
			Workspace: workspace,
			Status:    Status(status), Retries: retries,
			QueueTime:      queueTime,
			DeliveryTime:   deliveryTime, SentTime: sentTime, GroupID: groupID,
			TaskID:         taskID,
			CreatorAgentID: creatorAgentID,
			Schema:         schema,
		})
	}
	return msgs, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
