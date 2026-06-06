package service

import (
	"encoding/json"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/agents"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/Shaik-Sirajuddin/memory/operator"
)

type TargetSpec struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

type PayloadMessage struct {
	To             TargetSpec      `json:"to"`
	Workspace      string          `json:"workspace,omitempty"`
	Prompt         string          `json:"prompt"`
	Refs           json.RawMessage `json:"refs,omitempty"`
	Async          bool            `json:"async,omitempty"`
	RequestType    string          `json:"request_type,omitempty"`
	ShouldReply    *bool           `json:"should_reply,omitempty"`
	Schema         string          `json:"schema,omitempty"`
	TaskID         string          `json:"task_id,omitempty"`
	CreatorAgentID string          `json:"creator_agent_id,omitempty"`
}

type SendMessageRequest struct {
	Payload PayloadMessage `json:"payload_message"`
}

type SendGroupMessageRequest struct {
	Messages []PayloadMessage `json:"messages"`
}

type SendMessageResponse struct {
	MessageID string `json:"message_id"`
}

type SendGroupMessageResponse struct {
	GroupID    string   `json:"group_id"`
	MessageIDs []string `json:"message_ids"`
}

type QueryResultItem struct {
	MessageID string `json:"message_id"`
	Response  string `json:"response"`
}

type QueryResultRequest struct {
	Item QueryResultItem `json:"item"`
}

type QueryResultBatchRequest struct {
	Items []QueryResultItem `json:"results"`
}

type QueryResultResponse struct {
	MessageID   string `json:"message_id"`
	RespondedTo string `json:"responded_to"`
}

type QueryResultBatchResponse struct {
	Results []QueryResultResponse `json:"results"`
	Count   int                   `json:"count"`
	GroupID string                `json:"group_id,omitempty"`
}

// TaskKey identifies a task for pause/resume operations.
type TaskKey struct {
	TaskID         string
	CreatorAgentID string
}

// TaskControlRequest is the HTTP request body for pause/resume task endpoints.
type TaskControlRequest struct {
	AgentID        string `json:"agent_id"`
	TaskID         string `json:"task_id"`
	CreatorAgentID string `json:"creator_agent_id"`
}

// SendResponseItem is a response to a query message, including the response payload.
type SendResponseItem struct {
	MessageID string `json:"message_id"`
	Response  string `json:"response"`
}

// SendResponseRequest wraps a single SendResponseItem for HTTP requests.
type SendResponseRequest struct {
	Item SendResponseItem `json:"item"`
}

// SendResponseBatchRequest wraps multiple SendResponseItems for HTTP batch requests.
type SendResponseBatchRequest struct {
	Items []SendResponseItem `json:"items"`
}

// SendResponseResponse is an alias for QueryResultResponse.
type SendResponseResponse = QueryResultResponse

// SendResponseBatchResponse is an alias for QueryResultBatchResponse.
type SendResponseBatchResponse = QueryResultBatchResponse

type ErrorResponse struct {
	Error string `json:"error"`
}

type HealthResponse struct {
	Status    string `json:"status"`
	Service   string `json:"service"`
	Version   string `json:"version"`
	Transport string `json:"transport,omitempty"`
}

type SenderSpec struct {
	ID        string
	Kind      message.Spec
	Workspace string
}

type ListMessagesRequest struct {
	ID      string
	IDs     []string
	GroupID string
	From    string
	To      string
	Page    message.Page
}

type ListRequest struct {
	Filter string
	Team   string
	Page   message.Page
}

type DeleteMessageResponse struct {
	Deleted bool   `json:"deleted"`
	ID      string `json:"id"`
}

type AgentControlRequest struct {
	AgentID string `json:"agent_id"`
}

type AgentStatusResponse struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

type ListTeamsResponse struct {
	Teams []*operator.TeamInfo `json:"teams"`
	Count int                  `json:"count"`
}

type MessageResponse = message.Message

type ListAgentsResponse struct {
	Agents []*agents.AgentInfo `json:"agents"`
	Count  int                 `json:"count"`
}
