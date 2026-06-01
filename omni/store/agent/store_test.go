package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Shaik-Sirajuddin/omni/connector/codeagent"
	"github.com/Shaik-Sirajuddin/omni/omniagent"
	agentstore "github.com/Shaik-Sirajuddin/omni/store/agent"
	"github.com/Shaik-Sirajuddin/omni/store/codesession"
	"github.com/Shaik-Sirajuddin/omni/store/database"
	sandbox "github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, database.ApplySchema(db))
	return db
}

func newTestStore(t *testing.T) (agentstore.AgentStore, *sql.DB) {
	t.Helper()
	db := newTestDB(t)
	sessions := codesession.NewWithDB(db)
	return agentstore.NewWithDB(db, sessions), db
}

func TestAgentStore_CreateAndGet(t *testing.T) {
	store, _ := newTestStore(t)

	agent := &omniagent.Data{
		Info: &omniagent.AgentInfo{
			ID:           "a1",
			Name:         "test-agent",
			WorkspaceDir: sandbox.WorkspaceDir("/ws/proj"),
			MemoryDir:    "/mem/test-agent",
		},
	}
	require.NoError(t, store.Create(agent))

	got, err := store.GetAgent("a1")
	require.NoError(t, err)
	assert.Equal(t, "a1", got.Info.ID)
	assert.Equal(t, "test-agent", got.Info.Name)
	assert.Equal(t, sandbox.WorkspaceDir("/ws/proj"), got.Info.WorkspaceDir)
	assert.Equal(t, "/mem/test-agent", got.Info.MemoryDir)
}

func TestAgentStore_Save_UpdatesExisting(t *testing.T) {
	store, _ := newTestStore(t)

	agent := &omniagent.Data{
		Info: &omniagent.AgentInfo{ID: "a2", Name: "original", WorkspaceDir: "/ws", MemoryDir: "/mem/a2"},
	}
	require.NoError(t, store.Create(agent))

	agent.Info.Name = "updated"
	require.NoError(t, store.Save(agent))

	got, err := store.GetAgent("a2")
	require.NoError(t, err)
	assert.Equal(t, "updated", got.Info.Name)
}

func TestAgentStore_ListAgents(t *testing.T) {
	store, _ := newTestStore(t)

	for _, name := range []string{"alpha", "beta"} {
		require.NoError(t, store.Create(&omniagent.Data{
			Info: &omniagent.AgentInfo{ID: name, Name: name, WorkspaceDir: "/ws/shared", MemoryDir: "/mem/" + name},
		}))
	}
	require.NoError(t, store.Create(&omniagent.Data{
		Info: &omniagent.AgentInfo{ID: "other", Name: "other", WorkspaceDir: "/ws/other", MemoryDir: "/mem/other"},
	}))

	resp := store.ListAgents(agentstore.ListAgentParams{Workspace: "/ws/shared"})
	require.Len(t, resp.Agents, 2)
	names := []string{resp.Agents[0].Info.Name, resp.Agents[1].Info.Name}
	assert.ElementsMatch(t, []string{"alpha", "beta"}, names)
}

func TestAgentStore_DeleteAgent(t *testing.T) {
	store, _ := newTestStore(t)

	require.NoError(t, store.Create(&omniagent.Data{
		Info: &omniagent.AgentInfo{ID: "del-1", Name: "bye", WorkspaceDir: "/ws", MemoryDir: "/mem/bye"},
	}))
	require.NoError(t, store.DeleteAgent("del-1"))

	_, err := store.GetAgent("del-1")
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestAgentStore_DeleteAgent_NotFound(t *testing.T) {
	store, _ := newTestStore(t)
	err := store.DeleteAgent("nonexistent")
	require.Error(t, err)
}

func TestAgentStore_GetSettings_DefaultAfterCreate(t *testing.T) {
	store, _ := newTestStore(t)

	require.NoError(t, store.Create(&omniagent.Data{
		Info: &omniagent.AgentInfo{ID: "s1", Name: "settings-test", WorkspaceDir: "/ws", MemoryDir: "/mem"},
	}))

	settings, err := store.GetSettings("s1")
	require.NoError(t, err)
	assert.Nil(t, settings.DefaultModel)
	assert.Nil(t, settings.Sandbox)
}

func TestAgentStore_UpdateSettings(t *testing.T) {
	store, _ := newTestStore(t)

	require.NoError(t, store.Create(&omniagent.Data{
		Info: &omniagent.AgentInfo{ID: "s2", Name: "settings-update", WorkspaceDir: "/ws", MemoryDir: "/mem"},
	}))

	updated := &omniagent.Settings{
		DefaultModel: &codeagent.Model{Provider: "claude", Model: "opus"},
	}
	require.NoError(t, store.UpdateSettings("s2", updated))

	got, err := store.GetSettings("s2")
	require.NoError(t, err)
	require.NotNil(t, got.DefaultModel)
	assert.Equal(t, "claude", string(got.DefaultModel.Provider))
	assert.Equal(t, "opus", got.DefaultModel.Model)
}

func TestAgentStore_Session_CreateAndGet(t *testing.T) {
	store, _ := newTestStore(t)

	require.NoError(t, store.Create(&omniagent.Data{
		Info: &omniagent.AgentInfo{ID: "ag1", Name: "session-agent", WorkspaceDir: "/ws", MemoryDir: "/mem"},
	}))

	session := &omniagent.CodeSession{
		Id:       "session-1",
		IsActive: true,
		Prompts:  3,
		Model:    &codeagent.Model{Provider: "gemini", Model: "flash"},
	}
	require.NoError(t, store.CreateSession("ag1", session))

	got, err := store.GetActiveSession("ag1")
	require.NoError(t, err)
	assert.Equal(t, "session-1", got.Id)
	assert.True(t, got.IsActive)
	assert.Equal(t, 3, got.Prompts)
}

func TestAgentStore_Session_Update(t *testing.T) {
	store, _ := newTestStore(t)

	require.NoError(t, store.Create(&omniagent.Data{
		Info: &omniagent.AgentInfo{ID: "ag2", Name: "session-update", WorkspaceDir: "/ws", MemoryDir: "/mem"},
	}))

	session := &omniagent.CodeSession{Id: "s-upd", IsActive: true, Prompts: 1}
	require.NoError(t, store.CreateSession("ag2", session))

	session.Prompts = 10
	session.LastSyncPrompt = 8
	require.NoError(t, store.UpdateActiveSession("ag2", session))

	got, err := store.GetActiveSession("ag2")
	require.NoError(t, err)
	assert.Equal(t, 10, got.Prompts)
	assert.Equal(t, 8, got.LastSyncPrompt)
}
