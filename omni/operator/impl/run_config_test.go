package impl

import (
	"testing"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/omniagent"
	operator "github.com/Shaik-Sirajuddin/memory/operator"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
	agentstore "github.com/Shaik-Sirajuddin/memory/store/agent"
	"github.com/Shaik-Sirajuddin/memory/store/codesession"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestOperatorWithAgentStore returns a test operator that has an agentStore wired in
// so RunConfig persistence can be tested.
func newTestOperatorWithAgentStore(t *testing.T) (*DefaultOperator, agentstore.AgentStore) {
	t.Helper()
	db := newTestDB(t)
	as := agentstore.NewWithDB(db, codesession.NewWithDB(db))
	op := newTestOperator(t, false)
	op.agentStore = as
	return op, as
}

// ---- mergeRunConfig unit tests ----

func TestMergeRunConfig_AdditiveArgs(t *testing.T) {
	stored := &omniagent.RunConfig{ExtraArgs: []string{"--foo"}}
	override := &omniagent.RunConfig{ExtraArgs: []string{"--bar"}}

	out := mergeRunConfig(stored, override, false, false)
	require.NotNil(t, out)
	assert.Equal(t, []string{"--foo", "--bar"}, out.ExtraArgs)
}

func TestMergeRunConfig_ClearArgsReplacesStored(t *testing.T) {
	stored := &omniagent.RunConfig{ExtraArgs: []string{"--old"}}
	override := &omniagent.RunConfig{ExtraArgs: []string{"--new"}}

	out := mergeRunConfig(stored, override, true, false)
	require.NotNil(t, out)
	assert.Equal(t, []string{"--new"}, out.ExtraArgs)
}

func TestMergeRunConfig_EnvUpsert(t *testing.T) {
	stored := &omniagent.RunConfig{Envs: map[string]string{"K": "old", "X": "1"}}
	override := &omniagent.RunConfig{Envs: map[string]string{"K": "new", "Y": "2"}}

	out := mergeRunConfig(stored, override, false, false)
	require.NotNil(t, out)
	assert.Equal(t, "new", out.Envs["K"])
	assert.Equal(t, "1", out.Envs["X"])
	assert.Equal(t, "2", out.Envs["Y"])
}

func TestMergeRunConfig_ClearEnvsReplacesStored(t *testing.T) {
	stored := &omniagent.RunConfig{Envs: map[string]string{"K": "old"}}
	override := &omniagent.RunConfig{Envs: map[string]string{"NEW": "val"}}

	out := mergeRunConfig(stored, override, false, true)
	require.NotNil(t, out)
	_, hasOld := out.Envs["K"]
	assert.False(t, hasOld, "cleared env key should not be present")
	assert.Equal(t, "val", out.Envs["NEW"])
}

func TestMergeRunConfig_BothNilReturnsNil(t *testing.T) {
	assert.Nil(t, mergeRunConfig(nil, nil, false, false))
}

func TestMergeRunConfig_NoOverrideReturnsCopy(t *testing.T) {
	stored := &omniagent.RunConfig{ExtraArgs: []string{"--a"}, Envs: map[string]string{"E": "v"}}
	out := mergeRunConfig(stored, nil, false, false)
	require.NotNil(t, out)
	assert.Equal(t, stored.ExtraArgs, out.ExtraArgs)
	assert.Equal(t, stored.Envs, out.Envs)
}

// ---- CreateAgent persists RunConfig ----

func TestCreateAgent_PersistsRunConfig(t *testing.T) {
	op, as := newTestOperatorWithAgentStore(t)
	workspace := sandbox.WorkspaceDir(t.TempDir())

	rc := &omniagent.RunConfig{
		ExtraArgs: []string{"--dangerously-skip-permissions"},
		Envs:      map[string]string{"MY_VAR": "hello"},
	}
	err := op.CreateAgent(operator.CreateAgentParams{
		Workspace: workspace,
		Name:      "rc-create-test",
		RunConfig: rc,
	})
	require.NoError(t, err)

	agents, err := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
	require.NoError(t, err)
	require.Len(t, agents.Agents, 1)

	settings, err := as.GetSettings(agents.Agents[0].ID)
	require.NoError(t, err)
	require.NotNil(t, settings.RunConfig)
	assert.Equal(t, rc.ExtraArgs, settings.RunConfig.ExtraArgs)
	assert.Equal(t, rc.Envs, settings.RunConfig.Envs)
}

// ---- stubCodeAgent captures ExtraArgs ----

type capturingCodeAgent struct {
	stubCodeAgent
	lastCreateArgs []string
	lastResumeArgs []string
	lastCreateEnvs []string
	lastResumeEnvs []string
}

func (a *capturingCodeAgent) Create(p codeagent.CreateSessionParams) (*codeagent.CreateSessionResult, error) {
	a.lastCreateArgs = p.ExtraArgs
	a.lastCreateEnvs = p.Envs
	return &codeagent.CreateSessionResult{ID: "sess-1"}, nil
}

func (a *capturingCodeAgent) Resume(p codeagent.ResumeSessionParams) (*codeagent.ResumeSessionResult, error) {
	a.lastResumeArgs = p.ExtraArgs
	a.lastResumeEnvs = p.Envs
	return &codeagent.ResumeSessionResult{SessionID: p.ID}, nil
}

func newCapturingOperator(t *testing.T) (*DefaultOperator, agentstore.AgentStore, *capturingCodeAgent) {
	t.Helper()
	db := newTestDB(t)
	as := agentstore.NewWithDB(db, codesession.NewWithDB(db))
	ca := &capturingCodeAgent{}
	op := newTestOperator(t, false)
	op.agentStore = as
	op.newCodeAgent = func(_ codeagent.Provider, _, _ string) (codeagent.CodeAgent, error) {
		return ca, nil
	}
	return op, as, ca
}

func TestCreateAgent_ExtraArgsReachConnector(t *testing.T) {
	op, _, ca := newCapturingOperator(t)
	workspace := sandbox.WorkspaceDir(t.TempDir())

	rc := &omniagent.RunConfig{
		ExtraArgs: []string{"--output-format=json"},
		Envs:      map[string]string{"FOO": "bar"},
	}
	require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
		Workspace: workspace,
		Name:      "cap-create",
		RunConfig: rc,
		Interactive: false,
	}))

	assert.Equal(t, []string{"--output-format=json"}, ca.lastCreateArgs)
	// Env should contain FOO=bar somewhere in the slice.
	assert.Contains(t, ca.lastCreateEnvs, "FOO=bar")
}

func TestResumeAgent_StoredRunConfigReachesConnector(t *testing.T) {
	op, as, ca := newCapturingOperator(t)
	workspace := sandbox.WorkspaceDir(t.TempDir())

	// Init agent with RunConfig.
	rc := &omniagent.RunConfig{
		ExtraArgs: []string{"--dangerously-skip-permissions"},
		Envs:      map[string]string{"K": "V"},
	}
	require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
		Workspace:   workspace,
		Name:        "cap-resume",
		RunConfig:   rc,
		Interactive: false,
	}))

	agents, err := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
	require.NoError(t, err)
	require.Len(t, agents.Agents, 1)

	// Verify settings were persisted.
	settings, err := as.GetSettings(agents.Agents[0].ID)
	require.NoError(t, err)
	require.NotNil(t, settings.RunConfig)

	// Resume with no overrides — stored args must arrive at connector.
	require.NoError(t, op.ResumeAgent(operator.ResumeAgentParams{
		Workspace: workspace,
		Name:      "cap-resume",
	}))

	assert.Equal(t, []string{"--dangerously-skip-permissions"}, ca.lastResumeArgs)
	assert.Contains(t, ca.lastResumeEnvs, "K=V")
}

func TestResumeAgent_ClearArgsThenAddNew(t *testing.T) {
	op, _, ca := newCapturingOperator(t)
	workspace := sandbox.WorkspaceDir(t.TempDir())

	require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
		Workspace:   workspace,
		Name:        "cap-clear",
		RunConfig:   &omniagent.RunConfig{ExtraArgs: []string{"--old-flag"}},
		Interactive: false,
	}))

	require.NoError(t, op.ResumeAgent(operator.ResumeAgentParams{
		Workspace: workspace,
		Name:      "cap-clear",
		RunConfig: &omniagent.RunConfig{ExtraArgs: []string{"--new-flag"}},
		ClearArgs: true,
	}))

	assert.Equal(t, []string{"--new-flag"}, ca.lastResumeArgs,
		"after ClearArgs, only the new flag should reach the connector")
}

func TestResumeAgent_NoOverrideIsNoop(t *testing.T) {
	op, as, _ := newCapturingOperator(t)
	workspace := sandbox.WorkspaceDir(t.TempDir())

	rc := &omniagent.RunConfig{ExtraArgs: []string{"--stable"}, Envs: map[string]string{"E": "1"}}
	require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
		Workspace:   workspace,
		Name:        "cap-noop",
		RunConfig:   rc,
		Interactive: false,
	}))

	agents, _ := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
	before, _ := as.GetSettings(agents.Agents[0].ID)

	// Resume with no RunConfig override.
	require.NoError(t, op.ResumeAgent(operator.ResumeAgentParams{
		Workspace: workspace,
		Name:      "cap-noop",
	}))

	after, _ := as.GetSettings(agents.Agents[0].ID)
	// RunConfig should be unchanged in the store.
	assert.Equal(t, before.RunConfig, after.RunConfig)
}
