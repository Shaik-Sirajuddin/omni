package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	provider "github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSandboxStore(t *testing.T) {
	t.Run("CreateListGetUpdate", func(t *testing.T) {
		store := newTestSandboxStore(t, "store-test")
		sbx := &provider.Sandbox{
			Config: &provider.Config{
				AgentPolicy: &provider.Policy{
					FSPolicy: provider.FSPolicy(provider.Inherit),
					Config: provider.MountConfig{
						AccessDirs: []string{"/tmp/cache"},
					},
				},
			},
			State: &provider.State{PID: "sandbox-1", Active: true},
			Data:  &provider.Data{ID: "sandbox-1", Application: "store-test", CreatedAt: "2026-04-17T00:00:00Z"},
		}

		require.NoError(t, store.Create(sbx), "Creating a sandbox in the store should not return an error")

		listed, err := store.List()
		require.NoError(t, err, "Listing sandboxes from the store should not return an error")
		require.Len(t, listed, 1, "Listing sandboxes should return the stored sandbox")
		assert.Equal(t, provider.FSPolicy(provider.Inherit), listed[0].Config.AgentPolicy.FSPolicy, "Stored sandbox config should be restored from yaml")

		pid := "sandbox-1"
		got, err := store.Get(&provider.GetSandboxParams{PID: &pid, Active: true})
		require.NoError(t, err, "Getting a sandbox from the store should not return an error")
		assert.Equal(t, "sandbox-1", got.Data.ID, "Fetched sandbox should match the stored sandbox")

		sbx.Config.AgentPolicy.FSPolicy = provider.FSPolicy(provider.PermissiveRead)
		require.NoError(t, store.Update(sbx), "Updating a sandbox in the store should not return an error")

		got, err = store.Get(&provider.GetSandboxParams{PID: &pid, Active: true})
		require.NoError(t, err, "Getting an updated sandbox from the store should not return an error")
		assert.Equal(t, provider.FSPolicy(provider.PermissiveRead), got.Config.AgentPolicy.FSPolicy, "Updated sandbox config should be restored from yaml")
	})

	t.Run("DuplicateCreate", func(t *testing.T) {
		store := newTestSandboxStore(t, "store-duplicate")
		sbx := &provider.Sandbox{
			Config: &provider.Config{},
			State:  &provider.State{PID: "sandbox-1", Active: true},
			Data:   &provider.Data{ID: "sandbox-1", Application: "store-duplicate", CreatedAt: "2026-04-17T00:00:00Z"},
		}

		require.NoError(t, store.Create(sbx), "Creating the first sandbox should not return an error")
		require.Error(t, store.Create(sbx), "Creating a duplicate sandbox id should return an error")
	})
}

func newTestSandboxStore(t *testing.T, application string) *sqlSandboxStore {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "sandbox.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err, "Opening a temp sqlite database should not return an error")
	t.Cleanup(func() {
		require.NoError(t, db.Close(), "Closing the temp sqlite database should not return an error")
	})

	require.NoError(t, applySchema(db), "Applying sandbox schema should not return an error")

	return &sqlSandboxStore{
		db:        db,
		info:      Info{Application: application},
		configDir: t.TempDir(),
	}
}
