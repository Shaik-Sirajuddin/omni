package mcpapi

import (
	"context"
	"encoding/json"

	"github.com/Shaik-Sirajuddin/memory/mcp/server/proxy"
	"github.com/Shaik-Sirajuddin/memory/mcp/server/service"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

type StdioHandler struct {
	proxy          *proxy.ProxyServer
	serviceVersion string
	sender         SenderInfo
}

type StdioOption func(*StdioHandler)

func WithStdioServiceVersion(version string) StdioOption {
	return func(h *StdioHandler) { h.serviceVersion = version }
}

func WithStdioSenderInfo(sender SenderInfo) StdioOption {
	return func(h *StdioHandler) { h.sender = sender }
}

func NewStdioHandler(ps *proxy.ProxyServer, opts ...StdioOption) *StdioHandler {
	h := &StdioHandler{proxy: ps, serviceVersion: "0.0.2"}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *StdioHandler) StdioServer() *mcpserver.StdioServer {
	logger.Info("mcp stdio handler initializing", "service", "tunnel-mcp", "version", h.serviceVersion)
	mcpSrv := mcpserver.NewMCPServer("tunnel-mcp", h.serviceVersion, mcpserver.WithToolCapabilities(true))
	h.registerTools(mcpSrv)
	s := mcpserver.NewStdioServer(mcpSrv)
	sender := h.sender
	s.SetContextFunc(func(ctx context.Context) context.Context {
		kind, _ := service.ParseBySpec(sender.Kind)
		logger.Debug("mcp stdio sender context injected", "sender_id", sender.ID, "sender_type", sender.Kind, "workspace", sender.Workspace)
		return context.WithValue(ctx, senderContextKey{}, service.SenderSpec{
			ID:        sender.ID,
			Kind:      kind,
			Workspace: sender.Workspace,
		})
	})
	return s
}

func (h *StdioHandler) registerTools(s *mcpserver.MCPServer) {
	s.AddTool(mcp.NewTool("health",
		mcp.WithDescription("Check tunnel MCP health."),
	), h.handleHealth)

	s.AddTool(mcp.NewTool("send_message",
		mcp.WithDescription("Store one message and notify the target agent."),
		mcp.WithString("to_type", mcp.Required(), mcp.Description("Target type: omni_agent.")),
		mcp.WithString("to_id", mcp.Description("Target id.")),
		mcp.WithString("to_name", mcp.Description("Target name.")),
		mcp.WithString("to_workspace", mcp.Description("Target workspace for name lookup.")),
		mcp.WithString("workspace", mcp.Description("Target agent workspace.")),
		mcp.WithString("prompt", mcp.Required(), mcp.Description("Prompt to send.")),
		mcp.WithString("refs", mcp.Description("Optional JSON refs object.")),
		mcp.WithString("request_type", mcp.Description("Request type: query, instant, execute.")),
	), h.handleSendMessage)

	s.AddTool(mcp.NewTool("send_group_message",
		mcp.WithDescription("Store a group of messages and notify each target."),
		mcp.WithString("messages_json", mcp.Required(), mcp.Description("JSON array of message payloads.")),
	), h.handleSendGroupMessage)

	s.AddTool(mcp.NewTool("get_message",
		mcp.WithDescription("Fetch a message by id."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Message id.")),
	), h.handleGetMessage)

	s.AddTool(mcp.NewTool("list_messages",
		mcp.WithDescription("List messages by group or conversation."),
		mcp.WithString("id", mcp.Description("Optional message id.")),
		mcp.WithString("ids_json", mcp.Description("Optional JSON array of message ids.")),
		mcp.WithString("group_id", mcp.Description("Optional group id.")),
		mcp.WithString("from", mcp.Description("Optional sender id.")),
		mcp.WithString("to", mcp.Description("Conversation target id.")),
		mcp.WithNumber("offset", mcp.Description("Pagination offset.")),
		mcp.WithNumber("limit", mcp.Description("Pagination limit.")),
	), h.handleListMessages)

	s.AddTool(mcp.NewTool("query_result",
		mcp.WithDescription("Send a response for one query message."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("Original query message id.")),
		mcp.WithString("response", mcp.Required(), mcp.Description("Response text.")),
	), h.handleQueryResult)

	s.AddTool(mcp.NewTool("query_result_batch",
		mcp.WithDescription("Send responses for multiple query messages."),
		mcp.WithArray("results",
			mcp.Required(),
			mcp.Description("Array of query result objects."),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message_id": map[string]any{"type": "string"},
					"response":   map[string]any{"type": "string"},
				},
				"required": []string{"message_id", "response"},
			}),
		),
	), h.handleQueryResultBatch)

	s.AddTool(mcp.NewTool("list_agents",
		mcp.WithDescription("List agents in the sender workspace."),
	), h.handleListAgents)

	s.AddTool(mcp.NewTool("list_teams",
		mcp.WithDescription("List known teams/workspaces."),
	), h.handleListTeams)

	s.AddTool(mcp.NewTool("agent_interrupt",
		mcp.WithDescription("Interrupt agent delivery."),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent id.")),
	), h.handleAgentInterrupt)

	s.AddTool(mcp.NewTool("agent_resume",
		mcp.WithDescription("Resume agent delivery."),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent id.")),
	), h.handleAgentResume)

	s.AddTool(mcp.NewTool("check_status",
		mcp.WithDescription("Fetch agent status."),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent id.")),
	), h.handleCheckStatus)
}

func (h *StdioHandler) handleHealth(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return resultJSON(service.HealthResponse{Status: "ok", Service: "tunnel-mcp", Version: h.serviceVersion, Transport: "stdio"}, nil)
}

func (h *StdioHandler) handleSendMessage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	payload, err := payloadFromToolRequest(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	resp, err := h.proxy.SendMessage(ctx, sender, payload)
	return resultJSON(resp, err)
}

func (h *StdioHandler) handleSendGroupMessage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	messagesJSON, err := req.RequireString("messages_json")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var payloads []service.PayloadMessage
	if err := json.Unmarshal([]byte(messagesJSON), &payloads); err != nil {
		return mcp.NewToolResultError("messages_json must be a JSON array"), nil
	}
	resp, err := h.proxy.SendGroupMessage(ctx, sender, payloads)
	return resultJSON(resp, err)
}

func (h *StdioHandler) handleGetMessage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	resp, err := h.proxy.GetMessage(ctx, id)
	return resultJSON(resp, err)
}

func (h *StdioHandler) handleListMessages(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	ids, err := idsFromToolRequest(req)
	if err != nil {
		return mcp.NewToolResultError("ids_json must be a JSON array of strings"), nil
	}
	listReq := service.ListMessagesRequest{
		ID:      req.GetString("id", ""),
		IDs:     ids,
		GroupID: req.GetString("group_id", ""),
		From:    req.GetString("from", ""),
		To:      req.GetString("to", ""),
		Page: message.Page{
			Offset: req.GetInt("offset", 0),
			Limit:  req.GetInt("limit", 50),
		},
	}
	msgs, err := h.proxy.ListMessages(ctx, sender, listReq)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return resultJSON(listMessagesToolResponse{Messages: nonNilMessagePtrs(msgs), Count: len(msgs)}, nil)
}

func (h *StdioHandler) handleQueryResult(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	messageID, err := req.RequireString("message_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	response, err := req.RequireString("response")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	resp, err := h.proxy.QueryResult(ctx, sender, service.QueryResultItem{MessageID: messageID, Response: response})
	return resultJSON(resp, err)
}

func (h *StdioHandler) handleQueryResultBatch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	items, err := queryResultItemsFromToolRequest(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	resp, err := h.proxy.QueryResultBatch(ctx, sender, items)
	return resultJSON(resp, err)
}

func (h *StdioHandler) handleListAgents(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	resp, err := h.proxy.ListAgents(ctx, sender)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return resultJSON(resp, nil)
}

func (h *StdioHandler) handleListTeams(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	resp, err := h.proxy.ListTeams(ctx, sender)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return resultJSON(resp, nil)
}

func (h *StdioHandler) handleAgentInterrupt(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	agentID, err := req.RequireString("agent_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	err = h.proxy.InterruptAgent(ctx, sender, agentID)
	return resultJSON(map[string]string{"status": "interrupted"}, err)
}

func (h *StdioHandler) handleAgentResume(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	agentID, err := req.RequireString("agent_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	err = h.proxy.ResumeAgent(ctx, sender, agentID)
	return resultJSON(map[string]string{"status": "resumed"}, err)
}

func (h *StdioHandler) handleCheckStatus(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	agentID, err := req.RequireString("agent_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	resp, err := h.proxy.CheckStatus(ctx, sender, agentID)
	return resultJSON(resp, err)
}

func nonNilMessagePtrs(msgs []*service.MessageResponse) []*service.MessageResponse {
	if msgs == nil {
		return []*service.MessageResponse{}
	}
	return msgs
}

