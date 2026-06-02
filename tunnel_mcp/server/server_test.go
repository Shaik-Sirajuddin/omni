//go:build unit

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/agents"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/Shaik-Sirajuddin/memory/operator"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerAPI(t *testing.T) {
	ctx := context.Background()

	t.Run("Send Message Stores And Notifies", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		delivery := &recordingDelivery{}
		srv := testServer(msgStore, delivery)
		body := `{"payload_message":{"to":{"type":"omni_agent","name":"worker"},"prompt":"run task","refs":{"k":"v"},"request_type":"execute"}}`

		rec := performRequest(t, srv, http.MethodPost, "/send-message", body)
		require.Equal(t, http.StatusAccepted, rec.Code, "Send message status should be accepted")
		var resp sendMessageResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "Send message response should decode")
		require.NotEmpty(t, resp.MessageID, "Send message response should include message id")

		got, err := msgStore.GetMessage(ctx, resp.MessageID)
		require.NoError(t, err, "Stored message should be retrievable")
		assert.Equal(t, "agent-1", got.To, "Stored message should target resolved agent id")
		assert.Equal(t, "mcp-client", got.From, "Stored message should include sender id")
		assert.Equal(t, message.SpecOmni, got.FromSpec, "Stored message sender type should match headers")
		assert.Equal(t, message.SpecOmni, got.ToSpec, "Stored message target type should match payload")
		assert.Equal(t, message.RequestTypeExecute, got.RequestType, "Stored message request type should match payload")
		assert.Equal(t, "/workspace", got.Workspace, "Stored message should include target workspace")
		assert.JSONEq(t, `{"k":"v","author_id":"mcp-client","author_type":"omni_agent","author_workspace":"/workspace","author_agent_name":"mcp-client","author_team_name":"workspace"}`, got.Refs, "Stored message refs should include author context")
		assert.Equal(t, []arrival{{from: "mcp-client", to: "agent-1"}}, delivery.waitForArrivals(t, 1), "Delivery should be notified for the stored message")
	})

	t.Run("Send Message Canonicalizes Sender And Target Names", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		delivery := &recordingDelivery{}
		srv := testServer(msgStore, delivery)
		body := `{"payload_message":{"to":{"type":"omni_agent","id":"worker"},"prompt":"run canonical"}}`
		req := httptest.NewRequest(http.MethodPost, "/send-message", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("X-SENDER-ID", "test-claude-1")
		req.Header.Set("X-SENDER-TYPE", "omni_agent")
		req.Header.Set("X-AGENT-WORKSPACE", "/workspace")
		rec := httptest.NewRecorder()

		srv.Routes().ServeHTTP(rec, req)

		require.Equal(t, http.StatusAccepted, rec.Code, "Send message should accept sender and target names")
		var resp sendMessageResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "Send message response should decode")
		got, err := msgStore.GetMessage(ctx, resp.MessageID)
		require.NoError(t, err, "Stored canonical message should be retrievable")
		assert.Equal(t, "agent-1", got.To, "Stored target should be canonical agent id")
		assert.Equal(t, "sender-1", got.From, "Stored sender should be canonical agent id")
		assert.Equal(t, []arrival{{from: "sender-1", to: "agent-1"}}, delivery.waitForArrivals(t, 1), "Delivery should use canonical ids")
	})

	t.Run("Send Group Message Stores Batch", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		delivery := &recordingDelivery{}
		srv := testServer(msgStore, delivery)
		body := `{"messages":[{"to":{"type":"omni_agent","id":"agent-1"},"prompt":"first"},{"to":{"type":"omni_agent","id":"agent-1"},"prompt":"second"}]}`

		rec := performRequest(t, srv, http.MethodPost, "/send-group-message", body)
		require.Equal(t, http.StatusAccepted, rec.Code, "Send group status should be accepted")
		var resp sendGroupMessageResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "Send group response should decode")
		require.Len(t, resp.MessageIDs, 2, "Send group response should include every message id")

		msgs, err := msgStore.GetMessages(ctx, resp.GroupID)
		require.NoError(t, err, "Group messages should be retrievable")
		require.Len(t, msgs, 2, "Group messages should include both messages")
		assert.Subset(t, []string{msgs[0].ID, msgs[1].ID}, resp.MessageIDs, "Stored group ids should match response ids")
		assert.Equal(t, []arrival{{from: "mcp-client", to: "agent-1"}, {from: "mcp-client", to: "agent-1"}}, delivery.waitForArrivals(t, 2), "Delivery should be notified for each group message")
	})

	t.Run("Send Group Message Canonicalizes Sender And Targets", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		delivery := &recordingDelivery{}
		srv := testServer(msgStore, delivery)
		body := `{"messages":[{"to":{"type":"omni_agent","id":"worker"},"prompt":"first"},{"to":{"type":"omni_agent","name":"worker"},"prompt":"second"}]}`
		req := httptest.NewRequest(http.MethodPost, "/send-group-message", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("X-SENDER-ID", "test-claude-1")
		req.Header.Set("X-SENDER-TYPE", "omni_agent")
		req.Header.Set("X-AGENT-WORKSPACE", "/workspace")
		rec := httptest.NewRecorder()

		srv.Routes().ServeHTTP(rec, req)

		require.Equal(t, http.StatusAccepted, rec.Code, "Send group should accept sender and target names")
		var resp sendGroupMessageResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "Send group response should decode")
		msgs, err := msgStore.GetMessages(ctx, resp.GroupID)
		require.NoError(t, err, "Canonical group messages should be retrievable")
		require.Len(t, msgs, 2, "Canonical group should include both messages")
		for _, got := range msgs {
			assert.Equal(t, "agent-1", got.To, "Stored group target should be canonical agent id")
			assert.Equal(t, "sender-1", got.From, "Stored group sender should be canonical agent id")
		}
		assert.Equal(t, []arrival{{from: "sender-1", to: "agent-1"}, {from: "sender-1", to: "agent-1"}}, delivery.waitForArrivals(t, 2), "Delivery should use canonical ids")
	})

	t.Run("Send Message By ID Does Not Require Workspace Header", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		delivery := &recordingDelivery{}
		srv := testServer(msgStore, delivery)
		body := `{"payload_message":{"to":{"type":"omni_agent","id":"agent-1"},"prompt":"run by id"}}`
		req := httptest.NewRequest(http.MethodPost, "/send-message", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("X-SENDER-ID", "mcp-client")
		req.Header.Set("X-SENDER-TYPE", "omni_agent")
		rec := httptest.NewRecorder()

		srv.Routes().ServeHTTP(rec, req)

		require.Equal(t, http.StatusAccepted, rec.Code, "Send message by id status should be accepted without workspace")
		var resp sendMessageResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "Send message by id response should decode")
		got, err := msgStore.GetMessage(ctx, resp.MessageID)
		require.NoError(t, err, "Stored message by id should be retrievable")
		assert.Equal(t, "agent-1", got.To, "Stored message by id should target requested agent")
		assert.Equal(t, "/workspace", got.Workspace, "Stored message by id should use agent workspace")
		assert.Equal(t, []arrival{{from: "mcp-client", to: "agent-1"}}, delivery.waitForArrivals(t, 1), "Delivery should be notified for id target")
	})

	t.Run("Send Message By Name Uses Sender Workspace", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		delivery := &recordingDelivery{}
		srv := testServer(msgStore, delivery)
		body := `{"payload_message":{"to":{"type":"omni_agent","name":"worker"},"prompt":"run by name"}}`
		req := httptest.NewRequest(http.MethodPost, "/send-message", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("X-SENDER-ID", "mcp-client")
		req.Header.Set("X-SENDER-TYPE", "omni_agent")
		rec := httptest.NewRecorder()

		srv.Routes().ServeHTTP(rec, req)

		require.Equal(t, http.StatusAccepted, rec.Code, "Send message by name should use canonical sender workspace")
		var resp sendMessageResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "Send message by name response should decode")
		got, err := msgStore.GetMessage(ctx, resp.MessageID)
		require.NoError(t, err, "Stored message by name should be retrievable")
		assert.Equal(t, "agent-1", got.To, "Stored message by name should target canonical agent id")
		assert.Equal(t, "mcp-client", got.From, "Stored message by name should keep canonical sender id")
		assert.Equal(t, "/workspace", got.Workspace, "Stored message by name should use sender workspace")
		assert.Equal(t, []arrival{{from: "mcp-client", to: "agent-1"}}, delivery.waitForArrivals(t, 1), "Delivery should use canonical ids")
	})

	t.Run("Get And List Messages", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		srv := testServer(msgStore, &recordingDelivery{})
		seed := &message.Message{
			ID:          "msg-1",
			To:          "agent-1",
			From:        "mcp-client",
			FromSpec:    message.SpecOmni,
			ToSpec:      message.SpecOmni,
			RequestType: message.RequestTypeQuery,
			ShouldReply: true,
			Prompt:      "hello",
			Refs:        "{}",
			Status:      message.StatusInQueue,
			SentTime:    100,
		}
		require.NoError(t, msgStore.InsertMessage(ctx, seed), "Seed message should insert")

		getRec := performRequest(t, srv, http.MethodGet, "/get-message?id=msg-1", "")
		require.Equal(t, http.StatusOK, getRec.Code, "Get message status should be ok")
		var got message.Message
		require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &got), "Get message response should decode")
		assert.Equal(t, seed.ID, got.ID, "Get message should return requested message")

		listRec := performRequest(t, srv, http.MethodGet, "/list-messages?to=agent-1", "")
		require.Equal(t, http.StatusOK, listRec.Code, "List messages status should be ok")
		var listed []message.Message
		require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &listed), "List messages response should decode")
		require.Len(t, listed, 1, "List messages should return seeded conversation")
		assert.Equal(t, seed.ID, listed[0].ID, "List messages should include seeded message")

		idRec := performRequest(t, srv, http.MethodGet, "/list-messages?id=msg-1", "")
		require.Equal(t, http.StatusOK, idRec.Code, "List messages by id status should be ok")
		var listedByID []message.Message
		require.NoError(t, json.Unmarshal(idRec.Body.Bytes(), &listedByID), "List messages by id response should decode")
		require.Len(t, listedByID, 1, "List messages by id should return one message")
		assert.Equal(t, seed.ID, listedByID[0].ID, "List messages by id should include requested message")
	})

	t.Run("List Filters MCP Agent And Team Messages", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		srv := testServer(msgStore, &recordingDelivery{})
		mcpMsg := &message.Message{
			ID:          "msg-mcp",
			To:          "agent-1",
			From:        "mcp-client",
			FromSpec:    message.SpecOmni,
			ToSpec:      message.SpecOmni,
			RequestType: message.RequestTypeQuery,
			ShouldReply: true,
			Prompt:      "mcp",
			Refs:        `{"team":"alpha"}`,
			Status:      message.StatusInQueue,
			SentTime:    100,
		}
		agentMsg := &message.Message{
			ID:          "msg-agent",
			To:          "mcp-client",
			From:        "agent-1",
			FromSpec:    message.SpecOmni,
			ToSpec:      message.SpecOmni,
			RequestType: message.RequestTypeQuery,
			ShouldReply: true,
			Prompt:      "agent",
			Refs:        `{"team":"beta"}`,
			Status:      message.StatusDelivered,
			SentTime:    200,
		}
		require.NoError(t, msgStore.InsertMessage(ctx, mcpMsg), "MCP seed message should insert")
		require.NoError(t, msgStore.InsertMessage(ctx, agentMsg), "Agent seed message should insert")

		mcpRec := performRequest(t, srv, http.MethodGet, "/list?filter=mcp", "")
		require.Equal(t, http.StatusOK, mcpRec.Code, "MCP list status should be ok")
		var mcpListed []message.Message
		require.NoError(t, json.Unmarshal(mcpRec.Body.Bytes(), &mcpListed), "MCP list response should decode")
		assert.Empty(t, mcpListed, "MCP list should be empty when all persisted specs are omni_agent")

		teamRec := performRequest(t, srv, http.MethodGet, "/list?filter=team=alpha", "")
		require.Equal(t, http.StatusOK, teamRec.Code, "Team list status should be ok")
		var teamListed []message.Message
		require.NoError(t, json.Unmarshal(teamRec.Body.Bytes(), &teamListed), "Team list response should decode")
		assert.Equal(t, []string{"msg-mcp"}, messageIDs(teamListed), "Team list should include matching team messages")
	})

	t.Run("Delete Message Only Allows In Queue", func(t *testing.T) {
		msgStore := message.WithTestDB(t)
		srv := testServer(msgStore, &recordingDelivery{})
		queued := &message.Message{
			ID:          "msg-delete",
			To:          "agent-1",
			From:        "mcp-client",
			FromSpec:    message.SpecOmni,
			ToSpec:      message.SpecOmni,
			RequestType: message.RequestTypeQuery,
			ShouldReply: true,
			Prompt:      "delete me",
			Refs:        "{}",
			Status:      message.StatusInQueue,
			SentTime:    100,
		}
		delivered := &message.Message{
			ID:          "msg-keep",
			To:          "agent-1",
			From:        "mcp-client",
			FromSpec:    message.SpecOmni,
			ToSpec:      message.SpecOmni,
			RequestType: message.RequestTypeQuery,
			ShouldReply: true,
			Prompt:      "keep me",
			Refs:        "{}",
			Status:      message.StatusDelivered,
			SentTime:    200,
		}
		require.NoError(t, msgStore.InsertMessage(ctx, queued), "Queued seed message should insert")
		require.NoError(t, msgStore.InsertMessage(ctx, delivered), "Delivered seed message should insert")

		deleteRec := performRequest(t, srv, http.MethodDelete, "/message?id=msg-delete", "")
		require.Equal(t, http.StatusOK, deleteRec.Code, "Delete queued message status should be ok")
		var deleted deleteMessageResponse
		require.NoError(t, json.Unmarshal(deleteRec.Body.Bytes(), &deleted), "Delete response should decode")
		assert.True(t, deleted.Deleted, "Delete response should report deletion")
		assert.Equal(t, "msg-delete", deleted.ID, "Delete response id should match")

		_, err := msgStore.GetMessage(ctx, "msg-delete")
		require.Error(t, err, "Deleted message lookup should fail")

		conflictRec := performRequest(t, srv, http.MethodDelete, "/message?id=msg-keep", "")
		assert.Equal(t, http.StatusConflict, conflictRec.Code, "Delete delivered message status should be conflict")
	})

	t.Run("List Agents Uses Workspace Header", func(t *testing.T) {
		srv := testServer(message.WithTestDB(t), &recordingDelivery{})

		rec := performRequest(t, srv, http.MethodGet, "/list-agents", "")
		require.Equal(t, http.StatusOK, rec.Code, "List agents status should be ok")
		var list []agents.AgentInfo
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list), "List agents response should decode")
		require.Len(t, list, 3, "List agents should return workspace agents")
		assert.Subset(t, agentNames(list), []string{"worker", "mcp-client", "test-claude-1"}, "List agents should include configured agents")
	})

	t.Run("List Teams Uses Operator Store", func(t *testing.T) {
		srv := testServer(message.WithTestDB(t), &recordingDelivery{})

		rec := performRequest(t, srv, http.MethodGet, "/list-teams", "")
		require.Equal(t, http.StatusOK, rec.Code, "List teams status should be ok")
		var resp listTeamsResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "List teams response should decode")
		require.Len(t, resp.Teams, 1, "List teams should return operator teams")
		assert.Equal(t, 1, resp.Count, "List teams count should match returned teams")
		assert.Equal(t, "/workspace", resp.Teams[0].WorkspaceDir, "List teams should include workspace dir")
	})

	t.Run("REST Health Is Public On Routes And Daemon", func(t *testing.T) {
		srv := testServer(message.WithTestDB(t), &recordingDelivery{})

		routesRec := httptest.NewRecorder()
		routesReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		srv.Routes().ServeHTTP(routesRec, routesReq)
		require.Equal(t, http.StatusOK, routesRec.Code, "Routes health status should be ok")

		daemonRec := httptest.NewRecorder()
		daemonReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		srv.DaemonHandler("/mcp").ServeHTTP(daemonRec, daemonReq)
		require.Equal(t, http.StatusOK, daemonRec.Code, "Daemon health status should be ok")

		var health healthResponse
		require.NoError(t, json.Unmarshal(daemonRec.Body.Bytes(), &health), "Daemon health response should decode")
		assert.Equal(t, "ok", health.Status, "Daemon health status should be ok")
		assert.Equal(t, "axolink", health.Service, "Daemon health service should identify axolink")
		assert.Equal(t, serviceVersion, health.Version, "Daemon health version should match service version")
	})

	t.Run("MCP Health Tool Returns Status", func(t *testing.T) {
		srv := testServer(message.WithTestDB(t), &recordingDelivery{})

		result, err := srv.handleMCPHealth(ctx, mcp.CallToolRequest{})
		require.NoError(t, err, "MCP health handler should not fail")
		require.False(t, result.IsError, "MCP health result should not be an error")
		require.Len(t, result.Content, 1, "MCP health result should include one content item")
		content, ok := result.Content[0].(mcp.TextContent)
		require.True(t, ok, "MCP health content should be text content")

		var health healthResponse
		require.NoError(t, json.Unmarshal([]byte(content.Text), &health), "MCP health response should decode")
		assert.Equal(t, "ok", health.Status, "MCP health status should be ok")
		assert.Equal(t, "axolink", health.Service, "MCP health service should identify axolink")
		assert.Equal(t, serviceVersion, health.Version, "MCP health version should match service version")
		assert.Equal(t, "mcp", health.Transport, "MCP health transport should identify MCP")
	})

	t.Run("Auth Rejects Missing Token", func(t *testing.T) {
		srv := testServer(message.WithTestDB(t), &recordingDelivery{})
		req := httptest.NewRequest(http.MethodGet, "/list-agents", nil)
		rec := httptest.NewRecorder()

		srv.Routes().ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code, "Missing bearer token should be rejected")
	})

	t.Run("Daemon Handler Mounts Engine Hook Routes", func(t *testing.T) {
		delivery := &hookDelivery{}
		srv := testServer(message.WithTestDB(t), &delivery.recordingDelivery)
		srv.delivery = delivery
		req := httptest.NewRequest(http.MethodPost, "/hook", nil)
		rec := httptest.NewRecorder()

		srv.DaemonHandler("/mcp").ServeHTTP(rec, req)

		assert.Equal(t, http.StatusAccepted, rec.Code, "Hook route status should be accepted")
		assert.True(t, delivery.hookCalled, "Hook route should be mounted by daemon handler")
	})

	t.Run("Agent Control Routes Use Delivery", func(t *testing.T) {
		delivery := &recordingDelivery{}
		srv := testServer(message.WithTestDB(t), delivery)

		interruptRec := performRequest(t, srv, http.MethodPost, "/agent-interrupt", `{"agent_id":"agent-1"}`)
		require.Equal(t, http.StatusOK, interruptRec.Code, "Agent interrupt status should be ok")
		assert.Equal(t, []string{"agent-1"}, delivery.interrupts, "Agent interrupt should call delivery")

		resumeRec := performRequest(t, srv, http.MethodPost, "/agent-resume", `{"agent_id":"agent-1"}`)
		require.Equal(t, http.StatusOK, resumeRec.Code, "Agent resume status should be ok")
		assert.Equal(t, []string{"agent-1"}, delivery.resumes, "Agent resume should call delivery")

		statusRec := performRequest(t, srv, http.MethodGet, "/check-status?agent_id=agent-1", "")
		require.Equal(t, http.StatusOK, statusRec.Code, "Agent status status should be ok")
		var status agentStatusResponse
		require.NoError(t, json.Unmarshal(statusRec.Body.Bytes(), &status), "Agent status response should decode")
		assert.Equal(t, "agent-1", status.AgentID, "Agent status should include agent id")
		assert.Equal(t, "ready", status.Status, "Agent status should include delivery status")
	})

	t.Run("Stdio Bridge Forwards To HTTP Endpoint", func(t *testing.T) {
		var gotBody string
		var gotSender string
		bridge := NewStdioBridge("http://mcp.local/mcp", "", map[string]string{"X-SENDER-ID": "mcp-client"})
		bridge.SetClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err, "Bridge request body should read")
			gotBody = string(body)
			gotSender = r.Header.Get("X-SENDER-ID")
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)),
				Header:     make(http.Header),
			}, nil
		})})

		resp, err := bridge.Forward(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		require.NoError(t, err, "Stdio bridge forward should not fail")

		assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, gotBody, "Stdio bridge should forward the JSON-RPC frame")
		assert.Equal(t, "mcp-client", gotSender, "Stdio bridge should forward sender headers")
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`, string(resp), "Stdio bridge should return the HTTP response body")
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func testServer(msgStore message.MessageStore, delivery *recordingDelivery) *Server {
	return NewWithDelivery(0, delivery,
		WithMessageStore(msgStore),
		WithAuthToken("test-token"),
		WithClock(func() int64 { return 1234 }),
		WithAgentStore(&fakeAgentStore{agents: []*agents.AgentData{{
			Info: &agents.AgentInfo{
				ID:           "agent-1",
				Name:         "worker",
				WorkspaceDir: sandbox.WorkspaceDir("/workspace"),
				MemoryDir:    "memory/agents/worker",
			},
		}, {
			Info: &agents.AgentInfo{
				ID:           "mcp-client",
				Name:         "mcp-client",
				WorkspaceDir: sandbox.WorkspaceDir("/workspace"),
				MemoryDir:    "memory/agents/mcp-client",
			},
		}, {
			Info: &agents.AgentInfo{
				ID:           "sender-1",
				Name:         "test-claude-1",
				WorkspaceDir: sandbox.WorkspaceDir("/workspace"),
				MemoryDir:    "memory/agents/test-claude-1",
			},
		}}}),
		WithTeamStore(&fakeTeamStore{teams: []*operator.TeamInfo{{
			ID:           "team-1",
			Name:         "workspace",
			WorkspaceDir: "/workspace",
			Agents:       1,
		}}}),
	)
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

func performRequest(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("X-SENDER-ID", "mcp-client")
	req.Header.Set("X-SENDER-TYPE", "omni_agent")
	req.Header.Set("X-AGENT-WORKSPACE", "/workspace")
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	return rec
}

func messageIDs(msgs []message.Message) []string {
	ids := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		ids = append(ids, msg.ID)
	}
	return ids
}

func agentNames(list []agents.AgentInfo) []string {
	names := make([]string, 0, len(list))
	for _, agent := range list {
		names = append(names, agent.Name)
	}
	return names
}

type arrival struct {
	from string
	to   string
}

type recordingDelivery struct {
	mu         sync.Mutex
	ready      chan struct{}
	items      []arrival
	interrupts []string
	resumes    []string
}

func (d *recordingDelivery) MessageArrived(_ context.Context, from, to string) {
	d.mu.Lock()
	if d.ready == nil {
		d.ready = make(chan struct{}, 10)
	}
	d.items = append(d.items, arrival{from: from, to: to})
	d.mu.Unlock()
	d.ready <- struct{}{}
}

func (d *recordingDelivery) waitForArrivals(t *testing.T, count int) []arrival {
	t.Helper()
	d.mu.Lock()
	if d.ready == nil {
		d.ready = make(chan struct{}, 10)
	}
	d.mu.Unlock()
	for {
		d.mu.Lock()
		if len(d.items) >= count {
			items := append([]arrival(nil), d.items...)
			d.mu.Unlock()
			return items
		}
		d.mu.Unlock()
		select {
		case <-d.ready:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "Delivery notification should arrive before timeout")
		}
	}
}

func (d *recordingDelivery) Interrupt(agentID string) {
	d.interrupts = append(d.interrupts, agentID)
}

func (d *recordingDelivery) Resume(_ context.Context, agentID string) {
	d.resumes = append(d.resumes, agentID)
}

func (d *recordingDelivery) GetAgentStatus(_ context.Context, agentID string) (agentStatusResponse, bool) {
	return agentStatusResponse{AgentID: agentID, Status: "ready"}, true
}

type hookDelivery struct {
	recordingDelivery
	hookCalled bool
}

func (d *hookDelivery) RegisterHookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/hook", func(w http.ResponseWriter, _ *http.Request) {
		d.hookCalled = true
		w.WriteHeader(http.StatusAccepted)
	})
}
