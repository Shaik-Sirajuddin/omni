package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type taskPauser interface {
	PauseTask(agentID, taskID, creatorAgentID string)
}

type taskResumer interface {
	ResumeTask(ctx context.Context, agentID, taskID, creatorAgentID string)
}

func (s *Service) SendMessageWithMetadata(ctx context.Context, sender SenderSpec, payload PayloadMessage) (SendMessageResponse, error) {
	msg, err := s.buildMessage(ctx, sender, payload, "")
	if err != nil {
		return SendMessageResponse{}, err
	}
	msg.Schema = strings.TrimSpace(payload.Schema)
	msg.TaskID = strings.TrimSpace(payload.TaskID)
	msg.CreatorAgentID = strings.TrimSpace(payload.CreatorAgentID)
	if err := s.msgStore.InsertMessage(ctx, msg); err != nil {
		return SendMessageResponse{}, InternalError(err)
	}
	s.notifyArrived(ctx, msg.From, msg.To)
	return SendMessageResponse{MessageID: msg.ID}, nil
}

func (s *Service) SendGroupMessageWithMetadata(ctx context.Context, sender SenderSpec, payloads []PayloadMessage) (SendGroupMessageResponse, error) {
	if len(payloads) == 0 {
		return SendGroupMessageResponse{}, BadRequest("messages is required")
	}
	groupID := uuid.NewString()
	msgs := make([]*message.Message, 0, len(payloads))
	ids := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		msg, err := s.buildMessage(ctx, sender, payload, groupID)
		if err != nil {
			return SendGroupMessageResponse{}, err
		}
		msg.Schema = strings.TrimSpace(payload.Schema)
		msg.TaskID = strings.TrimSpace(payload.TaskID)
		msg.CreatorAgentID = strings.TrimSpace(payload.CreatorAgentID)
		msgs = append(msgs, msg)
		ids = append(ids, msg.ID)
	}
	if err := s.msgStore.InsertMessagesGroup(ctx, groupID, msgs); err != nil {
		return SendGroupMessageResponse{}, InternalError(err)
	}
	for _, msg := range msgs {
		s.notifyArrived(ctx, msg.From, msg.To)
	}
	return SendGroupMessageResponse{GroupID: groupID, MessageIDs: ids}, nil
}

func (s *Service) SendResponse(ctx context.Context, sender SenderSpec, item SendResponseItem) (SendResponseResponse, error) {
	msg, original, resp, err := s.buildSendResponseMessage(ctx, sender, item, "")
	if err != nil {
		return SendResponseResponse{}, err
	}
	if err := s.msgStore.InsertMessage(ctx, msg); err != nil {
		return SendResponseResponse{}, InternalError(err)
	}
	if err := s.markQueryReplyReceived(ctx, original); err != nil {
		return SendResponseResponse{}, err
	}
	s.notifyArrived(ctx, msg.From, msg.To)
	return resp, nil
}

func (s *Service) SendResponseBatch(ctx context.Context, sender SenderSpec, items []SendResponseItem) (SendResponseBatchResponse, error) {
	if len(items) == 0 {
		return SendResponseBatchResponse{}, BadRequest("results is required")
	}
	if err := validateQueryResultSender(sender); err != nil {
		return SendResponseBatchResponse{}, BadRequest(err.Error())
	}
	sender, err := s.resolveSender(ctx, sender, PayloadMessage{Workspace: sender.Workspace})
	if err != nil {
		return SendResponseBatchResponse{}, BadRequest(err.Error())
	}
	groupID := uuid.NewString()
	msgs := make([]*message.Message, 0, len(items))
	originals := make([]*message.Message, 0, len(items))
	results := make([]QueryResultResponse, 0, len(items))
	for _, item := range items {
		msg, original, resp, err := s.buildSendResponseMessageForResolvedSender(ctx, sender, item, groupID)
		if err != nil {
			return SendResponseBatchResponse{}, err
		}
		msgs = append(msgs, msg)
		originals = append(originals, original)
		results = append(results, resp)
	}
	if err := s.msgStore.InsertMessagesGroup(ctx, groupID, msgs); err != nil {
		return SendResponseBatchResponse{}, InternalError(err)
	}
	for _, original := range originals {
		if err := s.markQueryReplyReceived(ctx, original); err != nil {
			return SendResponseBatchResponse{}, err
		}
	}
	for _, msg := range msgs {
		s.notifyArrived(ctx, msg.From, msg.To)
	}
	return SendResponseBatchResponse{Results: results, Count: len(results), GroupID: groupID}, nil
}

func (s *Service) PauseTask(agentID string, taskKey TaskKey) error {
	if strings.TrimSpace(agentID) == "" {
		return BadRequest("agent_id is required")
	}
	if strings.TrimSpace(taskKey.TaskID) == "" {
		return BadRequest("task_id is required")
	}
	if strings.TrimSpace(taskKey.CreatorAgentID) == "" {
		return BadRequest("creator_agent_id is required")
	}
	pauser, ok := s.delivery.(taskPauser)
	if !ok {
		return ServiceError{status: http.StatusNotImplemented, err: fmt.Errorf("task control not yet implemented — PauseTask requires T1/T2/T3 engine changes")}
	}
	pauser.PauseTask(agentID, taskKey.TaskID, taskKey.CreatorAgentID)
	return nil
}

func (s *Service) ResumeTask(ctx context.Context, agentID string, taskKey TaskKey) error {
	if strings.TrimSpace(agentID) == "" {
		return BadRequest("agent_id is required")
	}
	if strings.TrimSpace(taskKey.TaskID) == "" {
		return BadRequest("task_id is required")
	}
	if strings.TrimSpace(taskKey.CreatorAgentID) == "" {
		return BadRequest("creator_agent_id is required")
	}
	resumer, ok := s.delivery.(taskResumer)
	if !ok {
		return ServiceError{status: http.StatusNotImplemented, err: fmt.Errorf("task control not yet implemented — ResumeTask requires T1/T2/T3 engine changes")}
	}
	resumer.ResumeTask(ctx, agentID, taskKey.TaskID, taskKey.CreatorAgentID)
	return nil
}

func (s *Service) buildSendResponseMessage(ctx context.Context, sender SenderSpec, item SendResponseItem, groupID string) (*message.Message, *message.Message, QueryResultResponse, error) {
	if err := validateQueryResultSender(sender); err != nil {
		return nil, nil, QueryResultResponse{}, BadRequest(err.Error())
	}
	sender, err := s.resolveSender(ctx, sender, PayloadMessage{Workspace: sender.Workspace})
	if err != nil {
		return nil, nil, QueryResultResponse{}, BadRequest(err.Error())
	}
	return s.buildSendResponseMessageForResolvedSender(ctx, sender, item, groupID)
}

func (s *Service) buildSendResponseMessageForResolvedSender(ctx context.Context, sender SenderSpec, item SendResponseItem, groupID string) (*message.Message, *message.Message, QueryResultResponse, error) {
	msg, original, resp, err := s.buildQueryResultMessageForResolvedSender(ctx, sender, QueryResultItem(item), groupID)
	if err != nil {
		return nil, nil, QueryResultResponse{}, err
	}
	if err := validateResponseSchema(original.Schema, item.Response); err != nil {
		return nil, nil, QueryResultResponse{}, ServiceError{status: http.StatusBadRequest, err: err}
	}
	return msg, original, resp, nil
}

// TaskSummary is a lightweight task record returned by ListTasks.
type TaskSummary struct {
	TaskID         string `json:"task_id"`
	CreatorAgentID string `json:"creator_agent_id"`
	TotalMessages  int    `json:"total_messages"`
	PendingCount   int    `json:"pending_count"`   // in_queue + queued + processing
	DeliveredCount int    `json:"delivered_count"` // delivered
	FailedCount    int    `json:"failed_count"`    // failed
}

// TaskListResponse is returned by ListTasks.
type TaskListResponse struct {
	Tasks []TaskSummary `json:"tasks"`
	Count int           `json:"count"`
}

// TaskDetailResponse is returned by GetTask.
type TaskDetailResponse struct {
	TaskID         string             `json:"task_id"`
	CreatorAgentID string             `json:"creator_agent_id"`
	Messages       []*message.Message `json:"messages"`
	Count          int                `json:"count"`
}

// ActiveTaskSummary holds info about a currently in-flight task.
type ActiveTaskSummary struct {
	TaskID         string `json:"task_id"`
	CreatorAgentID string `json:"creator_agent_id"`
	Status         string `json:"status"` // queued or processing
	MessageID      string `json:"message_id"`
}

// ActiveTaskResponse is returned by ListActiveTask.
type ActiveTaskResponse struct {
	Tasks []ActiveTaskSummary `json:"tasks"`
	Count int                 `json:"count"`
}

// ListTasks returns a summary of all tasks (by distinct task_id) received by agentID.
// If agentID is empty, it defaults to the sender's agent ID (self).
func (s *Service) ListTasks(ctx context.Context, agentID string) (TaskListResponse, error) {
	if strings.TrimSpace(agentID) == "" {
		return TaskListResponse{}, BadRequest("agent_id is required")
	}
	msgs, err := s.msgStore.RawQuery(ctx,
		`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id, task_id, creator_agent_id, schema
		 FROM messages WHERE "to" = ? AND task_id != '' ORDER BY sent_time DESC`,
		agentID,
	)
	if err != nil {
		return TaskListResponse{}, InternalError(err)
	}
	// Aggregate by task_id.
	seen := make(map[string]*TaskSummary)
	order := make([]string, 0)
	for _, m := range msgs {
		key := m.TaskID
		if _, ok := seen[key]; !ok {
			seen[key] = &TaskSummary{TaskID: m.TaskID, CreatorAgentID: m.CreatorAgentID}
			order = append(order, key)
		}
		ts := seen[key]
		ts.TotalMessages++
		switch m.Status {
		case message.StatusInQueue, message.StatusQueued, message.StatusProcessing:
			ts.PendingCount++
		case message.StatusDelivered:
			ts.DeliveredCount++
		case message.StatusFailed:
			ts.FailedCount++
		}
	}
	tasks := make([]TaskSummary, 0, len(order))
	for _, k := range order {
		tasks = append(tasks, *seen[k])
	}
	return TaskListResponse{Tasks: tasks, Count: len(tasks)}, nil
}

// GetTask returns all messages for the given task_id received by agentID.
func (s *Service) GetTask(ctx context.Context, agentID, taskID string) (TaskDetailResponse, error) {
	if strings.TrimSpace(agentID) == "" {
		return TaskDetailResponse{}, BadRequest("agent_id is required")
	}
	if strings.TrimSpace(taskID) == "" {
		return TaskDetailResponse{}, BadRequest("task_id is required")
	}
	msgs, err := s.msgStore.RawQuery(ctx,
		`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id, task_id, creator_agent_id, schema
		 FROM messages WHERE "to" = ? AND task_id = ? ORDER BY sent_time ASC`,
		agentID, taskID,
	)
	if err != nil {
		return TaskDetailResponse{}, InternalError(err)
	}
	var creatorAgentID string
	if len(msgs) > 0 {
		creatorAgentID = msgs[0].CreatorAgentID
	}
	return TaskDetailResponse{
		TaskID:         taskID,
		CreatorAgentID: creatorAgentID,
		Messages:       msgs,
		Count:          len(msgs),
	}, nil
}

// ListActiveTask returns currently in-flight (queued or processing) task messages for agentID.
// Defaults to self when agentID is empty.
func (s *Service) ListActiveTask(ctx context.Context, agentID string) (ActiveTaskResponse, error) {
	if strings.TrimSpace(agentID) == "" {
		return ActiveTaskResponse{}, BadRequest("agent_id is required")
	}
	msgs, err := s.msgStore.RawQuery(ctx,
		`SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id, task_id, creator_agent_id, schema
		 FROM messages WHERE "to" = ? AND task_id != '' AND status IN (?, ?, ?)
		 ORDER BY sent_time DESC`,
		agentID, string(message.StatusInQueue), string(message.StatusQueued), string(message.StatusProcessing),
	)
	if err != nil {
		return ActiveTaskResponse{}, InternalError(err)
	}
	tasks := make([]ActiveTaskSummary, 0, len(msgs))
	for _, m := range msgs {
		tasks = append(tasks, ActiveTaskSummary{
			TaskID:         m.TaskID,
			CreatorAgentID: m.CreatorAgentID,
			Status:         string(m.Status),
			MessageID:      m.ID,
		})
	}
	return ActiveTaskResponse{Tasks: tasks, Count: len(tasks)}, nil
}

func validateResponseSchema(schemaText, response string) error {
	schemaText = strings.TrimSpace(schemaText)
	if schemaText == "" {
		return nil
	}
	var schemaDoc any
	if err := json.Unmarshal([]byte(schemaText), &schemaDoc); err != nil {
		return fmt.Errorf(`{"error":"schema_mismatch","details":%q}`, "invalid stored schema: "+err.Error())
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("response-schema.json", schemaDoc); err != nil {
		return fmt.Errorf(`{"error":"schema_mismatch","details":%q}`, "invalid stored schema: "+err.Error())
	}
	schema, err := compiler.Compile("response-schema.json")
	if err != nil {
		return fmt.Errorf(`{"error":"schema_mismatch","details":%q}`, "invalid stored schema: "+err.Error())
	}
	var payload any
	if err := json.Unmarshal([]byte(response), &payload); err != nil {
		return fmt.Errorf(`{"error":"schema_mismatch","details":%q}`, "response must be valid JSON: "+err.Error())
	}
	if err := schema.Validate(payload); err != nil {
		return fmt.Errorf(`{"error":"schema_mismatch","details":%q}`, err.Error())
	}
	return nil
}
