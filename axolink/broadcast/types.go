package broadcast

import (
	"context"
	"encoding/json"

	storebroadcast "github.com/Shaik-Sirajuddin/memory/mcp/store/broadcast"
)

type CallbackType = storebroadcast.CallbackType
type MCPClientEntry = storebroadcast.MCPClientEntry

const (
	CallbackHTTP         = storebroadcast.CallbackHTTP
	CallbackHTTPOverUnix = storebroadcast.CallbackHTTPOverUnix
	CallbackAGCLI        = storebroadcast.CallbackAGCLI
)

// CallbackPayload is sent to registered MCP clients after an agent reply is
// delivered.
type CallbackPayload struct {
	ServerID         string          `json:"server_id"`
	CallbackToolName string          `json:"callback_tool_name"`
	MessageID        string          `json:"message_id"`
	RespondedTo      string          `json:"responded_to,omitempty"`
	Prompt           string          `json:"prompt"`
	Refs             json.RawMessage `json:"refs"`
	Status           string          `json:"status"`
	DeliveryTime     *int64          `json:"delivery_time,omitempty"`
}

// Dispatcher sends one callback payload using a registered MCP client entry.
type Dispatcher interface {
	Dispatch(ctx context.Context, entry MCPClientEntry, payload CallbackPayload) error
}
