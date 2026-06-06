package server

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) handleMCPHealth(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultJSON(healthResponse{
		Status:    "ok",
		Service:   "tunnel-mcp",
		Version:   serviceVersion,
		Transport: "mcp",
	})
}
