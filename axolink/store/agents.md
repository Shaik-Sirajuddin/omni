# Store Package Map

primary_dir: `store/`

## Interfaces

- `interface.go`
  - `Store`

## Data Struct Files

- `store.go`
  - `NopStore`

- `db/mysql/store.go`
  - `Store`

## Related Packages

- `message/message.go`
  - `Message` struct: `From string`, `FromSpec Spec`, `To`, `ToSpec Spec`, `Workspace`, `Status`, ...
  - `type Spec string` — `SpecOmni = "omni_agent"` (sole value; agent/mcp removed)

- `message/store.go`
  - message persistence and query implementation
  - `MessageStore`: InsertMessage, UpdateMessage, InsertMessagesGroup, GetMessage, GetMessages,
    GetConversationMessages, GetPendingAgents, GetWorkspaceForAgent, RawQuery, RawExec

- `session/session.go`
  - code session lookup helpers

- `broadcast/registry_store.go`
  - MCP callback registry persistence

- `agents/agents.go`
  - type aliases over omni `AgentStore`; `GetStore()` factory
  - aliases: `AgentStore`, `AgentData`, `ListAgentParams`, `ListAgentResponse`, `CodeSession`, `Settings`
