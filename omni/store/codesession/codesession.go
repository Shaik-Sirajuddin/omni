package codesession

import (
	"database/sql"

	"github.com/Shaik-Sirajuddin/omni/omniagent"
)

// CodeSessionStore provides persistent storage for code sessions.
type CodeSessionStore interface {
	GetSession(agentID string) (*omniagent.CodeSession, error)
	// GetSessionByID looks up a session by its own ID (not agent ID).
	// Returns the owning agentID alongside the session.
	GetSessionByID(sessionID string) (agentID string, session *omniagent.CodeSession, err error)
	CreateSession(agentID string, session *omniagent.CodeSession) error
	UpdateSession(agentID string, session *omniagent.CodeSession) error
	ListSessions(agentID string, filter *omniagent.CodeSession) ([]*omniagent.CodeSession, error)
}

type sqlCodeSessionStore struct {
	db *sql.DB
}
