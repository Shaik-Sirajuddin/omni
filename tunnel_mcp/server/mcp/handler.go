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

const serverInstructions = `You are an agent connected to the tunnel-mcp messaging system.

Authentication headers (set by your runtime — do not override):
  X-SENDER-ID:       your agent id
  X-SENDER-TYPE:     omni_agent
  X-AGENT-WORKSPACE: your workspace directory

Tool discipline by request_type:
  query   → you MUST call query_result (or query_result_batch) with the original message_id and your answer.
  execute → you MUST call query_result with the message_id after completing the task; include a summary of changes.
  instant → no reply tool required; acknowledgement is optional.

Never use send_message to reply to a received message. Always use query_result / query_result_batch.

Retry behaviour: if you receive a message with a mandatory tool call and do not invoke it, the engine will inject a recall prompt up to 3 times before marking the message failed.`

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
	if sender.ID == "" {
		logger.Warn("mcp anonymous connection — X-SENDER-ID missing; tool calls requiring auth will fail", "sender_type", r.Header.Get("X-SENDER-TYPE"), "path", r.URL.Path)
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
		mcpserver.WithPromptCapabilities(true),
		mcpserver.WithInstructions(serverInstructions),
	)
	h.registerMCPTools(s)
	h.registerMCPPrompts(s)
	return s
}

func (h *Handler) MCPHandler() http.Handler {
	logger.Info("mcp streamable handler initializing", "service", "tunnel-mcp", "version", h.serviceVersion)
	return mcpserver.NewStreamableHTTPServer(
		h.buildMCPServer(),
		mcpserver.WithHTTPContextFunc(senderContextFromRequest),
	)
}


func promptText(text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{
		Messages: []mcp.PromptMessage{{
			Role:    mcp.RoleUser,
			Content: mcp.TextContent{Type: "text", Text: text},
		}},
	}
}

func (h *Handler) registerMCPPrompts(s *mcpserver.MCPServer) {
	s.AddPrompt(mcp.NewPrompt("how-to-send-message",
		mcp.WithPromptDescription("Explains how to send a message to another agent."),
		mcp.WithArgument("to_name", mcp.RequiredArgument(), mcp.ArgumentDescription("Name of the target agent.")),
		mcp.WithArgument("to_type", mcp.ArgumentDescription("Target type (default: omni_agent).")),
		mcp.WithArgument("request_type", mcp.ArgumentDescription("Request type: query, instant, or execute.")),
	), func(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		toName := req.Params.Arguments["to_name"]
		reqType := req.Params.Arguments["request_type"]
		if reqType == "" {
			reqType = "instant"
		}
		return promptText(fmt.Sprintf(
			"To send a message to agent %q:\n"+
				"  tool: send_message\n"+
				"  to_type: omni_agent\n"+
				"  to_name: %q  (or use to_id if you know the agent UUID)\n"+
				"  to_workspace: <workspace directory of the target agent, if known>\n"+
				"  prompt: <your message text>\n"+
				"  request_type: %q\n\n"+
				"request_type values:\n"+
				"  query   — you expect a response; the recipient must call query_result.\n"+
				"  execute — you are delegating a task; the recipient must call query_result when done.\n"+
				"  instant — fire-and-forget; no reply required.",
			toName, toName, reqType,
		)), nil
	})

	s.AddPrompt(mcp.NewPrompt("how-to-respond-query",
		mcp.WithPromptDescription("Explains how to respond to a query message."),
		mcp.WithArgument("message_id", mcp.RequiredArgument(), mcp.ArgumentDescription("The message_id of the query to respond to.")),
	), func(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		msgID := req.Params.Arguments["message_id"]
		return promptText(fmt.Sprintf(
			"To respond to query message %q:\n"+
				"  tool: query_result\n"+
				"  message_id: %q\n"+
				"  response: <your answer text>\n\n"+
				"For multiple queries at once use query_result_batch with a results array.\n"+
				"Do NOT use send_message to reply — only query_result / query_result_batch close the query loop.",
			msgID, msgID,
		)), nil
	})

	s.AddPrompt(mcp.NewPrompt("how-to-respond-execute",
		mcp.WithPromptDescription("Explains how to report task completion for an execute message."),
		mcp.WithArgument("message_id", mcp.RequiredArgument(), mcp.ArgumentDescription("The message_id of the execute request.")),
	), func(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		msgID := req.Params.Arguments["message_id"]
		return promptText(fmt.Sprintf(
			"To report completion of execute message %q:\n"+
				"  tool: query_result\n"+
				"  message_id: %q\n"+
				"  response: <summary of what was done, file paths changed, outcome>\n\n"+
				"Include paths of any files created or modified in the response so the requester can verify.\n"+
				"Do NOT use send_message to reply — only query_result closes the execute loop.",
			msgID, msgID,
		)), nil
	})

	s.AddPrompt(mcp.NewPrompt("how-to-check-status",
		mcp.WithPromptDescription("Explains how to check, interrupt, or resume an agent."),
		mcp.WithArgument("agent_id", mcp.RequiredArgument(), mcp.ArgumentDescription("The UUID of the agent to inspect.")),
	), func(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		agentID := req.Params.Arguments["agent_id"]
		return promptText(fmt.Sprintf(
			"Agent control for agent_id %q:\n\n"+
				"  check_status   — fetch current delivery status (idle, busy, interrupted).\n"+
				"    tool: check_status, agent_id: %q\n\n"+
				"  agent_interrupt — pause message delivery to the agent.\n"+
				"    tool: agent_interrupt, agent_id: %q\n\n"+
				"  agent_resume   — resume delivery after an interrupt.\n"+
				"    tool: agent_resume, agent_id: %q",
			agentID, agentID, agentID, agentID,
		)), nil
	})
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
