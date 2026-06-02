package agentpool

// PoolEntry is a pre-warmed agent session returned by the daemon on Get.
type PoolEntry struct {
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	Provider  string `json:"provider"`
	Workspace string `json:"workspace"`
}

// ProviderPoolConfig registers a (provider, workspace) pair with a minimum
// queue depth and an internal concurrency cap for CreateAgent calls.
type ProviderPoolConfig struct {
	Provider    string `json:"provider"`
	Workspace   string `json:"workspace"`
	WorkspaceMin int   `json:"workspace_min"`
	// MaxParallel is the internal throttle for concurrent CreateAgent calls
	// per (provider, workspace) key. Defaults to 5. Callers are never capped.
	MaxParallel int `json:"max_parallel,omitempty"`
}

// OpCode identifies the operation in a pool request.
type OpCode string

const (
	OpGet            OpCode = "get"
	OpRegisterConfig OpCode = "register_config"
)

// Request is the wire type for daemon requests over the unix socket.
type Request struct {
	Op        OpCode              `json:"op"`
	Provider  string              `json:"provider,omitempty"`
	Workspace string              `json:"workspace,omitempty"`
	Config    *ProviderPoolConfig `json:"config,omitempty"`
}

// Response is the wire type for daemon responses over the unix socket.
type Response struct {
	OK    bool       `json:"ok"`
	Entry *PoolEntry `json:"entry,omitempty"`
	Error string     `json:"error,omitempty"`
}
