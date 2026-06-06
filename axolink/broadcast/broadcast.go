package broadcast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	storebroadcast "github.com/Shaik-Sirajuddin/memory/mcp/store/broadcast"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/google/uuid"
)

// Service registers MCP callback clients and notifies them when messages have
// been delivered by an agent.
type Service interface {
	Register(ctx context.Context, entry MCPClientEntry) error
	Unregister(ctx context.Context, serverID string) error
	Notify(ctx context.Context, messageIDs []string) error
}

type service struct {
	messages   message.MessageStore
	registry   *registry
	dispatcher Dispatcher
}

type Option func(*service)

func WithDispatcher(dispatcher Dispatcher) Option {
	return func(s *service) {
		logger.Debug("broadcast option: override dispatcher")
		s.dispatcher = dispatcher
	}
}

// New creates a broadcast service backed by message and registry stores.
func New(messages message.MessageStore, registryStore storebroadcast.RegistryStore, opts ...Option) Service {
	logger.Info("broadcast service initializing")
	s := &service{
		messages: messages,
		registry: newRegistry(registryStore),
		dispatcher: dispatcherSet{
			http: NewHTTPDispatcher(10 * time.Second),
			unix: NewUnixDispatcher(10 * time.Second),
			cli:  NewCLIDispatcher("omni"),
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	logger.Info("broadcast service initialized")
	return s
}

func (s *service) Register(ctx context.Context, entry MCPClientEntry) error {
	logger.Debug("broadcast register requested",
		"server_id", entry.ServerID,
		"agent_id", entry.AgentID,
		"callback_tool", entry.CallbackToolName,
		"callback_type", entry.CallbackType,
		"endpoint", entry.Endpoint,
		"auth_ref_present", entry.AuthenticationRef != "",
	)
	if err := s.registry.register(ctx, entry); err != nil {
		logger.Error("broadcast register failed", "err", err, "server_id", entry.ServerID)
		return err
	}
	logger.Info("broadcast client registered", "server_id", entry.ServerID, "callback_type", entry.CallbackType)
	return nil
}

func (s *service) Unregister(ctx context.Context, serverID string) error {
	logger.Debug("broadcast unregister requested", "server_id", serverID)
	if err := s.registry.unregister(ctx, serverID); err != nil {
		logger.Error("broadcast unregister failed", "err", err, "server_id", serverID)
		return err
	}
	logger.Info("broadcast client unregistered", "server_id", serverID)
	return nil
}

func (s *service) Notify(ctx context.Context, messageIDs []string) error {
	if len(messageIDs) == 0 {
		err := errors.New("message_ids is required")
		logger.Error("broadcast notify rejected", "err", err)
		return err
	}

	logger.Debug("broadcast notify requested", "message_count", len(messageIDs), "message_ids", messageIDs)
	var errs []error
	for _, messageID := range messageIDs {
		if err := s.notifyOne(ctx, messageID); err != nil {
			logger.Error("broadcast notify message failed", "err", err, "message_id", messageID)
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		logger.Error("broadcast notify completed with errors", "err", err, "message_count", len(messageIDs), "failed_count", len(errs))
		return err
	}
	logger.Debug("broadcast notify completed", "message_count", len(messageIDs))
	return nil
}

func (s *service) notifyOne(ctx context.Context, messageID string) error {
	logger.Debug("broadcast notify message loading", "message_id", messageID)
	msg, err := s.messages.GetMessage(ctx, messageID)
	if err != nil {
		logger.Error("broadcast notify get message failed", "err", err, "message_id", messageID)
		return fmt.Errorf("get message %q: %w", messageID, err)
	}
	logger.Debug("broadcast notify message loaded",
		"message_id", msg.ID,
		"from", msg.From,
		"from_spec", msg.FromSpec,
		"status", msg.Status,
		"should_reply", msg.ShouldReply,
	)
	if msg.FromSpec == message.SpecOmni {
		logger.Debug("broadcast notify: skipping omni_agent message", "message_id", msg.ID, "from_spec", msg.FromSpec)
		return nil
	}

	entry := cliEntryForMessage(msg)
	logger.Debug("broadcast notify client resolved",
		"message_id", msg.ID,
		"server_id", entry.ServerID,
		"callback_type", entry.CallbackType,
		"endpoint", entry.Endpoint,
		"source", "derived_cli",
	)

	payload := buildPayload(entry, msg)
	logger.Debug("broadcast notify dispatch starting",
		"message_id", msg.ID,
		"server_id", entry.ServerID,
		"callback_type", entry.CallbackType,
	)
	err = s.dispatcher.Dispatch(ctx, entry, payload)
	status := storebroadcast.AttemptSuccess
	errText := ""
	if err != nil {
		logger.Error("broadcast notify dispatch failed", "err", err, "message_id", msg.ID, "server_id", entry.ServerID)
		status = storebroadcast.AttemptFailed
		errText = err.Error()
	} else {
		logger.Debug("broadcast notify dispatch succeeded", "message_id", msg.ID, "server_id", entry.ServerID)
	}

	attempt := storebroadcast.CallbackAttempt{
		ID:          uuid.New().String(),
		MessageID:   msg.ID,
		ServerID:    entry.ServerID,
		Status:      status,
		Error:       errText,
		AttemptedAt: time.Now().UnixMilli(),
	}
	if recordErr := s.registry.store.RecordAttempt(ctx, attempt); recordErr != nil {
		logger.Warn("broadcast notify: record callback attempt failed",
			"message_id", msg.ID,
			"server_id", entry.ServerID,
			"err", recordErr,
		)
	} else {
		logger.Debug("broadcast notify attempt recorded",
			"attempt_id", attempt.ID,
			"message_id", attempt.MessageID,
			"server_id", attempt.ServerID,
			"status", attempt.Status,
		)
	}
	if err != nil {
		return fmt.Errorf("dispatch message %q to server %q: %w", msg.ID, entry.ServerID, err)
	}
	logger.Info("broadcast message dispatched", "message_id", msg.ID, "server_id", entry.ServerID, "callback_type", entry.CallbackType)
	return nil
}

func cliEntryForMessage(msg *message.Message) MCPClientEntry {
	return MCPClientEntry{
		ServerID:         msg.From,
		AgentID:          msg.From,
		CallbackToolName: "send_callback",
		CallbackType:     CallbackAGCLI,
	}
}

func buildPayload(entry MCPClientEntry, msg *message.Message) CallbackPayload {
	logger.Debug("broadcast payload building", "message_id", msg.ID, "server_id", entry.ServerID, "refs_bytes", len(msg.Refs))
	refs := json.RawMessage(msg.Refs)
	if !json.Valid(refs) {
		logger.Warn("broadcast notify: invalid refs json, sending empty refs", "message_id", msg.ID)
		refs = json.RawMessage(`{}`)
	}
	logger.Debug("broadcast payload built", "message_id", msg.ID, "server_id", entry.ServerID, "status", msg.Status)
	return CallbackPayload{
		ServerID:         entry.ServerID,
		CallbackToolName: entry.CallbackToolName,
		MessageID:        msg.ID,
		RespondedTo:      msg.RespondedTo,
		Prompt:           msg.Prompt,
		Refs:             refs,
		Status:           string(msg.Status),
		DeliveryTime:     msg.DeliveryTime,
	}
}
