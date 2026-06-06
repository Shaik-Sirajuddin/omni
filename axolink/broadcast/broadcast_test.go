//go:build unit

package broadcast

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	storebroadcast "github.com/Shaik-Sirajuddin/memory/mcp/store/broadcast"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService(t *testing.T) {
	ctx := context.Background()

	t.Run("Register Persists Entry", func(t *testing.T) {
		registry := newFakeRegistryStore()
		svc := New(newFakeMessageStore(), registry, WithDispatcher(fakeDispatcher{}))
		entry := testEntry(CallbackHTTP)

		err := svc.Register(ctx, entry)
		require.NoError(t, err, "Register should persist a valid entry")

		got, err := registry.Get(ctx, entry.ServerID)
		require.NoError(t, err, "Registry store should load registered entry")
		assert.Equal(t, entry.ServerID, got.ServerID, "Registered entry server id should match")
		assert.Equal(t, entry.CallbackType, got.CallbackType, "Registered entry callback type should match")
		assert.NotZero(t, got.UpdatedAt, "Registered entry updated time should be set")
	})

	t.Run("Notify Dispatches Non Omni Message", func(t *testing.T) {
		messages := newFakeMessageStore()
		registry := newFakeRegistryStore()
		dispatcher := &recordingDispatcher{}
		svc := New(messages, registry, WithDispatcher(dispatcher))
		msg := testMessage("msg-1", message.Spec("external"))
		messages.messages[msg.ID] = msg

		err := svc.Notify(ctx, []string{msg.ID})
		require.NoError(t, err, "Notify should dispatch MCP messages without registry entries")

		require.Len(t, dispatcher.calls, 1, "Dispatcher should receive one callback")
		assert.Equal(t, CallbackAGCLI, dispatcher.calls[0].entry.CallbackType, "Derived callback entry should use ag_cli")
		assert.Equal(t, msg.From, dispatcher.calls[0].entry.ServerID, "Derived callback entry agent id should match original caller")
		assert.Empty(t, dispatcher.calls[0].entry.Endpoint, "Derived callback entry workspace should be empty when unknown")
		assert.Equal(t, msg.ID, dispatcher.calls[0].payload.MessageID, "Callback payload message id should match")
		assert.JSONEq(t, msg.Refs, string(dispatcher.calls[0].payload.Refs), "Callback payload refs should match")
		attempts, err := registry.ListAttempts(ctx, msg.ID)
		require.NoError(t, err, "Registry store should list callback attempts")
		require.Len(t, attempts, 1, "Notify should record one callback attempt")
		assert.Equal(t, storebroadcast.AttemptSuccess, attempts[0].Status, "Callback attempt status should be success")
	})

	t.Run("Notify Skips Omni Agent Message", func(t *testing.T) {
		messages := newFakeMessageStore()
		registry := newFakeRegistryStore()
		dispatcher := &recordingDispatcher{}
		svc := New(messages, registry, WithDispatcher(dispatcher))
		msg := testMessage("msg-agent", message.SpecOmni)
		messages.messages[msg.ID] = msg

		err := svc.Notify(ctx, []string{msg.ID})
		require.NoError(t, err, "Notify should skip omni_agent messages without error")
		assert.Empty(t, dispatcher.calls, "Dispatcher should not be called for omni_agent messages")
	})

	t.Run("Notify Records Failed Attempt", func(t *testing.T) {
		messages := newFakeMessageStore()
		registry := newFakeRegistryStore()
		dispatcher := fakeDispatcher{err: assert.AnError}
		svc := New(messages, registry, WithDispatcher(dispatcher))
		msg := testMessage("msg-failed", message.Spec("external"))
		messages.messages[msg.ID] = msg

		err := svc.Notify(ctx, []string{msg.ID})
		require.Error(t, err, "Notify should return dispatch failures")

		attempts, listErr := registry.ListAttempts(ctx, msg.ID)
		require.NoError(t, listErr, "Registry store should list failed callback attempts")
		require.Len(t, attempts, 1, "Notify should record one failed callback attempt")
		assert.Equal(t, storebroadcast.AttemptFailed, attempts[0].Status, "Callback attempt status should be failed")
		assert.NotEmpty(t, attempts[0].Error, "Callback attempt error should be recorded")
	})
}

func TestDispatchers(t *testing.T) {
	ctx := context.Background()

	t.Run("HTTP Dispatcher Sends JSON And Authorization", func(t *testing.T) {
		var gotAuth string
		var gotPayload CallbackPayload
		dispatcher := &HTTPDispatcher{
			client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotAuth = r.Header.Get("Authorization")
				require.NoError(t, json.NewDecoder(r.Body).Decode(&gotPayload), "HTTP callback body should decode")
				return &http.Response{
					StatusCode: http.StatusAccepted,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
				}, nil
			})},
		}
		entry := testEntry(CallbackHTTP)
		entry.Endpoint = "http://example.test/callback"
		entry.AuthenticationRef = "secret"
		payload := testPayload("msg-http")

		err := dispatcher.Dispatch(ctx, entry, payload)
		require.NoError(t, err, "HTTP dispatcher should send callback")
		assert.Equal(t, "Bearer secret", gotAuth, "HTTP dispatcher should send bearer authorization")
		assert.Equal(t, payload.MessageID, gotPayload.MessageID, "HTTP dispatcher payload should match")
	})

	t.Run("Unix Endpoint Parser", func(t *testing.T) {
		socketPath, requestPath, err := parseUnixEndpoint("unix:///tmp/callback.sock:/callback")
		require.NoError(t, err, "Unix endpoint parser should accept valid endpoints")
		assert.Equal(t, "/tmp/callback.sock", socketPath, "Unix endpoint parser should return socket path")
		assert.Equal(t, "/callback", requestPath, "Unix endpoint parser should return request path")
	})

	t.Run("CLI Dispatcher Sends Success Prompt To Agent Session", func(t *testing.T) {
		executor := &recordingSessionExecutor{}
		entry := testEntry(CallbackAGCLI)
		payload := testPayload("msg-cli")

		err := NewCLIDispatcherWithExecutor("omni", executor).Dispatch(ctx, entry, payload)
		require.NoError(t, err, "CLI dispatcher should exec in agent session")
		assert.Equal(t, entry.ServerID, executor.agentID, "CLI dispatcher agent id should match entry target")
		assert.Equal(t, entry.Endpoint, executor.workspace, "CLI dispatcher workspace should match entry endpoint")
		assert.Contains(t, executor.prompt, "request_type: callback_success", "CLI dispatcher prompt should include callback success request type")
		assert.Contains(t, executor.prompt, "message_id: msg-cli", "CLI dispatcher prompt should include message id")
		assert.Contains(t, executor.prompt, "status: delivered", "CLI dispatcher prompt should include delivered status")
		assert.Contains(t, executor.prompt, "callback payload", "CLI dispatcher prompt should include message prompt")
		assert.Contains(t, executor.prompt, `refs: '{"source":"test"}'`, "CLI dispatcher prompt should include refs")
	})
}

type fakeMessageStore struct {
	messages map[string]*message.Message
}

func newFakeMessageStore() *fakeMessageStore {
	return &fakeMessageStore{messages: make(map[string]*message.Message)}
}

func (s *fakeMessageStore) InsertMessage(ctx context.Context, msg *message.Message) error {
	s.messages[msg.ID] = msg
	return nil
}

func (s *fakeMessageStore) UpdateMessage(ctx context.Context, msg *message.Message) error {
	s.messages[msg.ID] = msg
	return nil
}

func (s *fakeMessageStore) InsertMessagesGroup(ctx context.Context, groupID string, msgs []*message.Message) error {
	for _, msg := range msgs {
		msg.GroupID = groupID
		s.messages[msg.ID] = msg
	}
	return nil
}

func (s *fakeMessageStore) GetMessage(ctx context.Context, id string) (*message.Message, error) {
	msg, ok := s.messages[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return msg, nil
}

func (s *fakeMessageStore) GetMessages(ctx context.Context, groupID string) ([]*message.Message, error) {
	return nil, nil
}

func (s *fakeMessageStore) GetConversationMessages(ctx context.Context, from, to string, page message.Page) ([]*message.Message, error) {
	return nil, nil
}

func (s *fakeMessageStore) GetPendingAgents(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (s *fakeMessageStore) GetWorkspaceForAgent(ctx context.Context, agentID string) (string, error) {
	return "", sql.ErrNoRows
}

func (s *fakeMessageStore) RawQuery(ctx context.Context, query string, args ...any) ([]*message.Message, error) {
	return nil, nil
}

func (s *fakeMessageStore) RawExec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return nil, nil
}

type fakeRegistryStore struct {
	entries  map[string]*storebroadcast.MCPClientEntry
	attempts map[string][]*storebroadcast.CallbackAttempt
}

func newFakeRegistryStore() *fakeRegistryStore {
	return &fakeRegistryStore{
		entries:  make(map[string]*storebroadcast.MCPClientEntry),
		attempts: make(map[string][]*storebroadcast.CallbackAttempt),
	}
}

func (s *fakeRegistryStore) Upsert(ctx context.Context, entry storebroadcast.MCPClientEntry) error {
	copied := entry
	s.entries[entry.ServerID] = &copied
	return nil
}

func (s *fakeRegistryStore) Get(ctx context.Context, serverID string) (*storebroadcast.MCPClientEntry, error) {
	entry, ok := s.entries[serverID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	copied := *entry
	return &copied, nil
}

func (s *fakeRegistryStore) List(ctx context.Context) ([]*storebroadcast.MCPClientEntry, error) {
	entries := make([]*storebroadcast.MCPClientEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		copied := *entry
		entries = append(entries, &copied)
	}
	return entries, nil
}

func (s *fakeRegistryStore) Delete(ctx context.Context, serverID string) error {
	delete(s.entries, serverID)
	return nil
}

func (s *fakeRegistryStore) RecordAttempt(ctx context.Context, attempt storebroadcast.CallbackAttempt) error {
	copied := attempt
	s.attempts[attempt.MessageID] = append(s.attempts[attempt.MessageID], &copied)
	return nil
}

func (s *fakeRegistryStore) ListAttempts(ctx context.Context, messageID string) ([]*storebroadcast.CallbackAttempt, error) {
	return s.attempts[messageID], nil
}

type dispatchCall struct {
	entry   MCPClientEntry
	payload CallbackPayload
}

type recordingDispatcher struct {
	calls []dispatchCall
}

func (d *recordingDispatcher) Dispatch(ctx context.Context, entry MCPClientEntry, payload CallbackPayload) error {
	d.calls = append(d.calls, dispatchCall{entry: entry, payload: payload})
	return nil
}

type fakeDispatcher struct {
	err error
}

func (d fakeDispatcher) Dispatch(ctx context.Context, entry MCPClientEntry, payload CallbackPayload) error {
	return d.err
}

type recordingSessionExecutor struct {
	agentID   string
	agentName string
	workspace string
	prompt    string
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func (r *recordingSessionExecutor) ExecInSession(ctx context.Context, agentID, agentName, workspace, prompt string) error {
	r.agentID = agentID
	r.agentName = agentName
	r.workspace = workspace
	r.prompt = prompt
	return nil
}

func testEntry(callbackType CallbackType) MCPClientEntry {
	return MCPClientEntry{
		ServerID:         "mcp-server",
		AgentID:          "broadcast",
		CallbackToolName: "agent_callback",
		CallbackType:     callbackType,
		Endpoint:         "http://example.test/callback",
	}
}

func testMessage(id string, bySpec message.Spec) *message.Message {
	now := time.Now().UnixMilli()
	return &message.Message{
		ID:           id,
		To:           "agent-a",
		From:         "mcp-server",
		FromSpec:     bySpec,
		ToSpec:       message.SpecOmni,
		RequestType:  message.RequestTypeQuery,
		ShouldReply:  true,
		Prompt:       "callback payload",
		Refs:         `{"source":"test"}`,
		Status:       message.StatusDelivered,
		DeliveryTime: &now,
		SentTime:     now,
	}
}

func testPayload(messageID string) CallbackPayload {
	now := time.Now().UnixMilli()
	return CallbackPayload{
		ServerID:         "mcp-server",
		CallbackToolName: "agent_callback",
		MessageID:        messageID,
		Prompt:           "callback payload",
		Refs:             json.RawMessage(`{"source":"test"}`),
		Status:           string(message.StatusDelivered),
		DeliveryTime:     &now,
	}
}
