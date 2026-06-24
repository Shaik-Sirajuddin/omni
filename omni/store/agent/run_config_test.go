package store_test

import (
	"testing"

	"github.com/Shaik-Sirajuddin/memory/omniagent"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedAgent(t *testing.T, id, name string) *omniagent.Data {
	t.Helper()
	return &omniagent.Data{
		Info: &omniagent.AgentInfo{
			ID:           id,
			Name:         name,
			WorkspaceDir: sandbox.WorkspaceDir("/ws/test"),
			MemoryDir:    "/mem/" + name,
		},
	}
}

// RunConfig round-trips through GetSettings / UpdateSettings correctly.
func TestRunConfig_StoreRoundTrip(t *testing.T) {
	store, _ := newTestStore(t)

	require.NoError(t, store.Create(seedAgent(t, "rc1", "rc-agent-1")))

	rc := &omniagent.RunConfig{
		ExtraArgs: []string{"--dangerously-skip-permissions", "--output-format=json"},
		Envs:      map[string]string{"MY_VAR": "hello", "DEBUG": "1"},
	}
	require.NoError(t, store.UpdateSettings("rc1", &omniagent.Settings{RunConfig: rc}))

	settings, err := store.GetSettings("rc1")
	require.NoError(t, err)
	require.NotNil(t, settings.RunConfig)
	assert.Equal(t, rc.ExtraArgs, settings.RunConfig.ExtraArgs)
	assert.Equal(t, rc.Envs, settings.RunConfig.Envs)
}

// Nil RunConfig is stored as '{}' and returns nil on read.
func TestRunConfig_NilIsNoOp(t *testing.T) {
	store, _ := newTestStore(t)

	require.NoError(t, store.Create(seedAgent(t, "rc2", "rc-agent-2")))
	require.NoError(t, store.UpdateSettings("rc2", &omniagent.Settings{}))

	settings, err := store.GetSettings("rc2")
	require.NoError(t, err)
	assert.Nil(t, settings.RunConfig)
}

// Updating RunConfig replaces the previous value.
func TestRunConfig_Update(t *testing.T) {
	store, _ := newTestStore(t)

	require.NoError(t, store.Create(seedAgent(t, "rc3", "rc-agent-3")))
	require.NoError(t, store.UpdateSettings("rc3", &omniagent.Settings{
		RunConfig: &omniagent.RunConfig{ExtraArgs: []string{"--old"}},
	}))
	require.NoError(t, store.UpdateSettings("rc3", &omniagent.Settings{
		RunConfig: &omniagent.RunConfig{ExtraArgs: []string{"--new"}, Envs: map[string]string{"K": "V"}},
	}))

	settings, err := store.GetSettings("rc3")
	require.NoError(t, err)
	require.NotNil(t, settings.RunConfig)
	assert.Equal(t, []string{"--new"}, settings.RunConfig.ExtraArgs)
	assert.Equal(t, map[string]string{"K": "V"}, settings.RunConfig.Envs)
}
