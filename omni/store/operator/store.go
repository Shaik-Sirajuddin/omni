package operator

import (
	"github.com/Shaik-Sirajuddin/omni/omniagent"
	"github.com/Shaik-Sirajuddin/omni/operator"
	sandbox "github.com/Shaik-Sirajuddin/omni/sandbox/provider"
)

// OperatorStore handles persistence for the operator layer.
// It owns the workspaces table and reads the agents table (owned by omniagent).
type OperatorStore interface {
	CreateWorkspace(ws *operator.TeamInfo) error
	GetWorkspace(id string) (*operator.TeamInfo, error)
	WorkspaceByDir(dir sandbox.WorkspaceDir) (*operator.TeamInfo, error)
	ListWorkspaces() ([]*operator.TeamInfo, error)
	DeleteWorkspace(id string) error

	// Agent operations — read/write omniagent's agents table via the shared DB.
	CreateAgent(*omniagent.AgentInfo) error
	GetAgent(id string) (*omniagent.AgentInfo, error)
	ListAgentsByDir(dir sandbox.WorkspaceDir) ([]*omniagent.AgentInfo, error)
	DeleteAgent(id string) error
}
