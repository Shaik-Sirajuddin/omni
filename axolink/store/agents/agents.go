package agents

import (
	"github.com/Shaik-Sirajuddin/memory/omniagent"
	omnistore "github.com/Shaik-Sirajuddin/memory/store/agent"
)

// Type aliases — same underlying types as omni, no duplication, no conversion needed.
type (
	AgentStore        = omnistore.AgentStore
	AgentData         = omniagent.Data
	AgentInfo         = omniagent.AgentInfo
	ListAgentParams   = omnistore.ListAgentParams
	ListAgentResponse = omnistore.ListAgentResponse
	CodeSession       = omniagent.CodeSession
	Settings          = omniagent.Settings
)

// GetStore returns the singleton AgentStore backed by the omni shared database.
func GetStore() (AgentStore, error) {
	return omnistore.GetOmniAgentStore()
}
