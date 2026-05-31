package mcpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/mcp/server/service"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/agents"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/Shaik-Sirajuddin/memory/operator"
	pkglog "github.com/Shaik-Sirajuddin/memory/pkg/log"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

var logger = pkglog.NewLogger("component", "mcp-handler")

type SenderInfo struct {
	ID        string
	Kind      string
	Workspace string
}

type Handler struct {
	service        *service.Service
	serviceVersion string
}

type listAgentsToolResponse struct {
	Agents []*agents.AgentInfo `json:"agents"`
	Count  int                 `json:"count"`
}

type listMessagesToolResponse struct {
	Messages []*message.Message `json:"messages"`
	Count    int                `json:"count"`
}

type Option func(*Handler)

func WithServiceVersion(version string) Option {
	return func(h *Handler) { h.serviceVersion = version }
}


func New(svc *service.Service, opts ...Option) *Handler {
	h := &Handler{service: svc, serviceVersion: "0.0.2"}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

type senderContextKey struct{}

func senderContextFromRequest(ctx context.Context, r *http.Request) context.Context {
	sender := service.SenderSpec{
		ID:        strings.TrimSpace(r.Header.Get("X-SENDER-ID")),
		Workspace: strings.TrimSpace(r.Header.Get("X-AGENT-WORKSPACE")),
	}
	kind, err := service.ParseBySpec(r.Header.Get("X-SENDER-TYPE"))
	if err == nil {
		sender.Kind = kind
	} else {
		logger.Warn("mcp sender type parse failed", "err", err, "sender_id", sender.ID, "sender_type", r.Header.Get("X-SENDER-TYPE"), "workspace", sender.Workspace, "path", r.URL.Path)
	}
	logger.Debug("mcp sender context extracted", "sender_id", sender.ID, "sender_type", sender.Kind, "workspace", sender.Workspace, "path", r.URL.Path)
	return context.WithValue(ctx, senderContextKey{}, sender)
}

func senderFromContext(ctx context.Context) (service.SenderSpec, error) {
	sender, ok := ctx.Value(senderContextKey{}).(service.SenderSpec)
	if !ok {
		err := fmt.Errorf("sender context is missing")
		logger.Error("mcp sender context missing", "err", err)
		return service.SenderSpec{}, err
	}
	if sender.ID == "" {
		err := fmt.Errorf("X-SENDER-ID is required")
		logger.Error("mcp sender validation failed", "err", err, "sender_type", sender.Kind, "workspace", sender.Workspace)
		return service.SenderSpec{}, err
	}
	if sender.Kind == "" {
		err := fmt.Errorf("X-SENDER-TYPE must be omni_agent")
		logger.Error("mcp sender validation failed", "err", err, "sender_id", sender.ID, "workspace", sender.Workspace)
		return service.SenderSpec{}, err
	}
	return sender, nil
}

func (h *Handler) buildMCPServer() *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(
		"tunnel-mcp",
		h.serviceVersion,
		mcpserver.WithToolCapabilities(true),
	)
	h.registerMCPTools(s)
	return s
}

func (h *Handler) MCPHandler() http.Handler {
	logger.Info("mcp streamable handler initializing", "service", "tunnel-mcp", "version", h.serviceVersion)
	return mcpserver.NewStreamableHTTPServer(
		h.buildMCPServer(),
		mcpserver.WithHTTPContextFunc(senderContextFromRequest),
	)
}


func (h *Handler) registerMCPTools(mcpServer *mcpserver.MCPServer) {
	logger.Debug("mcp tools registering")
	mcpServer.AddTool(mcp.NewTool("health",
		mcp.WithDescription("Check tunnel MCP health."),
	), h.handleMCPHealth)

	mcpServer.AddTool(mcp.NewTool("send_message",
		mcp.WithDescription("Store one message and notify the target agent."),
		mcp.WithString("to_type", mcp.Required(), mcp.Description("Target type: omni_agent.")),
		mcp.WithString("to_id", mcp.Description("Target id.")),
		mcp.WithString("to_name", mcp.Description("Target name.")),
		mcp.WithString("to_workspace", mcp.Description("Target workspace for name lookup.")),
		mcp.WithString("workspace", mcp.Description("Target agent workspace.")),
		mcp.WithString("prompt", mcp.Required(), mcp.Description("Prompt to send.")),
		mcp.WithString("refs", mcp.Description("Optional JSON refs object.")),
		mcp.WithString("request_type", mcp.Description("Request type: query, instant, execute.")),
	), h.handleMCPSendMessage)

	mcpServer.AddTool(mcp.NewTool("send_group_message",
		mcp.WithDescription("Store a group of messages and notify each target."),
		mcp.WithString("messages_json", mcp.Required(), mcp.Description("JSON array of message payloads.")),
	), h.handleMCPSendGroupMessage)

	mcpServer.AddTool(mcp.NewTool("get_message",
		mcp.WithDescription("Fetch a message by id."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Message id.")),
	), h.handleMCPGetMessage)

	mcpServer.AddTool(mcp.NewTool("list_messages",
		mcp.WithDescription("List messages by group or conversation."),
		mcp.WithString("id", mcp.Description("Optional message id.")),
		mcp.WithString("ids_json", mcp.Description("Optional JSON array of message ids.")),
		mcp.WithString("group_id", mcp.Description("Optional group id.")),
		mcp.WithString("from", mcp.Description("Optional sender id.")),
		mcp.WithString("to", mcp.Description("Conversation target id.")),
		mcp.WithNumber("offset", mcp.Description("Pagination offset.")),
		mcp.WithNumber("limit", mcp.Description("Pagination limit.")),
	), h.handleMCPListMessages)

	mcpServer.AddTool(mcp.NewTool("query_result",
		mcp.WithDescription("Send a response for one query message."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("Original query message id.")),
		mcp.WithString("response", mcp.Required(), mcp.Description("Response text.")),
	), h.handleMCPQueryResult)

	mcpServer.AddTool(mcp.NewTool("query_result_batch",
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
	), h.handleMCPQueryResultBatch)

	mcpServer.AddTool(mcp.NewTool("list_agents",
		mcp.WithDescription("List agents in the sender workspace."),
	), h.handleMCPListAgents)

	mcpServer.AddTool(mcp.NewTool("list_teams",
		mcp.WithDescription("List known teams/workspaces."),
	), h.handleMCPListTeams)

	mcpServer.AddTool(mcp.NewTool("agent_interrupt",
		mcp.WithDescription("Interrupt agent delivery."),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent id.")),
	), h.handleMCPAgentInterrupt)

	mcpServer.AddTool(mcp.NewTool("agent_resume",
		mcp.WithDescription("Resume agent delivery."),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent id.")),
	), h.handleMCPAgentResume)

	mcpServer.AddTool(mcp.NewTool("check_status",
		mcp.WithDescription("Fetch agent status."),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent id.")),
	), h.handleMCPCheckStatus)
	logger.Info("mcp tools registered", "count", 12)
}

func (h *Handler) handleMCPHealth(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logger.Debug("mcp tool call received", "tool", "health")
	resp := service.HealthResponse{
		Status:    "ok",
		Service:   "tunnel-mcp",
		Version:   h.serviceVersion,
		Transport: "mcp",
	}
	logger.Debug("mcp tool call succeeded", "tool", "health", "status", resp.Status)
	return resultJSON(resp, nil)
}

func (h *Handler) handleMCPSendMessage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		logger.Error("mcp tool sender validation failed", "err", err, "tool", "send_message")
		return mcp.NewToolResultError(err.Error()), nil
	}
	logger.Debug("mcp tool call received", "tool", "send_message", "sender_id", sender.ID, "sender_type", sender.Kind, "workspace", sender.Workspace)
	payload, err := payloadFromToolRequest(request)
	if err != nil {
		logger.Error("mcp tool payload validation failed", "err", err, "tool", "send_message", "sender_id", sender.ID)
		return mcp.NewToolResultError(err.Error()), nil
	}
	logger.Debug("mcp tool payload mapped", "tool", "send_message", "sender_id", sender.ID, "to_type", payload.To.Type, "to_id", payload.To.ID, "to_name", payload.To.Name, "request_type", payload.RequestType, "prompt_bytes", len(payload.Prompt), "refs_bytes", len(payload.Refs))
	resp, err := h.service.SendMessage(ctx, sender, payload)
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "send_message", "sender_id", sender.ID, "to_type", payload.To.Type, "to_id", payload.To.ID, "to_name", payload.To.Name)
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "send_message", "sender_id", sender.ID, "message_id", resp.MessageID)
	}
	return resultJSON(resp, err)
}

func (h *Handler) handleMCPSendGroupMessage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		logger.Error("mcp tool sender validation failed", "err", err, "tool", "send_group_message")
		return mcp.NewToolResultError(err.Error()), nil
	}
	logger.Debug("mcp tool call received", "tool", "send_group_message", "sender_id", sender.ID, "sender_type", sender.Kind, "workspace", sender.Workspace)
	messagesJSON, err := request.RequireString("messages_json")
	if err != nil {
		logger.Error("mcp tool payload validation failed", "err", err, "tool", "send_group_message", "sender_id", sender.ID)
		return mcp.NewToolResultError(err.Error()), nil
	}
	var payloads []service.PayloadMessage
	if err := json.Unmarshal([]byte(messagesJSON), &payloads); err != nil {
		logger.Error("mcp tool messages_json decode failed", "err", err, "tool", "send_group_message", "sender_id", sender.ID, "bytes", len(messagesJSON))
		return mcp.NewToolResultError("messages_json must be a JSON array"), nil
	}
	logger.Debug("mcp tool payload mapped", "tool", "send_group_message", "sender_id", sender.ID, "message_count", len(payloads), "payload_bytes", len(messagesJSON))
	resp, err := h.service.SendGroupMessage(ctx, sender, payloads)
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "send_group_message", "sender_id", sender.ID, "message_count", len(payloads))
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "send_group_message", "sender_id", sender.ID, "group_id", resp.GroupID, "message_count", len(resp.MessageIDs))
	}
	return resultJSON(resp, err)
}

func (h *Handler) handleMCPGetMessage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logger.Debug("mcp tool call received", "tool", "get_message")
	id, err := request.RequireString("id")
	if err != nil {
		logger.Error("mcp tool payload validation failed", "err", err, "tool", "get_message")
		return mcp.NewToolResultError(err.Error()), nil
	}
	resp, err := h.service.GetMessage(ctx, id)
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "get_message", "message_id", id)
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "get_message", "message_id", id, "status", resp.Status)
	}
	return resultJSON(resp, err)
}

func (h *Handler) handleMCPListMessages(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		logger.Error("mcp tool sender validation failed", "err", err, "tool", "list_messages")
		return mcp.NewToolResultError(err.Error()), nil
	}
	ids, err := idsFromToolRequest(request)
	if err != nil {
		logger.Error("mcp tool payload validation failed", "err", err, "tool", "list_messages", "sender_id", sender.ID)
		return mcp.NewToolResultError("ids_json must be a JSON array of strings"), nil
	}
	req := service.ListMessagesRequest{
		ID:      request.GetString("id", ""),
		IDs:     ids,
		GroupID: request.GetString("group_id", ""),
		From:    request.GetString("from", ""),
		To:      request.GetString("to", ""),
		Page: message.Page{
			Offset: request.GetInt("offset", 0),
			Limit:  request.GetInt("limit", 50),
		},
	}
	logger.Debug("mcp tool call received", "tool", "list_messages", "sender_id", sender.ID, "sender_type", sender.Kind, "workspace", sender.Workspace, "id", req.ID, "ids_count", len(req.IDs), "group_id", req.GroupID, "from", req.From, "to", req.To, "offset", req.Page.Offset, "limit", req.Page.Limit)
	resp, err := h.service.ListMessages(ctx, sender, req)
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "list_messages", "sender_id", sender.ID, "id", req.ID, "ids_count", len(req.IDs), "group_id", req.GroupID, "from", req.From, "to", req.To)
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "list_messages", "sender_id", sender.ID, "count", len(resp))
	}
	return resultJSON(listMessagesToolResponse{Messages: nonNilMessages(resp), Count: len(resp)}, err)
}

func (h *Handler) handleMCPQueryResult(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		logger.Error("mcp tool sender validation failed", "err", err, "tool", "query_result")
		return mcp.NewToolResultError(err.Error()), nil
	}
	messageID, err := request.RequireString("message_id")
	if err != nil {
		logger.Error("mcp tool payload validation failed", "err", err, "tool", "query_result", "sender_id", sender.ID)
		return mcp.NewToolResultError(err.Error()), nil
	}
	response, err := request.RequireString("response")
	if err != nil {
		logger.Error("mcp tool payload validation failed", "err", err, "tool", "query_result", "sender_id", sender.ID, "message_id", messageID)
		return mcp.NewToolResultError(err.Error()), nil
	}
	logger.Debug("mcp tool call received", "tool", "query_result", "sender_id", sender.ID, "sender_type", sender.Kind, "workspace", sender.Workspace, "message_id", messageID, "response_bytes", len(response))
	resp, err := h.service.QueryResult(ctx, sender, service.QueryResultItem{MessageID: messageID, Response: response})
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "query_result", "sender_id", sender.ID, "message_id", messageID)
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "query_result", "sender_id", sender.ID, "message_id", resp.MessageID, "responded_to", resp.RespondedTo)
	}
	return resultJSON(resp, err)
}

func (h *Handler) handleMCPQueryResultBatch(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		logger.Error("mcp tool sender validation failed", "err", err, "tool", "query_result_batch")
		return mcp.NewToolResultError(err.Error()), nil
	}
	items, err := queryResultItemsFromToolRequest(request)
	if err != nil {
		logger.Error("mcp tool payload validation failed", "err", err, "tool", "query_result_batch", "sender_id", sender.ID)
		return mcp.NewToolResultError(err.Error()), nil
	}
	logger.Debug("mcp tool call received", "tool", "query_result_batch", "sender_id", sender.ID, "sender_type", sender.Kind, "workspace", sender.Workspace, "count", len(items))
	resp, err := h.service.QueryResultBatch(ctx, sender, items)
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "query_result_batch", "sender_id", sender.ID, "count", len(items))
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "query_result_batch", "sender_id", sender.ID, "count", resp.Count, "group_id", resp.GroupID)
	}
	return resultJSON(resp, err)
}

func (h *Handler) handleMCPListAgents(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		logger.Error("mcp tool sender validation failed", "err", err, "tool", "list_agents")
		return mcp.NewToolResultError(err.Error()), nil
	}
	logger.Debug("mcp tool call received", "tool", "list_agents", "sender_id", sender.ID, "sender_type", sender.Kind, "workspace", sender.Workspace)
	resp, err := h.service.ListAgents(sender.Workspace)
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "list_agents", "sender_id", sender.ID, "workspace", sender.Workspace)
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "list_agents", "sender_id", sender.ID, "count", len(resp))
	}
	return resultJSON(listAgentsToolResponse{Agents: nonNilAgents(resp), Count: len(resp)}, err)
}

func (h *Handler) handleMCPListTeams(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		logger.Error("mcp tool sender validation failed", "err", err, "tool", "list_teams")
		return mcp.NewToolResultError(err.Error()), nil
	}
	logger.Debug("mcp tool call received", "tool", "list_teams", "sender_id", sender.ID, "sender_type", sender.Kind, "workspace", sender.Workspace)
	resp, err := h.service.ListTeams()
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "list_teams", "sender_id", sender.ID)
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "list_teams", "sender_id", sender.ID, "count", resp.Count)
	}
	if resp.Teams == nil {
		resp.Teams = []*operator.TeamInfo{}
	}
	return resultJSON(resp, err)
}

func (h *Handler) handleMCPAgentInterrupt(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logger.Debug("mcp tool call received", "tool", "agent_interrupt")
	agentID, err := request.RequireString("agent_id")
	if err != nil {
		logger.Error("mcp tool payload validation failed", "err", err, "tool", "agent_interrupt")
		return mcp.NewToolResultError(err.Error()), nil
	}
	err = h.service.InterruptAgent(agentID)
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "agent_interrupt", "agent_id", agentID)
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "agent_interrupt", "agent_id", agentID)
	}
	return resultJSON(map[string]string{"status": "interrupted"}, err)
}

func (h *Handler) handleMCPAgentResume(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logger.Debug("mcp tool call received", "tool", "agent_resume")
	agentID, err := request.RequireString("agent_id")
	if err != nil {
		logger.Error("mcp tool payload validation failed", "err", err, "tool", "agent_resume")
		return mcp.NewToolResultError(err.Error()), nil
	}
	err = h.service.ResumeAgent(ctx, agentID)
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "agent_resume", "agent_id", agentID)
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "agent_resume", "agent_id", agentID)
	}
	return resultJSON(map[string]string{"status": "resumed"}, err)
}

func (h *Handler) handleMCPCheckStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logger.Debug("mcp tool call received", "tool", "check_status")
	agentID, err := request.RequireString("agent_id")
	if err != nil {
		logger.Error("mcp tool payload validation failed", "err", err, "tool", "check_status")
		return mcp.NewToolResultError(err.Error()), nil
	}
	resp, err := h.service.CheckStatus(ctx, agentID)
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "check_status", "agent_id", agentID)
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "check_status", "agent_id", agentID, "status", resp.Status)
	}
	return resultJSON(resp, err)
}

func payloadFromToolRequest(request mcp.CallToolRequest) (service.PayloadMessage, error) {
	prompt, err := request.RequireString("prompt")
	if err != nil {
		return service.PayloadMessage{}, err
	}
	refs := json.RawMessage(request.GetString("refs", "{}"))
	return service.PayloadMessage{
		To: service.TargetSpec{
			Type:      request.GetString("to_type", ""),
			ID:        request.GetString("to_id", ""),
			Name:      request.GetString("to_name", ""),
			Workspace: request.GetString("to_workspace", ""),
		},
		Workspace:   request.GetString("workspace", ""),
		Prompt:      prompt,
		Refs:        refs,
		RequestType: request.GetString("request_type", ""),
	}, nil
}

func idsFromToolRequest(request mcp.CallToolRequest) ([]string, error) {
	idsJSON := strings.TrimSpace(request.GetString("ids_json", ""))
	if idsJSON == "" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(idsJSON), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func queryResultItemsFromToolRequest(request mcp.CallToolRequest) ([]service.QueryResultItem, error) {
	raw, ok := request.GetArguments()["results"]
	if !ok {
		return nil, fmt.Errorf("results is required")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("results must be an array")
	}
	var items []service.QueryResultItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("results must be an array of {message_id,response}")
	}
	return items, nil
}

func resultJSON[T any](payload T, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	result, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func nonNilMessages(messages []*message.Message) []*message.Message {
	if messages == nil {
		return []*message.Message{}
	}
	return messages
}

func nonNilAgents(list []*agents.AgentInfo) []*agents.AgentInfo {
	if list == nil {
		return []*agents.AgentInfo{}
	}
	return list
}
