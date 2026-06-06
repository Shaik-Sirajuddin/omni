package session

import (
	"github.com/Shaik-Sirajuddin/memory/omniagent"
	codesession "github.com/Shaik-Sirajuddin/memory/store/codesession"
)

// CodeSession is the same type as omniagent.CodeSession — aliased so callers
// only need to import store/session.
type CodeSession = omniagent.CodeSession

// GetSession returns the active code session for the given agent using the omni read-only store.
func GetSession(agentID string) (*CodeSession, error) {
	logger.Debug("get code session", "agent_id", agentID)

	store, err := codesession.GetReadOnlyCodeSessionStore()
	if err != nil {
		logger.Error("get read-only code session store failed", "err", err)
		return nil, err
	}

	session, err := store.GetSession(agentID)
	if err != nil {
		logger.Error("get session failed", "err", err, "agent_id", agentID)
		return nil, err
	}

	logger.Debug("code session retrieved", "agent_id", agentID, "session_id", session.Id)
	return session, nil
}

// ListSessions returns all sessions for the given agent using the omni read-only store.
func ListSessions(agentID string) ([]*CodeSession, error) {
	logger.Debug("list code sessions", "agent_id", agentID)

	store, err := codesession.GetReadOnlyCodeSessionStore()
	if err != nil {
		logger.Error("get read-only code session store failed", "err", err)
		return nil, err
	}

	sessions, err := store.ListSessions(agentID, nil)
	if err != nil {
		logger.Error("list sessions failed", "err", err, "agent_id", agentID)
		return nil, err
	}

	logger.Debug("code sessions retrieved", "agent_id", agentID, "count", len(sessions))
	return sessions, nil
}
