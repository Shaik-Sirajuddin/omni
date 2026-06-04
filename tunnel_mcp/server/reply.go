package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/google/uuid"
)

type replyService struct {
	msgStore     message.MessageStore
	onNewMessage func(context.Context, string, string)
}

func newReplyService(msgStore message.MessageStore, onNewMessage func(context.Context, string, string)) *replyService {
	return &replyService{
		msgStore:     msgStore,
		onNewMessage: onNewMessage,
	}
}

func (r *replyService) SendReply(ctx context.Context, msg *message.Message, fromAgentID, fromAgentName string) error {
	if msg == nil || !msg.ShouldReply {
		return nil
	}
	if r.msgStore == nil {
		return fmt.Errorf("message store is unavailable")
	}
	reply := &message.Message{
		ID:          uuid.NewString(),
		To:          msg.From,
		From:        fromAgentID,
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: msg.RequestType,
		IsResponse:  true,
		ShouldReply: false,
		RespondedTo: msg.ID,
		Prompt:      msg.Prompt,
		Refs:        replyRefs(msg, fromAgentID, fromAgentName),
		Workspace:   msg.Workspace,
		Status:      message.StatusInQueue,
		SentTime:    time.Now().UnixMilli(),
	}
	if err := r.msgStore.InsertMessage(ctx, reply); err != nil {
		return err
	}
	logger.Debug("send reply: inserted", "from_agent_id", fromAgentID, "from_agent_name", fromAgentName, "to", msg.From, "reply_id", reply.ID)
	if r.onNewMessage != nil {
		r.onNewMessage(ctx, fromAgentID, msg.From)
	}
	return nil
}

// SendFailureCallback sends an instant failure notification to the author of msg.
// Called when the agent exhausted send_response retries without responding.
func (r *replyService) SendFailureCallback(ctx context.Context, msg *message.Message, fromAgentID string) error {
	if msg == nil || !msg.ShouldReply {
		return nil
	}
	if r.msgStore == nil {
		return fmt.Errorf("message store is unavailable")
	}
	payload, _ := json.Marshal(map[string]string{
		"error":      "delivery_failed",
		"message_id": msg.ID,
		"reason":     "agent did not respond within retry limit; use send_message to start a new conversation",
	})
	failure := &message.Message{
		ID:          uuid.NewString(),
		To:          msg.From,
		From:        fromAgentID,
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: message.RequestTypeInstant,
		IsResponse:  true,
		ShouldReply: false,
		RespondedTo: msg.ID,
		Prompt:      string(payload),
		Refs:        failureRefs(msg, fromAgentID),
		Workspace:   msg.Workspace,
		Status:      message.StatusInQueue,
		SentTime:    time.Now().UnixMilli(),
		TaskID:      msg.TaskID,
		CreatorAgentID: msg.CreatorAgentID,
	}
	if err := r.msgStore.InsertMessage(ctx, failure); err != nil {
		return err
	}
	logger.Info("send failure callback: inserted", "from_agent_id", fromAgentID, "to", msg.From, "original_message_id", msg.ID)
	if r.onNewMessage != nil {
		r.onNewMessage(ctx, fromAgentID, msg.From)
	}
	return nil
}

func failureRefs(msg *message.Message, fromAgentID string) string {
	fields := map[string]json.RawMessage{}
	if strings.TrimSpace(msg.Refs) != "" {
		_ = json.Unmarshal([]byte(msg.Refs), &fields)
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	putReplyRef(fields, "author", "tunnel-mcp")
	putReplyRef(fields, "author_agent_id", fromAgentID)
	putReplyRef(fields, "reply_to_message_id", msg.ID)
	putReplyRef(fields, "original_sender", msg.From)
	out, err := json.Marshal(fields)
	if err != nil {
		return `{"author":"tunnel-mcp"}`
	}
	return string(out)
}

func replyRefs(msg *message.Message, fromAgentID, fromAgentName string) string {
	fields := map[string]json.RawMessage{}
	if strings.TrimSpace(msg.Refs) != "" {
		_ = json.Unmarshal([]byte(msg.Refs), &fields)
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	putReplyRef(fields, "author", "tunnel-mcp")
	putReplyRef(fields, "author_agent_id", fromAgentID)
	putReplyRef(fields, "author_agent_name", fromAgentName)
	putReplyRef(fields, "reply_to_message_id", msg.ID)
	putReplyRef(fields, "original_sender", msg.From)
	putReplyRef(fields, "author_workspace", msg.Workspace)

	out, err := json.Marshal(fields)
	if err != nil {
		return `{"author":"tunnel-mcp"}`
	}
	return string(out)
}

func putReplyRef(fields map[string]json.RawMessage, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	fields[key] = encoded
}
