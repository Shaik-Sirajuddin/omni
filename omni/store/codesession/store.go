package codesession

import (
	"database/sql"
	"fmt"
	"sync"

	"github.com/Shaik-Sirajuddin/omni/omniagent"
	"github.com/Shaik-Sirajuddin/omni/store/database"
	"github.com/Shaik-Sirajuddin/omni/store/utils"
)

var (
	sessionStoreOnce sync.Once
	sessionStore     *sqlCodeSessionStore
	sessionStoreErr  error

	readOnlySessionStoreOnce sync.Once
	readOnlySessionStore     CodeSessionStore
	readOnlySessionStoreErr  error
)

// NewWithDB creates a CodeSessionStore backed by the provided database. Used in tests.
func NewWithDB(db *sql.DB) CodeSessionStore {
	return &sqlCodeSessionStore{db: db}
}

// GetCodeSessionStore returns the singleton CodeSessionStore.
func GetCodeSessionStore() (CodeSessionStore, error) {
	sessionStoreOnce.Do(func() {
		db, err := database.GetDB()
		if err != nil {
			sessionStoreErr = err
			return
		}
		sessionStore = &sqlCodeSessionStore{db: db}
	})
	return sessionStore, sessionStoreErr
}

// GetReadOnlyCodeSessionStore returns a singleton read-only CodeSessionStore.
//
// This is intended for viewers, inspectors, dashboards, or CLI commands that only
// need to read existing session data.
//
// It opens the SQLite database with mode=ro, so:
//   - the DB file must already exist
//   - writes will fail
//   - schema migrations are not applied here
//
// Use GetCodeSessionStore for normal read/write app usage.
func GetReadOnlyCodeSessionStore() (CodeSessionStore, error) {
	readOnlySessionStoreOnce.Do(func() {
		db, err := database.GetReadOnlyDB()
		if err != nil {
			readOnlySessionStoreErr = err
			return
		}

		readOnlySessionStore = &sqlCodeSessionStore{db: db}
	})

	return readOnlySessionStore, readOnlySessionStoreErr
}

// GetSessionByID looks up a session by its own ID and returns the owning agentID.
func (s *sqlCodeSessionStore) GetSessionByID(sessionID string) (string, *omniagent.CodeSession, error) {
	var agentID string
	row := s.db.QueryRow(
		`SELECT agent_id, id, model_provider, model_name, idx, is_active, prompts, last_sync_prompt,
		        status, stop_reason,
		        tokens_input, tokens_output, tokens_cached_input, tokens_cached_output,
		        tokens_max, tokens_consumed_percent
		 FROM code_sessions WHERE id = ? LIMIT 1`,
		sessionID,
	)
	var (
		id, provider, modelName           string
		idx, prompts, lastSync            int
		isActive                          int
		status, stopReason                string
		tokIn, tokOut, tokCIn, tokCOut, tokMax int
		tokPct                            float64
	)
	if err := row.Scan(
		&agentID, &id, &provider, &modelName, &idx, &isActive, &prompts, &lastSync,
		&status, &stopReason,
		&tokIn, &tokOut, &tokCIn, &tokCOut, &tokMax, &tokPct,
	); err != nil {
		return "", nil, err
	}
	session := &omniagent.CodeSession{
		Id:                 id,
		Model:              utils.BuildModel(provider, modelName),
		Idx:                idx,
		IsActive:           isActive == 1,
		Prompts:            prompts,
		LastSyncPrompt:     lastSync,
		Status:             status,
		StopReason:         stopReason,
		TokensInput:        tokIn,
		TokensOutput:       tokOut,
		TokensCachedInput:  tokCIn,
		TokensCachedOutput: tokCOut,
		TokensMax:          tokMax,
		TokensConsumedPct:  tokPct,
	}
	return agentID, session, nil
}

// GetSession returns the active session for the given agent.
func (s *sqlCodeSessionStore) GetSession(agentID string) (*omniagent.CodeSession, error) {
	row := s.db.QueryRow(
		`SELECT id, model_provider, model_name, idx, is_active, prompts, last_sync_prompt,
		        status, stop_reason, is_interrupted,
		        tokens_input, tokens_output, tokens_cached_input, tokens_cached_output,
		        tokens_max, tokens_consumed_percent
		 FROM code_sessions WHERE agent_id = ? AND is_active = 1 LIMIT 1`,
		agentID,
	)
	return scanSession(row)
}

// CreateSession persists a new session for the given agent.
func (s *sqlCodeSessionStore) CreateSession(agentID string, session *omniagent.CodeSession) error {
	provider, name := utils.ModelFields(session.Model)
	status := session.Status
	if status == "" {
		status = "ready"
	}
	_, err := s.db.Exec(
		`INSERT INTO code_sessions
		 (id, agent_id, model_provider, model_name, idx, is_active, prompts, last_sync_prompt,
		  status, stop_reason, is_interrupted,
		  tokens_input, tokens_output, tokens_cached_input, tokens_cached_output,
		  tokens_max, tokens_consumed_percent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.Id, agentID, provider, name,
		session.Idx, utils.BoolToInt(session.IsActive), session.Prompts, session.LastSyncPrompt,
		status, session.StopReason, utils.BoolToInt(session.IsInterrupted),
		session.TokensInput, session.TokensOutput, session.TokensCachedInput, session.TokensCachedOutput,
		session.TokensMax, session.TokensConsumedPct,
	)
	return err
}

// UpdateSession updates an existing session for the given agent.
func (s *sqlCodeSessionStore) UpdateSession(agentID string, session *omniagent.CodeSession) error {
	provider, name := utils.ModelFields(session.Model)
	res, err := s.db.Exec(
		`UPDATE code_sessions
		 SET model_provider = ?, model_name = ?, idx = ?, is_active = ?, prompts = ?, last_sync_prompt = ?,
		     status = ?, stop_reason = ?, is_interrupted = ?,
		     tokens_input = ?, tokens_output = ?, tokens_cached_input = ?, tokens_cached_output = ?,
		     tokens_max = ?, tokens_consumed_percent = ?
		 WHERE id = ? AND agent_id = ?`,
		provider, name, session.Idx, utils.BoolToInt(session.IsActive), session.Prompts, session.LastSyncPrompt,
		session.Status, session.StopReason, utils.BoolToInt(session.IsInterrupted),
		session.TokensInput, session.TokensOutput, session.TokensCachedInput, session.TokensCachedOutput,
		session.TokensMax, session.TokensConsumedPct,
		session.Id, agentID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("session %q not found for agent %q", session.Id, agentID)
	}
	return nil
}

// ListSessions returns sessions for the given agent, filtered by non-zero fields of filter.
func (s *sqlCodeSessionStore) ListSessions(agentID string, filter *omniagent.CodeSession) ([]*omniagent.CodeSession, error) {
	query := `SELECT id, model_provider, model_name, idx, is_active, prompts, last_sync_prompt,
	                 status, stop_reason, is_interrupted,
	                 tokens_input, tokens_output, tokens_cached_input, tokens_cached_output,
	                 tokens_max, tokens_consumed_percent
	          FROM code_sessions WHERE agent_id = ?`
	args := []any{agentID}

	if filter != nil {
		if filter.Id != "" {
			query += " AND id = ?"
			args = append(args, filter.Id)
		}
		if filter.IsActive {
			query += " AND is_active = 1"
		}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*omniagent.CodeSession
	for rows.Next() {
		session, err := scanSessionRow(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// --- helpers ---

func scanSession(row *sql.Row) (*omniagent.CodeSession, error) {
	var (
		id, provider, modelName              string
		idx, prompts, lastSync               int
		isActive, isInterrupted              int
		status, stopReason                   string
		tokIn, tokOut, tokCIn, tokCOut, tokMax int
		tokPct                               float64
	)
	if err := row.Scan(
		&id, &provider, &modelName, &idx, &isActive, &prompts, &lastSync,
		&status, &stopReason, &isInterrupted,
		&tokIn, &tokOut, &tokCIn, &tokCOut, &tokMax, &tokPct,
	); err != nil {
		return nil, err
	}
	return &omniagent.CodeSession{
		Id:                 id,
		Model:              utils.BuildModel(provider, modelName),
		Idx:                idx,
		IsActive:           isActive == 1,
		Prompts:            prompts,
		LastSyncPrompt:     lastSync,
		Status:             status,
		StopReason:         stopReason,
		IsInterrupted:      isInterrupted == 1,
		TokensInput:        tokIn,
		TokensOutput:       tokOut,
		TokensCachedInput:  tokCIn,
		TokensCachedOutput: tokCOut,
		TokensMax:          tokMax,
		TokensConsumedPct:  tokPct,
	}, nil
}

func scanSessionRow(rows *sql.Rows) (*omniagent.CodeSession, error) {
	var (
		id, provider, modelName              string
		idx, prompts, lastSync               int
		isActive, isInterrupted              int
		status, stopReason                   string
		tokIn, tokOut, tokCIn, tokCOut, tokMax int
		tokPct                               float64
	)
	if err := rows.Scan(
		&id, &provider, &modelName, &idx, &isActive, &prompts, &lastSync,
		&status, &stopReason, &isInterrupted,
		&tokIn, &tokOut, &tokCIn, &tokCOut, &tokMax, &tokPct,
	); err != nil {
		return nil, err
	}
	return &omniagent.CodeSession{
		Id:                 id,
		Model:              utils.BuildModel(provider, modelName),
		Idx:                idx,
		IsActive:           isActive == 1,
		Prompts:            prompts,
		LastSyncPrompt:     lastSync,
		Status:             status,
		StopReason:         stopReason,
		IsInterrupted:      isInterrupted == 1,
		TokensInput:        tokIn,
		TokensOutput:       tokOut,
		TokensCachedInput:  tokCIn,
		TokensCachedOutput: tokCOut,
		TokensMax:          tokMax,
		TokensConsumedPct:  tokPct,
	}, nil
}
