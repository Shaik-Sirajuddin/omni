package operator_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Shaik-Sirajuddin/omni/omniagent"
	"github.com/Shaik-Sirajuddin/omni/operator"
	operatorstore "github.com/Shaik-Sirajuddin/omni/store/operator"
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

func newTestStore(t *testing.T) operatorstore.OperatorStore {
	t.Helper()
	return operatorstore.NewWithDB(newTestDB(t))
}

func TestOperatorStore_CreateAndGetWorkspace(t *testing.T) {
	store := newTestStore(t)

	ws := &operator.TeamInfo{ID: "ws-1", Name: "my-workspace", WorkspaceDir: "/projects/ws"}
	require.NoError(t, store.CreateWorkspace(ws))

	got, err := store.GetWorkspace("ws-1")
	require.NoError(t, err)
	assert.Equal(t, "ws-1", got.ID)
	assert.Equal(t, "my-workspace", got.Name)
	assert.Equal(t, "/projects/ws", got.WorkspaceDir)
	assert.Equal(t, 0, got.Agents)
}

func TestOperatorStore_WorkspaceByDir(t *testing.T) {
	store := newTestStore(t)

	ws := &operator.TeamInfo{ID: "ws-2", Name: "by-dir", WorkspaceDir: "/projects/dir"}
	require.NoError(t, store.CreateWorkspace(ws))

	got, err := store.WorkspaceByDir(sandbox.WorkspaceDir("/projects/dir"))
	require.NoError(t, err)
	assert.Equal(t, "ws-2", got.ID)
}

func TestOperatorStore_WorkspaceByDir_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.WorkspaceByDir(sandbox.WorkspaceDir("/nonexistent"))
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestOperatorStore_ListWorkspaces(t *testing.T) {
	store := newTestStore(t)

	for _, id := range []string{"w1", "w2", "w3"} {
		require.NoError(t, store.CreateWorkspace(&operator.TeamInfo{
			ID:           id,
			Name:         id,
			WorkspaceDir: "/ws/" + id,
		}))
	}

	teams, err := store.ListWorkspaces()
	require.NoError(t, err)
	assert.Len(t, teams, 3)
}

func TestOperatorStore_DeleteWorkspace(t *testing.T) {
	store := newTestStore(t)

	require.NoError(t, store.CreateWorkspace(&operator.TeamInfo{ID: "del-ws", Name: "delete-me", WorkspaceDir: "/del"}))
	require.NoError(t, store.DeleteWorkspace("del-ws"))

	_, err := store.GetWorkspace("del-ws")
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestOperatorStore_DeleteWorkspace_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.DeleteWorkspace("nonexistent")
	require.Error(t, err)
}

func TestOperatorStore_CreateAndGetAgent(t *testing.T) {
	store := newTestStore(t)

	agent := &omniagent.AgentInfo{
		ID:           "ag-1",
		Name:         "my-agent",
		WorkspaceDir: sandbox.WorkspaceDir("/ws/proj"),
		MemoryDir:    "/mem/my-agent",
	}
	require.NoError(t, store.CreateAgent(agent))

	got, err := store.GetAgent("ag-1")
	require.NoError(t, err)
	assert.Equal(t, "ag-1", got.ID)
	assert.Equal(t, "my-agent", got.Name)
	assert.Equal(t, sandbox.WorkspaceDir("/ws/proj"), got.WorkspaceDir)
}

func TestOperatorStore_ListAgentsByDir(t *testing.T) {
	store := newTestStore(t)

	for _, name := range []string{"alpha", "beta"} {
		require.NoError(t, store.CreateAgent(&omniagent.AgentInfo{
			ID: name, Name: name, WorkspaceDir: "/ws/shared", MemoryDir: "/mem/" + name,
		}))
	}
	require.NoError(t, store.CreateAgent(&omniagent.AgentInfo{
		ID: "other", Name: "other", WorkspaceDir: "/ws/other", MemoryDir: "/mem/other",
	}))

	agents, err := store.ListAgentsByDir(sandbox.WorkspaceDir("/ws/shared"))
	require.NoError(t, err)
	require.Len(t, agents, 2)
	names := []string{agents[0].Name, agents[1].Name}
	assert.ElementsMatch(t, []string{"alpha", "beta"}, names)
}

func TestOperatorStore_DeleteAgent(t *testing.T) {
	store := newTestStore(t)

	require.NoError(t, store.CreateAgent(&omniagent.AgentInfo{
		ID: "del-ag", Name: "del", WorkspaceDir: "/ws", MemoryDir: "/mem/del",
	}))
	require.NoError(t, store.DeleteAgent("del-ag"))

	_, err := store.GetAgent("del-ag")
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestOperatorStore_WorkspaceAgentCount(t *testing.T) {
	store := newTestStore(t)

	require.NoError(t, store.CreateWorkspace(&operator.TeamInfo{ID: "ws-count", Name: "count", WorkspaceDir: "/ws/count"}))
	for i, name := range []string{"a1", "a2"} {
		_ = i
		require.NoError(t, store.CreateAgent(&omniagent.AgentInfo{
			ID: name, Name: name, WorkspaceDir: "/ws/count", MemoryDir: "/mem/" + name,
		}))
	}

	ws, err := store.GetWorkspace("ws-count")
	require.NoError(t, err)
	assert.Equal(t, 2, ws.Agents)
}
