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
	PauseTask(string, TaskKey)
}

type taskResumer interface {
	ResumeTask(context.Context, string, TaskKey)
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
	pauser.PauseTask(agentID, taskKey)
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
	resumer.ResumeTask(ctx, agentID, taskKey)
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

func validateResponseSchema(schemaText, response string) error {
	schemaText = strings.TrimSpace(schemaText)
	if schemaText == "" {
		return nil
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("response-schema.json", strings.NewReader(schemaText)); err != nil {
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
