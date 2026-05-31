package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/agents"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/Shaik-Sirajuddin/memory/operator"
	pkglog "github.com/Shaik-Sirajuddin/memory/pkg/log"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
	"github.com/google/uuid"
)

var logger = pkglog.NewLogger("component", "service")

type messageArriver interface {
	MessageArrived(context.Context, string, string)
}

type agentInterrupter interface {
	Interrupt(string)
}

type agentResumer interface {
	Resume(context.Context, string)
}

type agentStatusGetter interface {
	GetAgentStatus(context.Context, string) (AgentStatusResponse, bool)
}

type agentWorkspaceGetter interface {
	GetAgentWorkspace(context.Context, string) (string, bool)
}

type TeamStore interface {
	ListWorkspaces() ([]*operator.TeamInfo, error)
}

type Service struct {
	msgStore   message.MessageStore
	delivery   messageArriver
	agentStore agents.AgentStore
	teamStore  TeamStore
	nowUnixMS  func() int64
}

type ServiceError struct {
	status int
	err    error
}

func (e ServiceError) Error() string {
	return e.err.Error()
}

func (e ServiceError) Unwrap() error {
	return e.err
}

func New(msgStore message.MessageStore, delivery messageArriver, agentStore agents.AgentStore, teamStore TeamStore, nowUnixMS func() int64) *Service {
	return &Service{
		msgStore:   msgStore,
		delivery:   delivery,
		agentStore: agentStore,
		teamStore:  teamStore,
		nowUnixMS:  nowUnixMS,
	}
}

func (s *Service) SendMessage(ctx context.Context, sender SenderSpec, payload PayloadMessage) (SendMessageResponse, error) {
	logger.Debug("send_message called", "sender_id", sender.ID, "sender_type", sender.Kind, "sender_workspace", sender.Workspace, "to_id", payload.To.ID, "to_name", payload.To.Name, "to_type", payload.To.Type, "to_workspace", payload.To.Workspace, "workspace", payload.Workspace, "request_type", payload.RequestType, "prompt_bytes", len(payload.Prompt))
	msg, err := s.buildMessage(ctx, sender, payload, "")
	if err != nil {
		logger.Debug("send_message build failed", "sender_id", sender.ID, "err", err)
		return SendMessageResponse{}, err
	}
	if err := s.msgStore.InsertMessage(ctx, msg); err != nil {
		logger.Debug("send_message insert failed", "sender_id", sender.ID, "err", err)
		return SendMessageResponse{}, InternalError(err)
	}
	s.notifyArrived(ctx, msg.From, msg.To)
	logger.Debug("send_message succeeded", "sender_id", sender.ID, "message_id", msg.ID, "from", msg.From, "to", msg.To, "workspace", msg.Workspace)
	return SendMessageResponse{MessageID: msg.ID}, nil
}

func (s *Service) SendGroupMessage(ctx context.Context, sender SenderSpec, payloads []PayloadMessage) (SendGroupMessageResponse, error) {
	logger.Debug("send_group_message called", "sender_id", sender.ID, "sender_type", sender.Kind, "sender_workspace", sender.Workspace, "count", len(payloads))
	if len(payloads) == 0 {
		return SendGroupMessageResponse{}, BadRequest("messages is required")
	}
	groupID := uuid.NewString()
	msgs := make([]*message.Message, 0, len(payloads))
	ids := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		msg, err := s.buildMessage(ctx, sender, payload, groupID)
		if err != nil {
			logger.Debug("send_group_message build failed", "sender_id", sender.ID, "err", err)
			return SendGroupMessageResponse{}, err
		}
		msgs = append(msgs, msg)
		ids = append(ids, msg.ID)
	}
	if err := s.msgStore.InsertMessagesGroup(ctx, groupID, msgs); err != nil {
		logger.Debug("send_group_message insert failed", "sender_id", sender.ID, "group_id", groupID, "err", err)
		return SendGroupMessageResponse{}, InternalError(err)
	}
	for _, msg := range msgs {
		s.notifyArrived(ctx, msg.From, msg.To)
	}
	logger.Debug("send_group_message succeeded", "sender_id", sender.ID, "group_id", groupID, "count", len(ids))
	return SendGroupMessageResponse{GroupID: groupID, MessageIDs: ids}, nil
}

func (s *Service) QueryResult(ctx context.Context, sender SenderSpec, item QueryResultItem) (QueryResultResponse, error) {
	logger.Debug("query_result called", "sender_id", sender.ID, "sender_type", sender.Kind, "message_id", item.MessageID, "response_bytes", len(item.Response))
	msg, original, resp, err := s.buildQueryResultMessage(ctx, sender, item, "")
	if err != nil {
		logger.Debug("query_result build failed", "sender_id", sender.ID, "message_id", item.MessageID, "err", err)
		return QueryResultResponse{}, err
	}
	if err := s.msgStore.InsertMessage(ctx, msg); err != nil {
		logger.Debug("query_result insert failed", "sender_id", sender.ID, "message_id", item.MessageID, "err", err)
		return QueryResultResponse{}, InternalError(err)
	}
	if err := s.markQueryReplyReceived(ctx, original); err != nil {
		return QueryResultResponse{}, err
	}
	s.notifyArrived(ctx, msg.From, msg.To)
	logger.Debug("query_result succeeded", "sender_id", sender.ID, "reply_id", resp.MessageID, "responded_to", resp.RespondedTo)
	return resp, nil
}

func (s *Service) QueryResultBatch(ctx context.Context, sender SenderSpec, items []QueryResultItem) (QueryResultBatchResponse, error) {
	logger.Debug("query_result_batch called", "sender_id", sender.ID, "sender_type", sender.Kind, "count", len(items))
	if len(items) == 0 {
		return QueryResultBatchResponse{}, BadRequest("results is required")
	}
	if err := validateQueryResultSender(sender); err != nil {
		return QueryResultBatchResponse{}, BadRequest(err.Error())
	}
	sender, err := s.resolveSender(ctx, sender, PayloadMessage{Workspace: sender.Workspace})
	if err != nil {
		return QueryResultBatchResponse{}, BadRequest(err.Error())
	}
	groupID := uuid.NewString()
	msgs := make([]*message.Message, 0, len(items))
	originals := make([]*message.Message, 0, len(items))
	results := make([]QueryResultResponse, 0, len(items))
	for _, item := range items {
		msg, original, resp, err := s.buildQueryResultMessageForResolvedSender(ctx, sender, item, groupID)
		if err != nil {
			return QueryResultBatchResponse{}, err
		}
		msgs = append(msgs, msg)
		originals = append(originals, original)
		results = append(results, resp)
	}
	if err := s.msgStore.InsertMessagesGroup(ctx, groupID, msgs); err != nil {
		return QueryResultBatchResponse{}, InternalError(err)
	}
	for _, original := range originals {
		if err := s.markQueryReplyReceived(ctx, original); err != nil {
			return QueryResultBatchResponse{}, err
		}
	}
	for _, msg := range msgs {
		s.notifyArrived(ctx, msg.From, msg.To)
	}
	return QueryResultBatchResponse{Results: results, Count: len(results), GroupID: groupID}, nil
}

func (s *Service) GetMessage(ctx context.Context, id string) (*message.Message, error) {
	if strings.TrimSpace(id) == "" {
		return nil, BadRequest("id is required")
	}
	msg, err := s.msgStore.GetMessage(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ServiceError{status: http.StatusNotFound, err: fmt.Errorf("message not found")}
	}
	if err != nil {
		return nil, InternalError(err)
	}
	return msg, nil
}

func (s *Service) ListMessages(ctx context.Context, sender SenderSpec, req ListMessagesRequest) ([]*message.Message, error) {
	if strings.TrimSpace(req.ID) != "" {
		msg, err := s.GetMessage(ctx, req.ID)
		if err != nil {
			return nil, err
		}
		return []*message.Message{msg}, nil
	}
	if len(req.IDs) > 0 {
		msgs := make([]*message.Message, 0, len(req.IDs))
		for _, id := range req.IDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			msg, err := s.GetMessage(ctx, id)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, msg)
		}
		return msgs, nil
	}
	if req.GroupID != "" {
		msgs, err := s.msgStore.GetMessages(ctx, req.GroupID)
		if err != nil {
			return nil, InternalError(err)
		}
		return msgs, nil
	}
	from := strings.TrimSpace(req.From)
	if from == "" {
		from = sender.ID
	}
	to := strings.TrimSpace(req.To)
	if to == "" {
		return nil, BadRequest("to is required when group_id is not provided")
	}
	msgs, err := s.msgStore.GetConversationMessages(ctx, from, to, req.Page)
	if err != nil {
		return nil, InternalError(err)
	}
	return msgs, nil
}

func (s *Service) List(ctx context.Context, req ListRequest) ([]*message.Message, error) {
	if s.msgStore == nil {
		return nil, InternalError(fmt.Errorf("message store is unavailable"))
	}
	filter := strings.TrimSpace(req.Filter)
	if filter == "" && req.Team != "" {
		filter = "team=" + req.Team
	}

	query := `SELECT id, "to", "from", from_spec, to_spec, request_type, is_response, should_reply,
		        responded_to, prompt, refs, workspace, status, retries, queue_time, delivery_time, sent_time, group_id
		 FROM messages`
	args := []any{}
	switch {
	case filter == "", filter == "all":
	case filter == "mcp":
		query += ` WHERE from_spec != ? OR to_spec != ?`
		args = append(args, string(message.SpecOmni), string(message.SpecOmni))
	case filter == "agent":
		query += ` WHERE from_spec = ? OR to_spec = ?`
		args = append(args, string(message.SpecOmni), string(message.SpecOmni))
	case strings.HasPrefix(filter, "team="):
		team := strings.TrimSpace(strings.TrimPrefix(filter, "team="))
		if team == "" {
			return nil, BadRequest("team filter requires a value")
		}
		query += ` WHERE refs LIKE ?`
		args = append(args, `%`+team+`%`)
	default:
		return nil, BadRequest("filter must be all, mcp, agent, or team=<name>")
	}
	query += ` ORDER BY sent_time ASC LIMIT ? OFFSET ?`
	args = append(args, req.Page.Limit, req.Page.Offset)

	msgs, err := s.msgStore.RawQuery(ctx, query, args...)
	if err != nil {
		return nil, InternalError(err)
	}
	return msgs, nil
}

func (s *Service) DeleteMessage(ctx context.Context, id string) (DeleteMessageResponse, error) {
	if s.msgStore == nil {
		return DeleteMessageResponse{}, InternalError(fmt.Errorf("message store is unavailable"))
	}
	if strings.TrimSpace(id) == "" {
		return DeleteMessageResponse{}, BadRequest("id is required")
	}
	msg, err := s.msgStore.GetMessage(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return DeleteMessageResponse{}, ServiceError{status: http.StatusNotFound, err: fmt.Errorf("message not found")}
	}
	if err != nil {
		return DeleteMessageResponse{}, InternalError(err)
	}
	if msg.Status != message.StatusInQueue {
		return DeleteMessageResponse{}, ServiceError{status: http.StatusConflict, err: fmt.Errorf("message must be in_queue to delete")}
	}
	res, err := s.msgStore.RawExec(ctx, `DELETE FROM messages WHERE id = ? AND status = ?`, id, string(message.StatusInQueue))
	if err != nil {
		return DeleteMessageResponse{}, InternalError(err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return DeleteMessageResponse{}, InternalError(err)
	}
	if rows == 0 {
		return DeleteMessageResponse{}, ServiceError{status: http.StatusConflict, err: fmt.Errorf("message was not deleted")}
	}
	return DeleteMessageResponse{Deleted: true, ID: id}, nil
}

func (s *Service) ListAgents(workspace string) ([]*agents.AgentInfo, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, BadRequest("X-AGENT-WORKSPACE is required")
	}
	list, err := s.listWorkspaceAgents(workspace)
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (s *Service) ListTeams() (ListTeamsResponse, error) {
	if s.teamStore == nil {
		return ListTeamsResponse{}, InternalError(fmt.Errorf("operator store is unavailable"))
	}
	teams, err := s.teamStore.ListWorkspaces()
	if err != nil {
		return ListTeamsResponse{}, InternalError(err)
	}
	return ListTeamsResponse{Teams: teams, Count: len(teams)}, nil
}

func (s *Service) InterruptAgent(agentID string) error {
	if strings.TrimSpace(agentID) == "" {
		return BadRequest("agent_id is required")
	}
	interrupter, ok := s.delivery.(agentInterrupter)
	if !ok {
		return ServiceError{status: http.StatusNotImplemented, err: fmt.Errorf("agent interrupt is not available")}
	}
	interrupter.Interrupt(agentID)
	return nil
}

func (s *Service) ResumeAgent(ctx context.Context, agentID string) error {
	if strings.TrimSpace(agentID) == "" {
		return BadRequest("agent_id is required")
	}
	resumer, ok := s.delivery.(agentResumer)
	if !ok {
		return ServiceError{status: http.StatusNotImplemented, err: fmt.Errorf("agent resume is not available")}
	}
	resumer.Resume(ctx, agentID)
	return nil
}

func (s *Service) CheckStatus(ctx context.Context, agentID string) (AgentStatusResponse, error) {
	if strings.TrimSpace(agentID) == "" {
		return AgentStatusResponse{}, BadRequest("agent_id is required")
	}
	getter, ok := s.delivery.(agentStatusGetter)
	if !ok {
		return AgentStatusResponse{}, ServiceError{status: http.StatusNotImplemented, err: fmt.Errorf("agent status is not available")}
	}
	status, ok := getter.GetAgentStatus(ctx, agentID)
	if !ok {
		return AgentStatusResponse{}, ServiceError{status: http.StatusNotFound, err: fmt.Errorf("agent status not found")}
	}
	return status, nil
}

func (s *Service) buildMessage(ctx context.Context, sender SenderSpec, payload PayloadMessage, groupID string) (*message.Message, error) {
	if s.msgStore == nil {
		return nil, InternalError(fmt.Errorf("message store is unavailable"))
	}
	sender, err := s.resolveSender(ctx, sender, payload)
	if err != nil {
		return nil, BadRequest(err.Error())
	}
	targetType, err := ParseToSpec(payload.To.Type)
	if err != nil {
		return nil, BadRequest(err.Error())
	}
	to, workspace, err := s.resolveMessageTarget(ctx, sender, payload, targetType)
	if err != nil {
		return nil, BadRequest(err.Error())
	}
	if to == sender.ID {
		return nil, BadRequest("cannot send message to self")
	}
	if strings.TrimSpace(payload.Prompt) == "" {
		return nil, BadRequest("prompt is required")
	}
	refs := string(payload.Refs)
	if refs == "" {
		refs = "{}"
	}
	if !json.Valid([]byte(refs)) {
		return nil, BadRequest("refs must be valid json")
	}
	refs, err = s.enrichRefs(sender, workspace, refs)
	if err != nil {
		return nil, BadRequest(err.Error())
	}
	requestType := message.RequestTypeQuery
	if payload.RequestType != "" {
		requestType = message.RequestType(payload.RequestType)
	}
	shouldReply := true
	if payload.ShouldReply != nil {
		shouldReply = *payload.ShouldReply
	}
	return &message.Message{
		ID:          uuid.NewString(),
		To:          to,
		From:        sender.ID,
		FromSpec:    sender.Kind,
		ToSpec:      targetType,
		RequestType: requestType,
		ShouldReply: shouldReply,
		Prompt:      payload.Prompt,
		Refs:        refs,
		Workspace:   workspace,
		Status:      message.StatusInQueue,
		SentTime:    s.nowUnixMS(),
		GroupID:     groupID,
	}, nil
}

func (s *Service) buildQueryResultMessage(ctx context.Context, sender SenderSpec, item QueryResultItem, groupID string) (*message.Message, *message.Message, QueryResultResponse, error) {
	if err := validateQueryResultSender(sender); err != nil {
		return nil, nil, QueryResultResponse{}, BadRequest(err.Error())
	}
	sender, err := s.resolveSender(ctx, sender, PayloadMessage{Workspace: sender.Workspace})
	if err != nil {
		return nil, nil, QueryResultResponse{}, BadRequest(err.Error())
	}
	return s.buildQueryResultMessageForResolvedSender(ctx, sender, item, groupID)
}

func (s *Service) buildQueryResultMessageForResolvedSender(ctx context.Context, sender SenderSpec, item QueryResultItem, groupID string) (*message.Message, *message.Message, QueryResultResponse, error) {
	if s.msgStore == nil {
		return nil, nil, QueryResultResponse{}, InternalError(fmt.Errorf("message store is unavailable"))
	}
	messageID := strings.TrimSpace(item.MessageID)
	if messageID == "" {
		return nil, nil, QueryResultResponse{}, BadRequest("message_id is required")
	}
	response := strings.TrimSpace(item.Response)
	if response == "" {
		return nil, nil, QueryResultResponse{}, BadRequest("response is required")
	}
	original, err := s.GetMessage(ctx, messageID)
	if err != nil {
		return nil, nil, QueryResultResponse{}, err
	}
	if original.To != sender.ID {
		return nil, nil, QueryResultResponse{}, ServiceError{status: http.StatusForbidden, err: fmt.Errorf("caller is not the message recipient")}
	}
	replyID := uuid.NewString()
	reply := &message.Message{
		ID:          replyID,
		To:          original.From,
		From:        sender.ID,
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: message.RequestTypeInstant,
		IsResponse:  true,
		ShouldReply: false,
		RespondedTo: original.ID,
		Prompt:      response,
		Refs:        queryResultRefs(original, sender.ID),
		Workspace:   original.Workspace,
		Status:      message.StatusInQueue,
		SentTime:    s.nowUnixMS(),
		GroupID:     groupID,
	}
	return reply, original, QueryResultResponse{MessageID: replyID, RespondedTo: original.ID}, nil
}

func validateQueryResultSender(sender SenderSpec) error {
	if sender.Kind != message.SpecOmni {
		return fmt.Errorf("X-SENDER-TYPE must be omni_agent")
	}
	return nil
}

func (s *Service) markQueryReplyReceived(ctx context.Context, original *message.Message) error {
	now := s.nowUnixMS()
	original.Status = message.StatusDelivered
	original.ShouldReply = false
	original.DeliveryTime = &now
	if err := s.msgStore.UpdateMessage(ctx, original); err != nil {
		return InternalError(err)
	}
	return nil
}

func (s *Service) resolveMessageTarget(ctx context.Context, sender SenderSpec, payload PayloadMessage, targetType message.Spec) (string, string, error) {
	if targetType != message.SpecOmni {
		return "", "", fmt.Errorf("to.type must be omni_agent")
	}
	if strings.TrimSpace(payload.To.ID) != "" {
		agent, err := s.resolveTargetAgentByID(payload.To.ID)
		if err == nil {
			workspace := strings.TrimSpace(string(agent.WorkspaceDir))
			if workspace == "" {
				workspace = strings.TrimSpace(payload.To.Workspace)
			}
			if workspace == "" {
				workspace = strings.TrimSpace(payload.Workspace)
			}
			if workspace == "" {
				workspace = strings.TrimSpace(sender.Workspace)
			}
			return agent.ID, workspace, nil
		}
	}
	workspace, err := s.resolveWorkspace(ctx, sender, payload)
	if err != nil {
		return "", "", err
	}
	to, err := s.resolveTarget(ctx, payload.To, workspace)
	return to, workspace, err
}

func (s *Service) resolveWorkspace(ctx context.Context, sender SenderSpec, payload PayloadMessage) (string, error) {
	workspace := strings.TrimSpace(payload.To.Workspace)
	if workspace != "" {
		return workspace, nil
	}
	workspace = strings.TrimSpace(payload.Workspace)
	if workspace != "" {
		return workspace, nil
	}
	if strings.TrimSpace(sender.Workspace) != "" {
		return strings.TrimSpace(sender.Workspace), nil
	}
	if sender.Kind == "" {
		return "", fmt.Errorf("workspace required for omni_agent senders")
	}
	getter, ok := s.delivery.(agentWorkspaceGetter)
	if !ok {
		return "", fmt.Errorf("workspace required for omni_agent senders")
	}
	workspace, ok = getter.GetAgentWorkspace(ctx, sender.ID)
	if !ok || strings.TrimSpace(workspace) == "" {
		return "", fmt.Errorf("workspace required for omni_agent senders")
	}
	return strings.TrimSpace(workspace), nil
}

func (s *Service) resolveTarget(ctx context.Context, target TargetSpec, workspace string) (string, error) {
	_ = ctx
	if workspace == "" {
		return "", fmt.Errorf("X-AGENT-WORKSPACE is required for omni_agent targets")
	}
	list, err := s.listWorkspaceAgents(workspace)
	if err != nil {
		return "", err
	}
	for _, agent := range list {
		if target.ID != "" && (agent.ID == target.ID || agent.Name == target.ID) {
			return agent.ID, nil
		}
		if target.Name != "" && (agent.Name == target.Name || agent.ID == target.Name) {
			return agent.ID, nil
		}
	}
	return "", fmt.Errorf("agent not found in workspace")
}

func (s *Service) resolveSender(ctx context.Context, sender SenderSpec, payload PayloadMessage) (SenderSpec, error) {
	_ = ctx
	rawID := strings.TrimSpace(sender.ID)
	if rawID == "" {
		return SenderSpec{}, fmt.Errorf("X-SENDER-ID is required")
	}
	if s.agentStore == nil {
		return SenderSpec{}, InternalError(fmt.Errorf("agent store is unavailable"))
	}
	if agent, err := s.resolveTargetAgentByID(rawID); err == nil {
		sender.ID = agent.ID
		if strings.TrimSpace(sender.Workspace) == "" {
			sender.Workspace = strings.TrimSpace(string(agent.WorkspaceDir))
		}
		return sender, nil
	}

	workspace := strings.TrimSpace(sender.Workspace)
	if workspace == "" {
		workspace = strings.TrimSpace(payload.Workspace)
	}
	if workspace == "" {
		workspace = strings.TrimSpace(payload.To.Workspace)
	}
	if workspace == "" {
		return SenderSpec{}, fmt.Errorf("sender agent not found")
	}
	list, err := s.listWorkspaceAgents(workspace)
	if err != nil {
		return SenderSpec{}, err
	}
	for _, agent := range list {
		if agent == nil {
			continue
		}
		if agent.ID == rawID || agent.Name == rawID {
			sender.ID = agent.ID
			sender.Workspace = workspace
			return sender, nil
		}
	}
	return SenderSpec{}, fmt.Errorf("sender agent not found")
}

func (s *Service) resolveTargetAgentByID(id string) (*agents.AgentInfo, error) {
	if s.agentStore == nil {
		return nil, InternalError(fmt.Errorf("agent store is unavailable"))
	}
	agent, err := s.agentStore.GetAgent(strings.TrimSpace(id))
	if err != nil {
		return nil, fmt.Errorf("agent not found")
	}
	if agent == nil || agent.Info == nil {
		return nil, fmt.Errorf("agent not found")
	}
	return agent.Info, nil
}

func (s *Service) listWorkspaceAgents(workspace string) ([]*agents.AgentInfo, error) {
	if s.agentStore == nil {
		return nil, InternalError(fmt.Errorf("agent store is unavailable"))
	}
	resp := s.agentStore.ListAgents(agents.ListAgentParams{Workspace: sandbox.WorkspaceDir(workspace)})
	list := make([]*agents.AgentInfo, 0, len(resp.Agents))
	for _, agent := range resp.Agents {
		if agent == nil || agent.Info == nil {
			continue
		}
		list = append(list, agent.Info)
	}
	return list, nil
}

func (s *Service) enrichRefs(sender SenderSpec, workspace, refs string) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(refs), &fields); err != nil {
		return "", fmt.Errorf("refs must be valid json")
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	putStringRef(fields, "author_id", sender.ID)
	putStringRef(fields, "author_type", string(sender.Kind))
	putStringRef(fields, "author_workspace", workspace)
	putStringRef(fields, "author_agent_name", s.resolveAuthorAgentName(sender, workspace))
	putStringRef(fields, "author_team_name", s.resolveTeamName(workspace))

	out, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("refs must be valid json")
	}
	return string(out), nil
}

func (s *Service) resolveAuthorAgentName(sender SenderSpec, workspace string) string {
	list, err := s.listWorkspaceAgents(workspace)
	if err != nil {
		return sender.ID
	}
	for _, agent := range list {
		if agent == nil {
			continue
		}
		if agent.ID == sender.ID || agent.Name == sender.ID {
			return agent.Name
		}
	}
	return sender.ID
}

func (s *Service) resolveTeamName(workspace string) string {
	if s.teamStore == nil || strings.TrimSpace(workspace) == "" {
		return ""
	}
	teams, err := s.teamStore.ListWorkspaces()
	if err != nil {
		return ""
	}
	for _, team := range teams {
		if team != nil && team.WorkspaceDir == workspace {
			return team.Name
		}
	}
	return ""
}

func putStringRef(fields map[string]json.RawMessage, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	fields[key] = encoded
}

func queryResultRefs(original *message.Message, fromAgentID string) string {
	fields := map[string]json.RawMessage{}
	putStringRef(fields, "author", "tunnel-mcp")
	putStringRef(fields, "author_agent_id", fromAgentID)
	putStringRef(fields, "reply_to_message_id", original.ID)
	putStringRef(fields, "original_sender", original.From)
	out, err := json.Marshal(fields)
	if err != nil {
		return "{}"
	}
	return string(out)
}

func (s *Service) notifyArrived(ctx context.Context, from, to string) {
	if s.delivery == nil {
		return
	}
	go s.delivery.MessageArrived(ctx, from, to)
}

func HTTPError(status int, msg string) error {
	return ServiceError{status: status, err: fmt.Errorf("%s", msg)}
}

func BadRequest(msg string) error {
	return ServiceError{status: http.StatusBadRequest, err: fmt.Errorf("%s", msg)}
}

func InternalError(err error) error {
	return ServiceError{status: http.StatusInternalServerError, err: err}
}

func StatusFromError(err error) int {
	var serviceErr ServiceError
	if errors.As(err, &serviceErr) {
		return serviceErr.status
	}
	return http.StatusInternalServerError
}

func ParseBySpec(value string) (message.Spec, error) {
	switch strings.TrimSpace(value) {
	case string(message.SpecOmni):
		return message.SpecOmni, nil
	default:
		return "", fmt.Errorf("X-SENDER-TYPE must be omni_agent")
	}
}

func ParseToSpec(value string) (message.Spec, error) {
	switch strings.TrimSpace(value) {
	case string(message.SpecOmni):
		return message.SpecOmni, nil
	default:
		return "", fmt.Errorf("to.type must be omni_agent")
	}
}
