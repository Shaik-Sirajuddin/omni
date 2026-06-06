package engine

import "sync"

// MCPClientEntry describes a registered MCP server that the engine can callback into.
type MCPClientEntry struct {
	ServerID         string
	CallbackToolName string
}

// MCPClientRegistry is a thread-safe map of serverID → MCPClientEntry.
// The engine maintains this to route agent-completion callbacks back to the
// originating MCP caller.
type MCPClientRegistry struct {
	mu      sync.RWMutex
	clients map[string]*MCPClientEntry
}

func newMCPClientRegistry() *MCPClientRegistry {
	return &MCPClientRegistry{clients: make(map[string]*MCPClientEntry)}
}

func (r *MCPClientRegistry) Register(serverID, callbackToolName string) {
	logger.Debug("register mcp client", "server_id", serverID, "callback_tool", callbackToolName)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients[serverID] = &MCPClientEntry{ServerID: serverID, CallbackToolName: callbackToolName}
}

func (r *MCPClientRegistry) Get(serverID string) (*MCPClientEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.clients[serverID]
	return entry, ok
}

func (r *MCPClientRegistry) Unregister(serverID string) {
	logger.Debug("unregister mcp client", "server_id", serverID)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.clients, serverID)
}
