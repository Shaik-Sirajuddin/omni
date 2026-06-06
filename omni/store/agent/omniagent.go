package store

import (
	"github.com/Shaik-Sirajuddin/memory/omniagent"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

type ListAgentParams struct {
	Workspace sandbox.WorkspaceDir
}
type ListAgentResponse struct {
	Agents []*omniagent.Data
}

type AgentStore interface {
	// save creates an agent when doesn't exist , else updates data
	// Save doesnt' hande array fields
	Save(agent *omniagent.Data) error
	Create(agent *omniagent.Data) error
	// retuns all agents
	ListAgents(ListAgentParams) ListAgentResponse
	// Delete agent deletes agent from index , the data is retained
	DeleteAgent(ID string) error
	// return omniagent , array are omitted from fetch
	GetAgent(ID string) (agent *omniagent.Data, err error)

	GetActiveSession(ID string) (session *omniagent.CodeSession, err error)

	UpdateActiveSession(ID string, session *omniagent.CodeSession) error

	CreateSession(ID string, session *omniagent.CodeSession) error

	GetSettings(ID string) (*omniagent.Settings, error)
	UpdateSettings(Id string, settings *omniagent.Settings) error
}
