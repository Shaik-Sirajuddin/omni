package service

import (
	"bytes"
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
	results := make([]SendResponseResponse, 0, len(items))
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

func (s *Service) PauseTask(agentID string, key TaskKey) error {
	if strings.TrimSpace(agentID) == "" {
		return BadRequest("agent_id is required")
	}
	if strings.TrimSpace(key.TaskID) == "" {
		return BadRequest("task_id is required")
	}
	if strings.TrimSpace(key.CreatorAgentID) == "" {
		return BadRequest("creator_agent_id is required")
	}
	pauser, ok := s.delivery.(taskPauser)
	if !ok {
		return ServiceError{status: http.StatusNotImplemented, err: fmt.Errorf("task pause is not available")}
	}
	pauser.PauseTask(agentID, key)
	return nil
}

func (s *Service) ResumeTask(ctx context.Context, agentID string, key TaskKey) error {
	if strings.TrimSpace(agentID) == "" {
		return BadRequest("agent_id is required")
	}
	if strings.TrimSpace(key.TaskID) == "" {
		return BadRequest("task_id is required")
	}
	if strings.TrimSpace(key.CreatorAgentID) == "" {
		return BadRequest("creator_agent_id is required")
	}
	resumer, ok := s.delivery.(taskResumer)
	if !ok {
		return ServiceError{status: http.StatusNotImplemented, err: fmt.Errorf("task resume is not available")}
	}
	resumer.ResumeTask(ctx, agentID, key)
	return nil
}

func (s *Service) buildSendResponseMessage(ctx context.Context, sender SenderSpec, item SendResponseItem, groupID string) (*message.Message, *message.Message, SendResponseResponse, error) {
	if err := validateQueryResultSender(sender); err != nil {
		return nil, nil, SendResponseResponse{}, BadRequest(err.Error())
	}
	sender, err := s.resolveSender(ctx, sender, PayloadMessage{Workspace: sender.Workspace})
	if err != nil {
		return nil, nil, SendResponseResponse{}, BadRequest(err.Error())
	}
	return s.buildSendResponseMessageForResolvedSender(ctx, sender, item, groupID)
}

func (s *Service) buildSendResponseMessageForResolvedSender(ctx context.Context, sender SenderSpec, item SendResponseItem, groupID string) (*message.Message, *message.Message, SendResponseResponse, error) {
	msg, original, resp, err := s.buildQueryResultMessageForResolvedSender(ctx, sender, QueryResultItem(item), groupID)
	if err != nil {
		return nil, nil, SendResponseResponse{}, err
	}
	if err := validateResponseSchema(original.Schema, item.Response); err != nil {
		return nil, nil, SendResponseResponse{}, err
	}
	msg.TaskID = original.TaskID
	msg.CreatorAgentID = original.CreatorAgentID
	return msg, original, SendResponseResponse(resp), nil
}

func validateResponseSchema(schemaText, response string) error {
	schemaText = strings.TrimSpace(schemaText)
	if schemaText == "" {
		return nil
	}
	response = strings.TrimSpace(response)
	if response == "" {
		return BadRequest("response is required")
	}
	var instance any
	if err := json.Unmarshal([]byte(response), &instance); err != nil {
		return schemaMismatchError(fmt.Sprintf("response is not valid json: %v", err))
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("message-schema.json", bytes.NewBufferString(schemaText)); err != nil {
		return InternalError(fmt.Errorf("invalid stored schema: %w", err))
	}
	schema, err := compiler.Compile("message-schema.json")
	if err != nil {
		return InternalError(fmt.Errorf("compile stored schema: %w", err))
	}
	if err := schema.Validate(instance); err != nil {
		return schemaMismatchError(err.Error())
	}
	return nil
}

func schemaMismatchError(details string) error {
	return ServiceError{
		status: http.StatusBadRequest,
		err:    fmt.Errorf(`{"error":"schema_mismatch","details":%q}`, details),
	}
}
