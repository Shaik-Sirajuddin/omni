package engine

import (
	"context"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
)

// Default is the production Engine: a ProcessingEngine backed by msgStore
// using the real omni binary on PATH.
type Default struct {
	engine *ProcessingEngine
}

// NewDefault constructs the default engine wired to the given message store.
func NewDefault(msgStore message.MessageStore) *Default {
	return &Default{engine: New(msgStore)}
}

func (d *Default) Run(ctx context.Context) error {
	return d.engine.Run(ctx)
}

// MessageArrived forwards a message-arrived event to the processing engine.
func (d *Default) MessageArrived(ctx context.Context, from, to string) {
	d.engine.MessageArrived(ctx, from, to)
}

// AgentCallback forwards an agent callback event to the processing engine.
func (d *Default) AgentCallback(ctx context.Context, req AgentCallbackRequest, failed bool) {
	d.engine.AgentCallback(ctx, req, failed)
}

// Interrupt halts delivery to agentID until Resume is called. Called by MCP tools.
func (d *Default) Interrupt(agentID string) {
	d.engine.Interrupt(agentID)
}

// Resume clears the interrupted flag and re-triggers delivery. Called by MCP tools.
func (d *Default) Resume(ctx context.Context, agentID string) {
	d.engine.Resume(ctx, agentID)
}

// MCPClients exposes the client registry for the MCP server.
func (d *Default) MCPClients() *MCPClientRegistry {
	return d.engine.MCPClients()
}
