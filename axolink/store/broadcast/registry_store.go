package broadcast

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/database"
	"testing"
)

// CallbackType identifies the dispatch transport for a registered MCP client.
type CallbackType string

const (
	CallbackHTTP         CallbackType = "http"
	CallbackHTTPOverUnix CallbackType = "http_over_unix"
	CallbackAGCLI        CallbackType = "ag_cli"
)

// AttemptStatus tracks whether a dispatch attempt succeeded or failed.
type AttemptStatus string

const (
	AttemptPending AttemptStatus = "pending"
	AttemptSuccess AttemptStatus = "success"
	AttemptFailed  AttemptStatus = "failed"
)

// MCPClientEntry is the persisted callback configuration for a registered MCP client.
type MCPClientEntry struct {
	ServerID          string       `json:"server_id"`
	AgentID           string       `json:"agent_id"`
	CallbackToolName  string       `json:"callback_tool_name"`
	CallbackType      CallbackType `json:"callback_type"`
	Endpoint          string       `json:"endpoint"`
	AuthenticationRef string       `json:"authentication_ref,omitempty"`
	UpdatedAt         int64        `json:"updated_at"`
}

// CallbackAttempt records a single dispatch attempt for audit and retry.
type CallbackAttempt struct {
	ID          string        `json:"id"`
	MessageID   string        `json:"message_id"`
	ServerID    string        `json:"server_id"`
	Status      AttemptStatus `json:"status"`
	Error       string        `json:"error,omitempty"`
	AttemptedAt int64         `json:"attempted_at"`
}

// RegistryStore persists MCP client callback configurations and dispatch attempts.
type RegistryStore interface {
	Upsert(ctx context.Context, entry MCPClientEntry) error
	Get(ctx context.Context, serverID string) (*MCPClientEntry, error)
	List(ctx context.Context) ([]*MCPClientEntry, error)
	Delete(ctx context.Context, serverID string) error
	RecordAttempt(ctx context.Context, attempt CallbackAttempt) error
	ListAttempts(ctx context.Context, messageID string) ([]*CallbackAttempt, error)
}

type sqlRegistryStore struct {
	db *sql.DB
}

// New returns a RegistryStore backed by the provided database.
func New(db *sql.DB) RegistryStore {
	return &sqlRegistryStore{db: db}
}

// WithTestDB returns a RegistryStore backed by a fresh in-memory SQLite database.
func WithTestDB(t *testing.T) RegistryStore {
	t.Helper()
	return New(database.WithTestDB(t))
}

func (s *sqlRegistryStore) Upsert(ctx context.Context, entry MCPClientEntry) error {
	logger.Debug("upsert mcp client", "server_id", entry.ServerID, "agent_id", entry.AgentID, "callback_type", entry.CallbackType)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO broadcast_mcp_clients
		 (server_id, agent_id, callback_tool_name, callback_type, endpoint, authentication_ref, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(server_id) DO UPDATE SET
		   agent_id           = excluded.agent_id,
		   callback_tool_name = excluded.callback_tool_name,
		   callback_type      = excluded.callback_type,
		   endpoint           = excluded.endpoint,
		   authentication_ref = excluded.authentication_ref,
		   updated_at         = excluded.updated_at`,
		entry.ServerID, entry.AgentID, entry.CallbackToolName,
		string(entry.CallbackType), entry.Endpoint, entry.AuthenticationRef, entry.UpdatedAt,
	)
	if err != nil {
		logger.Error("upsert mcp client failed", "err", err, "server_id", entry.ServerID)
		return err
	}

	logger.Debug("mcp client upserted", "server_id", entry.ServerID)
	return nil
}

func (s *sqlRegistryStore) Get(ctx context.Context, serverID string) (*MCPClientEntry, error) {
	logger.Debug("get mcp client", "server_id", serverID)

	row := s.db.QueryRowContext(ctx,
		`SELECT server_id, agent_id, callback_tool_name, callback_type, endpoint, authentication_ref, updated_at
		 FROM broadcast_mcp_clients WHERE server_id = ?`, serverID,
	)
	entry, err := scanEntry(row)
	if err != nil {
		logger.Error("get mcp client failed", "err", err, "server_id", serverID)
		return nil, err
	}

	logger.Debug("mcp client retrieved", "server_id", serverID, "callback_type", entry.CallbackType)
	return entry, nil
}

func (s *sqlRegistryStore) List(ctx context.Context) ([]*MCPClientEntry, error) {
	logger.Debug("list mcp clients")

	rows, err := s.db.QueryContext(ctx,
		`SELECT server_id, agent_id, callback_tool_name, callback_type, endpoint, authentication_ref, updated_at
		 FROM broadcast_mcp_clients ORDER BY updated_at DESC`,
	)
	if err != nil {
		logger.Error("list mcp clients failed", "err", err)
		return nil, err
	}
	defer rows.Close()

	var entries []*MCPClientEntry
	for rows.Next() {
		var e MCPClientEntry
		if err := rows.Scan(&e.ServerID, &e.AgentID, &e.CallbackToolName,
			(*string)(&e.CallbackType), &e.Endpoint, &e.AuthenticationRef, &e.UpdatedAt,
		); err != nil {
			return nil, err
		}
		entries = append(entries, &e)
	}

	logger.Debug("mcp clients listed", "count", len(entries))
	return entries, rows.Err()
}

func (s *sqlRegistryStore) Delete(ctx context.Context, serverID string) error {
	logger.Debug("delete mcp client", "server_id", serverID)

	res, err := s.db.ExecContext(ctx,
		`DELETE FROM broadcast_mcp_clients WHERE server_id = ?`, serverID,
	)
	if err != nil {
		logger.Error("delete mcp client failed", "err", err, "server_id", serverID)
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("mcp client %q not found", serverID)
	}

	logger.Debug("mcp client deleted", "server_id", serverID)
	return nil
}

func (s *sqlRegistryStore) RecordAttempt(ctx context.Context, attempt CallbackAttempt) error {
	logger.Debug("record callback attempt", "id", attempt.ID, "message_id", attempt.MessageID, "server_id", attempt.ServerID, "status", attempt.Status)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO broadcast_callback_attempts (id, message_id, server_id, status, error, attempted_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		attempt.ID, attempt.MessageID, attempt.ServerID,
		string(attempt.Status), attempt.Error, attempt.AttemptedAt,
	)
	if err != nil {
		logger.Error("record callback attempt failed", "err", err, "id", attempt.ID)
		return err
	}

	logger.Debug("callback attempt recorded", "id", attempt.ID)
	return nil
}

func (s *sqlRegistryStore) ListAttempts(ctx context.Context, messageID string) ([]*CallbackAttempt, error) {
	logger.Debug("list callback attempts", "message_id", messageID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, message_id, server_id, status, error, attempted_at
		 FROM broadcast_callback_attempts WHERE message_id = ? ORDER BY attempted_at ASC`, messageID,
	)
	if err != nil {
		logger.Error("list callback attempts failed", "err", err, "message_id", messageID)
		return nil, err
	}
	defer rows.Close()

	var attempts []*CallbackAttempt
	for rows.Next() {
		var a CallbackAttempt
		if err := rows.Scan(&a.ID, &a.MessageID, &a.ServerID,
			(*string)(&a.Status), &a.Error, &a.AttemptedAt,
		); err != nil {
			return nil, err
		}
		attempts = append(attempts, &a)
	}

	logger.Debug("callback attempts listed", "message_id", messageID, "count", len(attempts))
	return attempts, rows.Err()
}

// --- helpers ---

func scanEntry(row *sql.Row) (*MCPClientEntry, error) {
	var e MCPClientEntry
	err := row.Scan(
		&e.ServerID, &e.AgentID, &e.CallbackToolName,
		(*string)(&e.CallbackType), &e.Endpoint, &e.AuthenticationRef, &e.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &e, nil
}
