package message

// Limits for message store validation.
const (
	MaxPromptBytes   = 64 * 1024 // 64 KB per message prompt
	MaxGroupMessages = 1000      // maximum messages per group
)

type Spec string
type RequestType string
type Status string

const (
	SpecOmni Spec = "omni_agent"

	RequestTypeQueryInstant RequestType = "query_instant"
	RequestTypeQuery        RequestType = "query"
	RequestTypeInstant      RequestType = "instant"
	RequestTypeExecute      RequestType = "execute"

	StatusPending    Status = "pending"
	StatusInQueue    Status = "in_queue"
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusDelivered  Status = "delivered"
	StatusFailed     Status = "failed"
)

// Message represents a routed message between agents or MCP servers.
type Message struct {
	ID           string      `json:"id"`
	To           string      `json:"to"`
	From         string      `json:"from"`
	FromSpec     Spec        `json:"from_spec"`
	ToSpec       Spec        `json:"to_spec"`
	RequestType  RequestType `json:"request_type"`
	IsResponse   bool        `json:"is_response"`
	ShouldReply  bool        `json:"should_reply"`
	RespondedTo    string      `json:"responded_to"`
	Prompt         string      `json:"prompt"`
	Schema         string      `json:"schema,omitempty"`
	Refs           string      `json:"refs"` // JSON blob
	Workspace      string      `json:"workspace"`
	TaskID         string      `json:"task_id,omitempty"`
	CreatorAgentID string      `json:"creator_agent_id,omitempty"`
	Status       Status      `json:"status"`
	Retries      int         `json:"retries"`
	QueueTime    int64       `json:"queue_time"`              // unix ms; 0 = not queued
	DeliveryTime *int64      `json:"delivery_time,omitempty"` // unix ms, nil if not yet delivered
	SentTime     int64       `json:"sent_time"`               // unix ms
	GroupID      string      `json:"group_id"`
}

// MessageGroup is a logical batch of related messages.
type MessageGroup struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
	Count     int    `json:"count"`
}

// Pagination parameters for conversation queries.
type Page struct {
	Offset int
	Limit  int
}
