package codesession_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Shaik-Sirajuddin/omni/connector/codeagent"
	"github.com/Shaik-Sirajuddin/omni/omniagent"
	"github.com/Shaik-Sirajuddin/omni/store/codesession"
	"github.com/Shaik-Sirajuddin/omni/store/database"
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

func TestCodeSessionStore_CreateAndGet(t *testing.T) {
	db := newTestDB(t)
	agentID := seedAgent(t, db, "agent-1", "/ws")
	store := codesession.NewWithDB(db)

	session := &omniagent.CodeSession{
		Id:       "sess-1",
		Model:    &codeagent.Model{Provider: "claude", Model: "sonnet"},
		Idx:      0,
		IsActive: true,
		Prompts:  0,
	}
	require.NoError(t, store.CreateSession(agentID, session))

	got, err := store.GetSession(agentID)
	require.NoError(t, err)
	assert.Equal(t, "sess-1", got.Id)
	assert.True(t, got.IsActive)
	assert.Equal(t, "claude", string(got.Model.Provider))
	assert.Equal(t, "sonnet", got.Model.Model)
}

func TestCodeSessionStore_UpdateSession(t *testing.T) {
	db := newTestDB(t)
	agentID := seedAgent(t, db, "agent-2", "/ws")
	store := codesession.NewWithDB(db)

	session := &omniagent.CodeSession{Id: "sess-2", IsActive: true, Prompts: 1}
	require.NoError(t, store.CreateSession(agentID, session))

	session.Prompts = 5
	session.IsActive = false
	require.NoError(t, store.UpdateSession(agentID, session))

	sessions, err := store.ListSessions(agentID, nil)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, 5, sessions[0].Prompts)
	assert.False(t, sessions[0].IsActive)
}

func TestCodeSessionStore_UpdateSession_NotFound(t *testing.T) {
	db := newTestDB(t)
	agentID := seedAgent(t, db, "agent-3", "/ws")
	store := codesession.NewWithDB(db)

	err := store.UpdateSession(agentID, &omniagent.CodeSession{Id: "nonexistent"})
	require.Error(t, err)
}

func TestCodeSessionStore_ListSessions_Filter(t *testing.T) {
	db := newTestDB(t)
	agentID := seedAgent(t, db, "agent-4", "/ws")
	store := codesession.NewWithDB(db)

	require.NoError(t, store.CreateSession(agentID, &omniagent.CodeSession{Id: "s1", IsActive: true}))
	require.NoError(t, store.CreateSession(agentID, &omniagent.CodeSession{Id: "s2", IsActive: false}))

	active, err := store.ListSessions(agentID, &omniagent.CodeSession{IsActive: true})
	require.NoError(t, err)
	assert.Len(t, active, 1)
	assert.Equal(t, "s1", active[0].Id)

	all, err := store.ListSessions(agentID, nil)
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestCodeSessionStore_GetSession_NoActive(t *testing.T) {
	db := newTestDB(t)
	agentID := seedAgent(t, db, "agent-5", "/ws")
	store := codesession.NewWithDB(db)

	_, err := store.GetSession(agentID)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

// seedAgent inserts a minimal agent row so FK constraints are satisfied.
func seedAgent(t *testing.T, db *sql.DB, name, wsDir string) string {
	t.Helper()
	id := "agent-" + name
	_, err := db.Exec(
		`INSERT INTO agents (id, name, workspace_dir, memory_dir) VALUES (?, ?, ?, ?)`,
		id, name, wsDir, "/mem/"+name,
	)
	require.NoError(t, err)
	return id
}
