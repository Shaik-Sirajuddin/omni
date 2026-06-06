//go:build unit

package mcpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/server/service"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/agents"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/Shaik-Sirajuddin/memory/operator"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPListAgents(t *testing.T) {
	t.Run("Returns Object Shape", func(t *testing.T) {
		handler := New(service.New(
			message.WithTestDB(t),
			nil,
			&fakeAgentStore{agents: []*agents.AgentData{{
				Info: &agents.AgentInfo{
					ID:           "agent-1",
					Name:         "worker",
					WorkspaceDir: sandbox.WorkspaceDir("/workspace"),
					MemoryDir:    "memory/agents/worker",
				},
			}}},
			&fakeTeamStore{teams: []*operator.TeamInfo{{
				ID:           "team-1",
				Name:         "workspace",
				WorkspaceDir: "/workspace",
				Agents:       1,
			}}},
			func() int64 { return 1234 },
		))
		ctx := context.WithValue(context.Background(), senderContextKey{}, service.SenderSpec{
			ID:        "mcp-client",
			Kind:      message.SpecOmni,
			Workspace: "/workspace",
		})

		result, err := handler.handleMCPListAgents(ctx, mcp.CallToolRequest{})

		require.NoError(t, err, "List agents tool should not return handler error")
		require.False(t, result.IsError, "List agents tool result should not be an error")
		content, ok := result.Content[0].(mcp.TextContent)
		require.True(t, ok, "List agents tool content should be text")

		var got listAgentsToolResponse
		require.NoError(t, json.Unmarshal([]byte(content.Text), &got), "List agents tool response should decode as object")
		require.Len(t, got.Agents, 1, "List agents tool response should include workspace agents")
		assert.Equal(t, 1, got.Count, "List agents tool response count should match agents length")
		assert.Equal(t, "worker", got.Agents[0].Name, "List agents tool response should include agent name")

		structured, ok := result.StructuredContent.(listAgentsToolResponse)
		require.True(t, ok, "List agents structured content should be an object")
		assert.Equal(t, 1, structured.Count, "List agents structured content should include count")
	})
}

func TestMCPListTeams(t *testing.T) {
	handler := New(service.New(
		message.WithTestDB(t),
		nil,
		&fakeAgentStore{},
		&fakeTeamStore{teams: []*operator.TeamInfo{{
			ID:           "team-1",
			Name:         "workspace",
			WorkspaceDir: "/workspace",
			Agents:       1,
		}}},
		func() int64 { return 1234 },
	))
	ctx := context.WithValue(context.Background(), senderContextKey{}, service.SenderSpec{
		ID:        "mcp-client",
		Kind:      message.SpecOmni,
		Workspace: "/workspace",
	})

	result, err := handler.handleMCPListTeams(ctx, mcp.CallToolRequest{})

	require.NoError(t, err, "List teams tool should not return handler error")
	require.False(t, result.IsError, "List teams tool result should not be an error")
	content, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "List teams tool content should be text")

	var got service.ListTeamsResponse
	require.NoError(t, json.Unmarshal([]byte(content.Text), &got), "List teams tool response should decode")
	require.Len(t, got.Teams, 1, "List teams tool response should include operator teams")
	assert.Equal(t, 1, got.Count, "List teams tool response count should match teams length")
	assert.Equal(t, "/workspace", got.Teams[0].WorkspaceDir, "List teams tool should include workspace dir")
}

func TestMCPListMessages(t *testing.T) {
	handler := New(service.New(
		message.WithTestDB(t),
		nil,
		&fakeAgentStore{},
		&fakeTeamStore{},
		func() int64 { return 1234 },
	))
	ctx := context.WithValue(context.Background(), senderContextKey{}, service.SenderSpec{
		ID:        "sender-1",
		Kind:      message.SpecOmni,
		Workspace: "/workspace",
	})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"to": "agent-1",
	}}}

	result, err := handler.handleMCPListMessages(ctx, req)

	require.NoError(t, err, "List messages tool should not return handler error")
	require.False(t, result.IsError, "List messages result should not be an error")
	content, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "List messages tool content should be text")
	assert.JSONEq(t, `{"messages":[],"count":0}`, content.Text, "List messages should return an object with an empty array")

	structured, ok := result.StructuredContent.(listMessagesToolResponse)
	require.True(t, ok, "List messages structured content should be an object")
	assert.NotNil(t, structured.Messages, "List messages should not expose a nil messages slice")
	assert.Empty(t, structured.Messages, "List messages should be empty")
	assert.Equal(t, 0, structured.Count, "List messages count should be zero")
}

func TestMCPSendMessageCanonicalizesSenderAndTargetNames(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	handler := New(service.New(
		msgStore,
		nil,
		&fakeAgentStore{agents: testAgents()},
		&fakeTeamStore{teams: []*operator.TeamInfo{{
			ID:           "team-1",
			Name:         "workspace",
			WorkspaceDir: "/workspace",
			Agents:       2,
		}}},
		func() int64 { return 1234 },
	))
	toolCtx := context.WithValue(ctx, senderContextKey{}, service.SenderSpec{
		ID:        "test-claude-1",
		Kind:      message.SpecOmni,
		Workspace: "/workspace",
	})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"to_type": "omni_agent",
		"to_id":   "worker",
		"prompt":  "run canonical",
	}}}

	result, err := handler.handleMCPSendMessage(toolCtx, req)

	require.NoError(t, err, "Send message tool should not return handler error")
	require.False(t, result.IsError, "Send message result should not be an error")
	content, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "Send message tool content should be text")
	var resp service.SendMessageResponse
	require.NoError(t, json.Unmarshal([]byte(content.Text), &resp), "Send message response should decode")
	got, err := msgStore.GetMessage(ctx, resp.MessageID)
	require.NoError(t, err, "MCP-created message should be retrievable")
	assert.Equal(t, "sender-1", got.From, "MCP sender name should be persisted as canonical agent id")
	assert.Equal(t, "agent-1", got.To, "MCP target name should be persisted as canonical agent id")
}

func TestMCPQueryResult(t *testing.T) {
	ctx := context.Background()

	t.Run("Stores Response And Notifies Original Sender", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		delivery := newRecordingDelivery()
		handler := New(service.New(
			msgStore,
			delivery,
			&fakeAgentStore{agents: testAgents()},
			&fakeTeamStore{},
			func() int64 { return 1234 },
		))
		seedQueryMessage(t, ctx, msgStore, "query-1", "sender-1", "agent-1")
		toolCtx := context.WithValue(ctx, senderContextKey{}, service.SenderSpec{
			ID:        "test-claude-1",
			Kind:      message.SpecOmni,
			Workspace: "/workspace",
		})
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"message_id": "query-1",
			"response":   "query answer",
		}}}

		result, err := handler.handleMCPQueryResult(toolCtx, req)

		require.NoError(t, err, "Query result tool should not return handler error")
		require.False(t, result.IsError, "Query result should not be an error")
		content, ok := result.Content[0].(mcp.TextContent)
		require.True(t, ok, "Query result content should be text")
		var resp service.QueryResultResponse
		require.NoError(t, json.Unmarshal([]byte(content.Text), &resp), "Query result response should decode")
		assert.Equal(t, "query-1", resp.RespondedTo, "Query result should identify original message")

		got, err := msgStore.GetMessage(ctx, resp.MessageID)
		require.NoError(t, err, "Inserted query response should be retrievable")
		assert.Equal(t, "agent-1", got.To, "Query response should target original sender")
		assert.Equal(t, "sender-1", got.From, "Query response should come from canonical caller id")
		assert.Equal(t, message.RequestTypeInstant, got.RequestType, "Query response should be instant")
		assert.True(t, got.IsResponse, "Query response should be marked as a response")
		assert.False(t, got.ShouldReply, "Query response should not request another reply")
		assert.Equal(t, "query-1", got.RespondedTo, "Query response should link original message")
		assert.Equal(t, "query answer", got.Prompt, "Query response text should be stored as message body")
		assert.Equal(t, message.StatusInQueue, got.Status, "Query response should be queued")
		assert.JSONEq(t, `{"author":"tunnel-mcp","author_agent_id":"sender-1","reply_to_message_id":"query-1","original_sender":"agent-1"}`, got.Refs, "Query response refs should include reply metadata")

		original, err := msgStore.GetMessage(ctx, "query-1")
		require.NoError(t, err, "Original query message should be retrievable")
		assert.Equal(t, message.StatusDelivered, original.Status, "Original query should be marked delivered after response")
		assert.False(t, original.ShouldReply, "Original query should not require another reply after response")
		require.NotNil(t, original.DeliveryTime, "Original query should record reply received time")
		assert.Equal(t, int64(1234), *original.DeliveryTime, "Original query delivery time should use service clock")

		assert.Equal(t, arrival{from: "sender-1", to: "agent-1"}, delivery.wait(t), "Query response should notify original sender")
	})

	t.Run("Rejects Non Recipient Caller", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		handler := New(service.New(
			msgStore,
			nil,
			&fakeAgentStore{agents: testAgents()},
			&fakeTeamStore{},
			func() int64 { return 1234 },
		))
		seedQueryMessage(t, ctx, msgStore, "query-1", "agent-1", "sender-1")
		toolCtx := context.WithValue(ctx, senderContextKey{}, service.SenderSpec{
			ID:        "test-claude-1",
			Kind:      message.SpecOmni,
			Workspace: "/workspace",
		})
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"message_id": "query-1",
			"response":   "query answer",
		}}}

		result, err := handler.handleMCPQueryResult(toolCtx, req)

		require.NoError(t, err, "Query result rejection should be returned as tool result")
		require.True(t, result.IsError, "Query result should reject non-recipient callers")
	})

	t.Run("Requires Response Argument", func(t *testing.T) {
		handler := New(service.New(message.WithTestDB(t), nil, &fakeAgentStore{agents: testAgents()}, &fakeTeamStore{}, func() int64 { return 1234 }))
		toolCtx := context.WithValue(ctx, senderContextKey{}, service.SenderSpec{
			ID:        "sender-1",
			Kind:      message.SpecOmni,
			Workspace: "/workspace",
		})
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"message_id": "query-1",
		}}}

		result, err := handler.handleMCPQueryResult(toolCtx, req)

		require.NoError(t, err, "Missing response should be returned as tool result")
		require.True(t, result.IsError, "Query result should require response, not prompt")
	})
}

func TestMCPQueryResultBatch(t *testing.T) {
	ctx := context.Background()
	msgStore := message.WithTestDB(t)
	delivery := newRecordingDelivery()
	handler := New(service.New(
		msgStore,
		delivery,
		&fakeAgentStore{agents: testAgents()},
		&fakeTeamStore{},
		func() int64 { return 1234 },
	))
	seedQueryMessage(t, ctx, msgStore, "query-1", "sender-1", "agent-1")
	seedQueryMessage(t, ctx, msgStore, "query-2", "sender-1", "agent-1")
	toolCtx := context.WithValue(ctx, senderContextKey{}, service.SenderSpec{
		ID:        "test-claude-1",
		Kind:      message.SpecOmni,
		Workspace: "/workspace",
	})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"results": []any{
			map[string]any{"message_id": "query-1", "response": "first answer"},
			map[string]any{"message_id": "query-2", "response": "second answer"},
		},
	}}}

	result, err := handler.handleMCPQueryResultBatch(toolCtx, req)

	require.NoError(t, err, "Query result batch tool should not return handler error")
	require.False(t, result.IsError, "Query result batch should not be an error")
	content, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "Query result batch content should be text")
	var resp service.QueryResultBatchResponse
	require.NoError(t, json.Unmarshal([]byte(content.Text), &resp), "Query result batch response should decode")
	require.Len(t, resp.Results, 2, "Batch response should include each inserted response")
	assert.Equal(t, 2, resp.Count, "Batch response count should match result length")
	assert.NotEmpty(t, resp.GroupID, "Batch responses should share a group id")

	msgs, err := msgStore.GetMessages(ctx, resp.GroupID)
	require.NoError(t, err, "Batch response group should be retrievable")
	require.Len(t, msgs, 2, "Batch response group should include both messages")
	assert.Equal(t, "first answer", msgs[0].Prompt, "First query response body should match")
	assert.Equal(t, "second answer", msgs[1].Prompt, "Second query response body should match")

	for _, id := range []string{"query-1", "query-2"} {
		original, err := msgStore.GetMessage(ctx, id)
		require.NoError(t, err, "Original batch query message should be retrievable")
		assert.Equal(t, message.StatusDelivered, original.Status, "Original batch query should be marked delivered after response")
		assert.False(t, original.ShouldReply, "Original batch query should not require another reply after response")
		require.NotNil(t, original.DeliveryTime, "Original batch query should record reply received time")
		assert.Equal(t, int64(1234), *original.DeliveryTime, "Original batch query delivery time should use service clock")
	}

	assert.Equal(t, arrival{from: "sender-1", to: "agent-1"}, delivery.wait(t), "First batch response should notify original sender")
	assert.Equal(t, arrival{from: "sender-1", to: "agent-1"}, delivery.wait(t), "Second batch response should notify original sender")
}

type fakeAgentStore struct {
	agents []*agents.AgentData
}

func (s *fakeAgentStore) Save(*agents.AgentData) error {
	return nil
}

func (s *fakeAgentStore) Create(*agents.AgentData) error {
	return nil
}

func (s *fakeAgentStore) ListAgents(params agents.ListAgentParams) agents.ListAgentResponse {
	list := make([]*agents.AgentData, 0, len(s.agents))
	for _, agent := range s.agents {
		if agent == nil || agent.Info == nil || agent.Info.WorkspaceDir != params.Workspace {
			continue
		}
		list = append(list, agent)
	}
	return agents.ListAgentResponse{Agents: list}
}

func (s *fakeAgentStore) DeleteAgent(string) error {
	return nil
}

func (s *fakeAgentStore) GetAgent(id string) (*agents.AgentData, error) {
	for _, agent := range s.agents {
		if agent != nil && agent.Info != nil && agent.Info.ID == id {
			return agent, nil
		}
	}
	return nil, assert.AnError
}

func (s *fakeAgentStore) GetActiveSession(string) (*agents.CodeSession, error) {
	return nil, nil
}

func (s *fakeAgentStore) UpdateActiveSession(string, *agents.CodeSession) error {
	return nil
}

func (s *fakeAgentStore) CreateSession(string, *agents.CodeSession) error {
	return nil
}

func (s *fakeAgentStore) GetSettings(string) (*agents.Settings, error) {
	return nil, nil
}

func (s *fakeAgentStore) UpdateSettings(string, *agents.Settings) error {
	return nil
}

type fakeTeamStore struct {
	teams []*operator.TeamInfo
}

func (s *fakeTeamStore) ListWorkspaces() ([]*operator.TeamInfo, error) {
	return s.teams, nil
}

func testAgents() []*agents.AgentData {
	return []*agents.AgentData{{
		Info: &agents.AgentInfo{
			ID:           "agent-1",
			Name:         "worker",
			WorkspaceDir: sandbox.WorkspaceDir("/workspace"),
			MemoryDir:    "memory/agents/worker",
		},
	}, {
		Info: &agents.AgentInfo{
			ID:           "sender-1",
			Name:         "test-claude-1",
			WorkspaceDir: sandbox.WorkspaceDir("/workspace"),
			MemoryDir:    "memory/agents/test-claude-1",
		},
	}}
}

type arrival struct {
	from string
	to   string
}

type recordingDelivery struct {
	ready chan arrival
}

func newRecordingDelivery() *recordingDelivery {
	return &recordingDelivery{ready: make(chan arrival, 10)}
}

func (d *recordingDelivery) MessageArrived(_ context.Context, from, to string) {
	d.ready <- arrival{from: from, to: to}
}

func (d *recordingDelivery) wait(t *testing.T) arrival {
	t.Helper()
	select {
	case item := <-d.ready:
		return item
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Delivery notification should arrive")
		return arrival{}
	}
}

func seedQueryMessage(t *testing.T, ctx context.Context, msgStore message.MessageStore, id, to, from string) {
	t.Helper()
	require.NoError(t, msgStore.InsertMessage(ctx, &message.Message{
		ID:          id,
		To:          to,
		From:        from,
		FromSpec:    message.SpecOmni,
		ToSpec:      message.SpecOmni,
		RequestType: message.RequestTypeQuery,
		ShouldReply: true,
		Prompt:      "question",
		Refs:        "{}",
		Workspace:   "/workspace",
		Status:      message.StatusProcessing,
		SentTime:    100,
	}), "Seed query message should insert")
}
