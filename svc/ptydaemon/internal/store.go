package internal

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const createTableSQL = `
CREATE TABLE IF NOT EXISTS pty_sessions (
    agent_id    TEXT     NOT NULL,
    session_id  TEXT     NOT NULL,
    pid         INTEGER  NOT NULL,
    status      TEXT     NOT NULL DEFAULT 'active',
    submit_key  TEXT     NOT NULL DEFAULT '',
    started_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stopped_at  DATETIME,
    PRIMARY KEY (agent_id, session_id)
);`

const createVersionTableSQL = `
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY);`

// schemaMigrations is the ordered list of schema migrations. Each entry is
// applied exactly once, tracked by version in schema_version.
// SQL files under migrations/ are reference copies — not executed automatically.
var schemaMigrations = []struct {
	version int
	sql     string
}{
	{1, `ALTER TABLE pty_sessions ADD COLUMN submit_key TEXT NOT NULL DEFAULT ''`},
}

type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(createTableSQL); err != nil {
		db.Close()
		return nil, err
	}
	if err := applyMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store migrations: %w", err)
	}
	return &Store{db: db}, nil
}

// applyMigrations creates the schema_version table if needed and runs any
// migrations whose version exceeds the current maximum.
// On a brand-new database, all migrations are stamped as already applied
// because createTableSQL already includes every column they would add.
func applyMigrations(db *sql.DB) error {
	if _, err := db.Exec(createVersionTableSQL); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	// Fresh database: stamp all known migrations as done — createTableSQL
	// already contains every column they would add.
	if current == 0 && len(schemaMigrations) > 0 {
		latest := schemaMigrations[len(schemaMigrations)-1].version
		if _, err := db.Exec(`INSERT OR IGNORE INTO schema_version (version) VALUES (?)`, latest); err != nil {
			return fmt.Errorf("stamp initial schema version: %w", err)
		}
		return nil
	}

	for _, m := range schemaMigrations {
		if m.version <= current {
			continue
		}
		if _, err := db.Exec(m.sql); err != nil {
			return fmt.Errorf("migration v%d: %w", m.version, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_version (version) VALUES (?)`, m.version); err != nil {
			return fmt.Errorf("record migration v%d: %w", m.version, err)
		}
	}
	return nil
}

func (s *Store) Insert(info *PTYTerminalInfo, submitKey string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO pty_sessions (agent_id, session_id, pid, status, submit_key) VALUES (?, ?, ?, ?, ?)`,
		info.AgentID, info.SessionID, info.PID, string(info.Status), submitKey,
	)
	return err
}

func (s *Store) UpdateStatus(agentID, sessionID string, status Status) error {
	var stoppedAt *time.Time
	if status != StatusActive {
		now := time.Now().UTC()
		stoppedAt = &now
	}
	_, err := s.db.Exec(
		`UPDATE pty_sessions SET status = ?, stopped_at = ? WHERE agent_id = ? AND session_id = ?`,
		string(status), stoppedAt, agentID, sessionID,
	)
	return err
}

func (s *Store) List() ([]*PTYTerminalInfo, error) {
	rows, err := s.db.Query(`SELECT agent_id, session_id, pid, status FROM pty_sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var infos []*PTYTerminalInfo
	for rows.Next() {
		info := &PTYTerminalInfo{}
		var status string
		if err := rows.Scan(&info.AgentID, &info.SessionID, &info.PID, &status); err != nil {
			return nil, err
		}
		info.Status = Status(status)
		infos = append(infos, info)
	}
	return infos, rows.Err()
}

// PTYSessionRecord is a full row from pty_sessions including timestamps.
type PTYSessionRecord struct {
	AgentID   string
	SessionID string
	PID       int
	Status    Status
	SubmitKey string
	StartedAt time.Time
	StoppedAt *time.Time
}

func (s *Store) GetBySessionOnly(sessionID string) (*PTYSessionRecord, error) {
	row := s.db.QueryRow(
		`SELECT agent_id, session_id, pid, status, submit_key, started_at, stopped_at FROM pty_sessions WHERE session_id = ? LIMIT 1`,
		sessionID,
	)
	return scanRecord(row.Scan)
}

func (s *Store) GetBySession(agentID, sessionID string) (*PTYSessionRecord, error) {
	row := s.db.QueryRow(
		`SELECT agent_id, session_id, pid, status, submit_key, started_at, stopped_at FROM pty_sessions WHERE agent_id = ? AND session_id = ?`,
		agentID, sessionID,
	)
	return scanRecord(row.Scan)
}

func (s *Store) ListByAgent(agentID string) ([]*PTYSessionRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if agentID == "" {
		rows, err = s.db.Query(
			`SELECT agent_id, session_id, pid, status, submit_key, started_at, stopped_at FROM pty_sessions ORDER BY started_at DESC`,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT agent_id, session_id, pid, status, submit_key, started_at, stopped_at FROM pty_sessions WHERE agent_id = ? ORDER BY started_at DESC`,
			agentID,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*PTYSessionRecord
	for rows.Next() {
		rec, err := scanRecord(rows.Scan)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

// MarkAllActiveCrashed marks every session still recorded as active as crashed.
// Called at daemon startup to invalidate sessions that survived a crash/kill.
func (s *Store) MarkAllActiveCrashed() error {
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE pty_sessions SET status = ?, stopped_at = ? WHERE status = ?`,
		string(StatusCrashed), now, string(StatusActive),
	)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

func scanRecord(scan func(...any) error) (*PTYSessionRecord, error) {
	var (
		rec       PTYSessionRecord
		status    string
		startedAt string
		stoppedAt sql.NullString
	)
	if err := scan(&rec.AgentID, &rec.SessionID, &rec.PID, &status, &rec.SubmitKey, &startedAt, &stoppedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	rec.Status = Status(status)
	if t, err := parseTime(startedAt); err == nil {
		rec.StartedAt = t
	}
	if stoppedAt.Valid {
		if t, err := parseTime(stoppedAt.String); err == nil {
			rec.StoppedAt = &t
		}
	}
	return &rec, nil
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time: %q", s)
}
