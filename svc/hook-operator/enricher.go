package hookoperator

import (
	"encoding/json"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
	"github.com/Shaik-Sirajuddin/memory/omniagent"
)

// SessionLookup resolves a code session by its own ID (not agent ID).
// codesession.CodeSessionStore satisfies this interface.
type SessionLookup interface {
	GetSessionByID(sessionID string) (agentID string, session *omniagent.CodeSession, err error)
}

// AgentLookup resolves agent info (name, workspace) by agent ID.
// operator.OperatorStore satisfies this interface.
type AgentLookup interface {
	GetAgent(id string) (*omniagent.AgentInfo, error)
}

type enricher struct {
	sessions SessionLookup
	agents   AgentLookup
}

func newEnricher(sessions SessionLookup, agents AgentLookup) *enricher {
	return &enricher{sessions: sessions, agents: agents}
}

// enrich merges an omni field into the raw hook payload body.
// It extracts session_id from body, resolves the agentID from the session store,
// then fetches agent info. On any failure it injects a best-effort omni field
// so hook execution is never blocked.
func (e *enricher) enrich(body []byte) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}

	ctx := e.buildContext(raw)

	omniBytes, err := json.Marshal(ctx)
	if err != nil {
		return body
	}
	raw["omni"] = omniBytes

	enriched, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return enriched
}

func (e *enricher) buildContext(raw map[string]json.RawMessage) hooks.OmniContext {
	ctx := hooks.OmniContext{}

	// Extract session_id from the raw payload JSON.
	var sessionID string
	if v, ok := raw["session_id"]; ok {
		_ = json.Unmarshal(v, &sessionID)
	}

	if sessionID == "" || e.sessions == nil {
		return ctx
	}

	agentID, session, err := e.sessions.GetSessionByID(sessionID)
	if err != nil || session == nil || agentID == "" {
		return ctx
	}

	ctx.Agent.ID = agentID
	ctx.Agent.Status = session.Status
	if session.Model != nil {
		ctx.Agent.Model = session.Model.Model
	}

	if e.agents != nil && agentID != "" {
		if info, err := e.agents.GetAgent(agentID); err == nil && info != nil {
			ctx.Agent.Name = info.Name
			ctx.Workspace = info.WorkspaceDir
		}
	}

	return ctx
}
