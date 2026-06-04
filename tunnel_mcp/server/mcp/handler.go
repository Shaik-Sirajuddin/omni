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

const serverInstructions = `You are an agent connected to the axolink messaging system.

Authentication headers (set by your runtime — do not override):
  X-SENDER-ID:       your agent id
  X-SENDER-TYPE:     omni_agent
  X-AGENT-WORKSPACE: your workspace directory

Tool discipline by request_type:
  query   → you MUST call send_response (or send_response_batch) with the original message_id and your answer.
  execute → you MUST call send_response with the message_id after completing the task; include a summary of changes.
  instant → no reply tool required; acknowledgement is optional.

For mixed task batches (execute + queries), call send_response for each message_id or send_response_batch for all.

Never use send_message to reply to a received message. Always use send_response / send_response_batch.

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
		"axolink",
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
	logger.Info("mcp streamable handler initializing", "service", "axolink", "version", h.serviceVersion)
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
				"  schema: <optional JSON schema string for the expected response>\n"+
				"  request_type: %q\n\n"+
				"request_type values:\n"+
				"  query   — you expect a response; the recipient must call send_response.\n"+
				"  execute — you are delegating a task; the recipient must call send_response when done.\n"+
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
				"  tool: send_response\n"+
				"  message_id: %q\n"+
				"  response: <your answer text>\n\n"+
				"For multiple queries at once use send_response_batch with a results array.\n"+
				"Do NOT use send_message to reply — only send_response / send_response_batch close the query loop.",
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
				"  tool: send_response\n"+
				"  message_id: %q\n"+
				"  response: <summary of what was done, file paths changed, outcome>\n\n"+
				"Include paths of any files created or modified in the response so the requester can verify.\n"+
				"Do NOT use send_message to reply — only send_response closes the execute loop.",
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

func sendResponseArraySchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message_id": map[string]any{"type": "string"},
			"response":   map[string]any{"type": "string"},
		},
		"required": []string{"message_id", "response"},
	}
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
		mcp.WithString("schema", mcp.Description("Optional JSON schema for the expected response payload.")),
		mcp.WithString("refs", mcp.Description("Optional JSON refs object.")),
		mcp.WithString("request_type", mcp.Description("Request type: query, instant, execute.")),
		mcp.WithString("task_id", mcp.Description("Optional task identifier for related messages.")),
		mcp.WithString("creator_agent_id", mcp.Description("Optional creator agent id paired with task_id.")),
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

	mcpServer.AddTool(mcp.NewTool("send_response",
		mcp.WithDescription("Send a response for one query or execute message."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("Original query or execute message id.")),
		mcp.WithString("response", mcp.Required(), mcp.Description("Response text.")),
	), h.handleMCPSendResponse)

	mcpServer.AddTool(mcp.NewTool("send_response_batch",
		mcp.WithDescription("Send responses for multiple query or execute messages."),
		mcp.WithArray("results", mcp.Required(), mcp.Description("Array of response objects."), mcp.Items(sendResponseArraySchema())),
	), h.handleMCPSendResponseBatch)

	mcpServer.AddTool(mcp.NewTool("query_result",
		mcp.WithDescription("Legacy alias for send_response."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("Original query message id.")),
		mcp.WithString("response", mcp.Required(), mcp.Description("Response text.")),
	), h.handleMCPQueryResult)

	mcpServer.AddTool(mcp.NewTool("query_result_batch",
		mcp.WithDescription("Legacy alias for send_response_batch."),
		mcp.WithArray("results", mcp.Required(), mcp.Description("Array of response objects."), mcp.Items(sendResponseArraySchema())),
	), h.handleMCPQueryResultBatch)

	mcpServer.AddTool(mcp.NewTool("update_message",
		mcp.WithDescription("Legacy alias for send_response."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("Original execute message id.")),
		mcp.WithString("response", mcp.Required(), mcp.Description("Response text.")),
	), h.handleMCPUpdateMessage)

	mcpServer.AddTool(mcp.NewTool("update_messages",
		mcp.WithDescription("Legacy alias for send_response_batch."),
		mcp.WithArray("results", mcp.Required(), mcp.Description("Array of response objects."), mcp.Items(sendResponseArraySchema())),
	), h.handleMCPUpdateMessages)

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

	mcpServer.AddTool(mcp.NewTool("pause_task",
		mcp.WithDescription("Pause delivery for a specific task on an agent."),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent id.")),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task id.")),
		mcp.WithString("creator_agent_id", mcp.Required(), mcp.Description("Task creator agent id.")),
	), h.handleMCPPauseTask)

	mcpServer.AddTool(mcp.NewTool("resume_task",
		mcp.WithDescription("Resume delivery for a specific task on an agent."),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent id.")),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task id.")),
		mcp.WithString("creator_agent_id", mcp.Required(), mcp.Description("Task creator agent id.")),
	), h.handleMCPResumeTask)

	mcpServer.AddTool(mcp.NewTool("list_tasks",
		mcp.WithDescription("List all tasks (by task_id) received by an agent. Defaults to the caller's own agent_id when omitted."),
		mcp.WithString("agent_id", mcp.Description("Agent id; defaults to self.")),
	), h.handleMCPListTasks)

	mcpServer.AddTool(mcp.NewTool("get_task",
		mcp.WithDescription("Get all messages for a specific task received by an agent."),
		mcp.WithString("agent_id", mcp.Description("Agent id; defaults to self.")),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task id.")),
	), h.handleMCPGetTask)

	mcpServer.AddTool(mcp.NewTool("list_active_tasks",
		mcp.WithDescription("List currently in-flight (queued or processing) task messages for an agent. Defaults to self."),
		mcp.WithString("agent_id", mcp.Description("Agent id; defaults to self.")),
	), h.handleMCPListActiveTasks)

	logger.Info("mcp tools registered", "count", 21)
}

func (h *Handler) handleMCPHealth(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logger.Debug("mcp tool call received", "tool", "health")
	resp := service.HealthResponse{Status: "ok", Service: "tunnel-mcp", Version: h.serviceVersion, Transport: "mcp"}
	return resultJSON(resp, nil)
}

func (h *Handler) handleMCPSendMessage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return toolResultError(err), nil
	}
	payload, err := payloadFromToolRequest(request)
	if err != nil {
		return toolResultError(err), nil
	}
	resp, err := h.service.SendMessageWithMetadata(ctx, sender, payload)
	if err != nil {
		return toolResultError(err), nil
	}
	return resultJSON(resp, nil)
}

func (h *Handler) handleMCPSendGroupMessage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return toolResultError(err), nil
	}
	messagesJSON, err := request.RequireString("messages_json")
	if err != nil {
		return toolResultError(err), nil
	}
	var payloads []service.PayloadMessage
	if err := json.Unmarshal([]byte(messagesJSON), &payloads); err != nil {
		return mcp.NewToolResultError("messages_json must be a JSON array"), nil
	}
	resp, err := h.service.SendGroupMessageWithMetadata(ctx, sender, payloads)
	if err != nil {
		return toolResultError(err), nil
	}
	return resultJSON(resp, nil)
}

func (h *Handler) handleMCPGetMessage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := request.RequireString("id")
	if err != nil {
		return toolResultError(err), nil
	}
	resp, err := h.service.GetMessage(ctx, id)
	if err != nil {
		return toolResultError(err), nil
	}
	return resultJSON(resp, nil)
}

func (h *Handler) handleMCPListMessages(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return toolResultError(err), nil
	}
	ids, err := idsFromToolRequest(request)
	if err != nil {
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
	resp, err := h.service.ListMessages(ctx, sender, req)
	if err != nil {
		return toolResultError(err), nil
	}
	return resultJSON(listMessagesToolResponse{Messages: nonNilMessages(resp), Count: len(resp)}, nil)
}

func (h *Handler) handleMCPSendResponse(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return toolResultError(err), nil
	}
	messageID, err := request.RequireString("message_id")
	if err != nil {
		return toolResultError(err), nil
	}
	response, err := request.RequireString("response")
	if err != nil {
		return toolResultError(err), nil
	}
	resp, err := h.service.SendResponse(ctx, sender, service.SendResponseItem{MessageID: messageID, Response: response})
	if err != nil {
		return toolResultError(err), nil
	}
	return resultJSON(resp, nil)
}

func (h *Handler) handleMCPSendResponseBatch(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return toolResultError(err), nil
	}
	items, err := sendResponseItemsFromToolRequest(request)
	if err != nil {
		return toolResultError(err), nil
	}
	resp, err := h.service.SendResponseBatch(ctx, sender, items)
	if err != nil {
		return toolResultError(err), nil
	}
	return resultJSON(resp, nil)
}

func (h *Handler) handleMCPQueryResult(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return h.handleMCPSendResponse(ctx, request)
}

func (h *Handler) handleMCPQueryResultBatch(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return h.handleMCPSendResponseBatch(ctx, request)
}

func (h *Handler) handleMCPUpdateMessage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return h.handleMCPSendResponse(ctx, request)
}

func (h *Handler) handleMCPUpdateMessages(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return h.handleMCPSendResponseBatch(ctx, request)
}

func (h *Handler) handleMCPListAgents(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sender, err := senderFromContext(ctx)
	if err != nil {
		return toolResultError(err), nil
	}
	resp, err := h.service.ListAgents(sender.Workspace)
	if err != nil {
		return toolResultError(err), nil
	}
	return resultJSON(listAgentsToolResponse{Agents: nonNilAgents(resp), Count: len(resp)}, nil)
}

func (h *Handler) handleMCPListTeams(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	resp, err := h.service.ListTeams()
	if err != nil {
		return toolResultError(err), nil
	}
	if resp.Teams == nil {
		resp.Teams = []*operator.TeamInfo{}
	}
	return resultJSON(resp, nil)
}

func (h *Handler) handleMCPAgentInterrupt(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentID, err := request.RequireString("agent_id")
	if err != nil {
		return toolResultError(err), nil
	}
	if err := h.service.InterruptAgent(agentID); err != nil {
		return toolResultError(err), nil
	}
	return resultJSON(map[string]string{"status": "interrupted"}, nil)
}

func (h *Handler) handleMCPAgentResume(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentID, err := request.RequireString("agent_id")
	if err != nil {
		return toolResultError(err), nil
	}
	if err := h.service.ResumeAgent(ctx, agentID); err != nil {
		return toolResultError(err), nil
	}
	return resultJSON(map[string]string{"status": "resumed"}, nil)
}

func (h *Handler) handleMCPCheckStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentID, err := request.RequireString("agent_id")
	if err != nil {
		return toolResultError(err), nil
	}
	resp, err := h.service.CheckStatus(ctx, agentID)
	if err != nil {
		return toolResultError(err), nil
	}
	return resultJSON(resp, nil)
}

func (h *Handler) handleMCPPauseTask(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentID, err := request.RequireString("agent_id")
	if err != nil {
		return toolResultError(err), nil
	}
	taskID, err := request.RequireString("task_id")
	if err != nil {
		return toolResultError(err), nil
	}
	creatorAgentID, err := request.RequireString("creator_agent_id")
	if err != nil {
		return toolResultError(err), nil
	}
	if err := h.service.PauseTask(agentID, service.TaskKey{TaskID: taskID, CreatorAgentID: creatorAgentID}); err != nil {
		return toolResultError(err), nil
	}
	return resultJSON(map[string]string{"status": "paused"}, nil)
}

func (h *Handler) handleMCPResumeTask(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentID, err := request.RequireString("agent_id")
	if err != nil {
		return toolResultError(err), nil
	}
	taskID, err := request.RequireString("task_id")
	if err != nil {
		return toolResultError(err), nil
	}
	creatorAgentID, err := request.RequireString("creator_agent_id")
	if err != nil {
		return toolResultError(err), nil
	}
	if err := h.service.ResumeTask(ctx, agentID, service.TaskKey{TaskID: taskID, CreatorAgentID: creatorAgentID}); err != nil {
		return toolResultError(err), nil
	}
	return resultJSON(map[string]string{"status": "resumed"}, nil)
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
		Workspace:      request.GetString("workspace", ""),
		Prompt:         prompt,
		Schema:         request.GetString("schema", ""),
		Refs:           refs,
		RequestType:    request.GetString("request_type", ""),
		TaskID:         request.GetString("task_id", ""),
		CreatorAgentID: request.GetString("creator_agent_id", ""),
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
	items, err := sendResponseItemsFromToolRequest(request)
	if err != nil {
		return nil, err
	}
	out := make([]service.QueryResultItem, 0, len(items))
	for _, item := range items {
		out = append(out, service.QueryResultItem(item))
	}
	return out, nil
}

func sendResponseItemsFromToolRequest(request mcp.CallToolRequest) ([]service.SendResponseItem, error) {
	raw, ok := request.GetArguments()["results"]
	if !ok {
		return nil, fmt.Errorf("results is required")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("results must be an array")
	}
	var items []service.SendResponseItem
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

func toolResultError(err error) *mcp.CallToolResult {
	if err == nil {
		return mcp.NewToolResultError("unknown error")
	}
	msg := err.Error()
	if strings.HasPrefix(strings.TrimSpace(msg), "{") {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{mcp.TextContent{Type: "text", Text: msg}},
		}
	}
	return mcp.NewToolResultError(msg)
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

func (h *Handler) handleMCPListTasks(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logger.Debug("mcp tool call received", "tool", "list_tasks")
	agentID := request.GetString("agent_id", "")
	if agentID == "" {
		sender, err := senderFromContext(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		agentID = sender.ID
	}
	resp, err := h.service.ListTasks(ctx, agentID)
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "list_tasks", "agent_id", agentID)
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "list_tasks", "agent_id", agentID, "count", resp.Count)
	}
	return resultJSON(resp, err)
}

func (h *Handler) handleMCPGetTask(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logger.Debug("mcp tool call received", "tool", "get_task")
	agentID := request.GetString("agent_id", "")
	if agentID == "" {
		sender, err := senderFromContext(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		agentID = sender.ID
	}
	taskID, err := request.RequireString("task_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	resp, err := h.service.GetTask(ctx, agentID, taskID)
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "get_task", "agent_id", agentID, "task_id", taskID)
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "get_task", "agent_id", agentID, "task_id", taskID, "count", resp.Count)
	}
	return resultJSON(resp, err)
}

func (h *Handler) handleMCPListActiveTasks(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logger.Debug("mcp tool call received", "tool", "list_active_tasks")
	agentID := request.GetString("agent_id", "")
	if agentID == "" {
		sender, err := senderFromContext(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		agentID = sender.ID
	}
	resp, err := h.service.ListActiveTask(ctx, agentID)
	if err != nil {
		logger.Error("mcp tool service call failed", "err", err, "tool", "list_active_tasks", "agent_id", agentID)
	} else {
		logger.Debug("mcp tool call succeeded", "tool", "list_active_tasks", "agent_id", agentID, "count", resp.Count)
	}
	return resultJSON(resp, err)
}
