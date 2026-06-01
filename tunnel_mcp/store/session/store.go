package session

import (
	"context"
	"database/sql"
	"time"

	"github.com/Shaik-Sirajuddin/omni/mcp/store/database"
)

// PromptSessionStore tracks which prompts have already been delivered to an
// agent session, allowing the engine to deduplicate re-queued messages.
type PromptSessionStore interface {
	// IsDelivered reports whether promptID has been delivered to sessionID.
	IsDelivered(ctx context.Context, sessionID, promptID string) bool
	// MarkDelivered records that promptID was delivered to sessionID.
	MarkDelivered(ctx context.Context, sessionID, promptID string) error
	// Cleanup removes records older than olderThanMs milliseconds (Unix epoch).
	Cleanup(ctx context.Context, olderThanMs int64) error
}

type sqlPromptSessionStore struct {
	db *sql.DB
}

// NewPromptSessionStore returns a PromptSessionStore backed by the default DB.
func NewPromptSessionStore() (PromptSessionStore, error) {
	db, err := database.GetDefaultDB()
	if err != nil {
		return nil, err
	}
	return &sqlPromptSessionStore{db: db}, nil
}

// NewPromptSessionStoreFromDB returns a PromptSessionStore backed by db.
func NewPromptSessionStoreFromDB(db *sql.DB) PromptSessionStore {
	return &sqlPromptSessionStore{db: db}
}

func (s *sqlPromptSessionStore) IsDelivered(ctx context.Context, sessionID, promptID string) bool {
	var delivered int
	err := s.db.QueryRowContext(ctx,
		`SELECT delivered FROM prompt_sessions WHERE session_id = ? AND prompt_id = ?`,
		sessionID, promptID,
	).Scan(&delivered)
	if err != nil {
		return false
	}
	return delivered == 1
}

func (s *sqlPromptSessionStore) MarkDelivered(ctx context.Context, sessionID, promptID string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO prompt_sessions (session_id, prompt_id, delivered, created_at)
		 VALUES (?, ?, 1, ?)
		 ON CONFLICT(session_id, prompt_id) DO UPDATE SET delivered = 1`,
		sessionID, promptID, now,
	)
	return err
}

func (s *sqlPromptSessionStore) Cleanup(ctx context.Context, olderThanMs int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM prompt_sessions WHERE created_at < ?`,
		olderThanMs,
	)
	return err
}
