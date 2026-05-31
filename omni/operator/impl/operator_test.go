package impl

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
	operator "github.com/Shaik-Sirajuddin/memory/operator"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
	"github.com/Shaik-Sirajuddin/memory/store/codesession"
	operatorstore "github.com/Shaik-Sirajuddin/memory/store/operator"
	ptyclients "github.com/Shaik-Sirajuddin/memory/svc/ptydaemon/clients"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

const testAgentSchema = `
CREATE TABLE IF NOT EXISTS agents (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    workspace_dir TEXT NOT NULL,
    memory_dir    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_settings (
    agent_id               TEXT PRIMARY KEY,
    sandbox                TEXT NOT NULL DEFAULT '{}',
    default_model_provider TEXT NOT NULL DEFAULT '',
    default_model_name     TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (agent_id) REFERENCES agents(id)
);`

const testWorkspaceSchema = `
CREATE TABLE IF NOT EXISTS workspaces (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    workspace_dir TEXT NOT NULL UNIQUE
);`

const testCodeSessionSchema = `
CREATE TABLE IF NOT EXISTS code_sessions (
    id               TEXT PRIMARY KEY,
    agent_id         TEXT NOT NULL,
    model_provider   TEXT NOT NULL DEFAULT '',
    model_name       TEXT NOT NULL DEFAULT '',
    idx              INTEGER NOT NULL DEFAULT 0,
    is_active        INTEGER NOT NULL DEFAULT 0,
    prompts          INTEGER NOT NULL DEFAULT 0,
    last_sync_prompt INTEGER NOT NULL DEFAULT 0,
    status           TEXT NOT NULL DEFAULT 'ready',
    stop_reason      TEXT NOT NULL DEFAULT '',
    is_interrupted   INTEGER NOT NULL DEFAULT 0,
    tokens_input     INTEGER NOT NULL DEFAULT 0,
    tokens_output    INTEGER NOT NULL DEFAULT 0,
    tokens_cached_input INTEGER NOT NULL DEFAULT 0,
    tokens_cached_output INTEGER NOT NULL DEFAULT 0,
    tokens_max       INTEGER NOT NULL DEFAULT 0,
    tokens_consumed_percent REAL NOT NULL DEFAULT 0
);`

func TestStoreSchemaIncludesRequiredTables(t *testing.T) {
	db := newTestDB(t)

	assert.True(t, tableExists(t, db, "agents"), "Agents table should exist in the temp sqlite store")
	assert.True(t, tableExists(t, db, "agent_settings"), "Agent settings table should exist in the temp sqlite store")
	assert.True(t, tableExists(t, db, "workspaces"), "Workspaces table should exist in the temp sqlite store")
}

func TestCreateAgent(t *testing.T) {
	t.Run("MemoryEnabledCreatesTemplateFiles", func(t *testing.T) {
		op := newTestOperator(t, true)
		workspace := sandbox.WorkspaceDir(t.TempDir())

		err := op.CreateAgent(operator.CreateAgentParams{Workspace: workspace, Name: "operator-a", Interactive: true})
		require.NoError(t, err, "CreateAgent should succeed when memory is enabled")

		result, err := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
		require.NoError(t, err, "ListCodeAgents should resolve the created agent")
		require.NotEmpty(t, result.Agents, "Created agent should be stored")

		assert.DirExists(t, result.Agents[0].MemoryDir, "Agent memory directory should exist")
		assert.Equal(t, "operator-a", result.Agents[0].Name, "Requested agent name should be preserved")
		assert.Equal(t, filepath.Join(string(workspace), "memory", "agents", result.Agents[0].Name), result.Agents[0].MemoryDir, "Agent memory directory should follow the new layout")
		_, memoryStatErr := os.Stat(filepath.Join(result.Agents[0].MemoryDir, "entry", "data", "memory.yaml"))
		assert.ErrorIs(t, memoryStatErr, os.ErrNotExist, "Per-agent memory.yaml should not be created")
		_, semanticsStatErr := os.Stat(filepath.Join(result.Agents[0].MemoryDir, "entry", "data", "semantics.yaml"))
		assert.ErrorIs(t, semanticsStatErr, os.ErrNotExist, "Per-agent semantics.yaml should not be created")
		assert.DirExists(t, filepath.Join(result.Agents[0].MemoryDir, "generated"), "Generated directory should be created")
		assert.DirExists(t, filepath.Join(result.Agents[0].MemoryDir, "state"), "State directory should be created")
		assert.DirExists(t, filepath.Join(result.Agents[0].MemoryDir, "entry", "tasks"), "entry/tasks directory should be created")
		assert.FileExists(t, filepath.Join(string(workspace), "memory", "memory.yaml"), "Workspace memory.yaml should be created instead")
		assert.FileExists(t, filepath.Join(string(workspace), "memory", "agent_"+result.Agents[0].Name+".md"), "Workspace should include the agent entry markdown")
		assert.FileExists(t, filepath.Join(string(workspace), "memory", "team", "entry", "tasks", result.Agents[0].Name, "default.yaml"), "Collab tasks dir should be seeded for the agent")

		workspaces, err := op.ListWorkspaces(operator.ListWorkspacesParams{})
		require.NoError(t, err, "ListWorkspaces should succeed after agent creation")
		require.Len(t, workspaces.Teams, 1, "Exactly one workspace should be created")
		assert.Equal(t, 1, workspaces.Teams[0].Agents, "Workspace agent count should reflect the inserted agent")

		workspaceData, err := op.GetWorkspace(operator.GetWorkSpaceParams{ID: workspaces.Teams[0].ID})
		require.NoError(t, err, "GetWorkspace should return the created workspace")
		require.NotNil(t, workspaceData.Info, "Workspace info should be populated")
		assert.Len(t, workspaceData.Agents, 1, "Workspace lookup should include the created agent")
	})

	t.Run("MemoryDisabledSkipsTemplateFiles", func(t *testing.T) {
		op := newTestOperator(t, false)
		workspace := sandbox.WorkspaceDir(t.TempDir())

		err := op.CreateAgent(operator.CreateAgentParams{Workspace: workspace, Name: "operator-b"})
		require.NoError(t, err, "CreateAgent should still persist the agent when memory is disabled")

		result, err := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
		require.NoError(t, err, "ListCodeAgents should resolve the stored agent")
		require.NotEmpty(t, result.Agents, "Created agent should be stored")

		_, statErr := os.Stat(result.Agents[0].MemoryDir)
		assert.ErrorIs(t, statErr, os.ErrNotExist, "Agent memory directory should not be created when memory is disabled")
	})

	t.Run("AutoInitialisesMemoryInCurrentWorkspaceWhenNoMemoryExists", func(t *testing.T) {
		op := newTestOperator(t, true)
		cwd := t.TempDir()

		prev, err := os.Getwd()
		require.NoError(t, err, "Current working directory lookup should succeed")
		require.NoError(t, os.Chdir(cwd), "Changing to working directory should succeed")
		t.Cleanup(func() {
			require.NoError(t, os.Chdir(prev), "Working directory should be restored")
		})

		err = op.CreateAgent(operator.CreateAgentParams{Name: "operator-c"})
		require.NoError(t, err, "CreateAgent should auto-init memory in the current workspace")

		result, err := op.ListCodeAgents(operator.GetCodeAgentsParams{})
		require.NoError(t, err, "ListCodeAgents should default to resolved workspace")
		require.NotEmpty(t, result.Agents, "Stored agent should be returned")
		assert.Equal(t, sandbox.WorkspaceDir(cwd), result.Agents[0].WorkspaceDir, "Workspace should default to the current directory when no memory root exists")
		assert.FileExists(t, filepath.Join(cwd, "memory", "memory.yaml"), "Workspace memory root should be created automatically")
		assert.FileExists(t, filepath.Join(cwd, "memory", "agent_operator-c.md"), "Workspace agent markdown should be created")
	})

	t.Run("ResolvesWorkspaceFromNearestAncestorMemory", func(t *testing.T) {
		op := newTestOperator(t, true)
		workspace := t.TempDir()
		require.NoError(t, op.TeamInit(operator.TeamInitParams{Workspace: sandbox.WorkspaceDir(workspace)}), "TeamInit should prepare the ancestor workspace")

		nested := filepath.Join(workspace, "nested", "child")
		require.NoError(t, os.MkdirAll(nested, 0o755), "Nested working directory creation should succeed")

		prev, err := os.Getwd()
		require.NoError(t, err, "Current working directory lookup should succeed")
		require.NoError(t, os.Chdir(nested), "Changing to nested working directory should succeed")
		t.Cleanup(func() {
			require.NoError(t, os.Chdir(prev), "Working directory should be restored")
		})

		err = op.CreateAgent(operator.CreateAgentParams{Name: "operator-d"})
		require.NoError(t, err, "CreateAgent should resolve to the nearest ancestor with a memory root")

		result, err := op.ListCodeAgents(operator.GetCodeAgentsParams{})
		require.NoError(t, err, "ListCodeAgents should default to the resolved ancestor workspace")
		require.NotEmpty(t, result.Agents, "Stored agent should be returned")
		assert.Equal(t, sandbox.WorkspaceDir(workspace), result.Agents[0].WorkspaceDir, "Workspace should resolve to the ancestor containing memory")
		assert.FileExists(t, filepath.Join(workspace, "memory", "agent_operator-d.md"), "Workspace agent markdown should be created in the ancestor workspace")
	})

	t.Run("RejectsDuplicateNameInSameWorkspace", func(t *testing.T) {
		op := newTestOperator(t, true)
		workspace := sandbox.WorkspaceDir(t.TempDir())

		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{Workspace: workspace, Name: "twin"}), "First create should succeed")

		err := op.CreateAgent(operator.CreateAgentParams{Workspace: workspace, Name: "twin"})
		require.EqualError(t, err, `operator: agent "twin" already exists in workspace`, "Second create with same name should fail")

		// Different workspace must not be blocked.
		other := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{Workspace: other, Name: "twin"}), "Same name in a different workspace should be allowed")
	})

	t.Run("AllowsGeneratedNamesWhenFlagEnabled", func(t *testing.T) {
		op := newTestOperator(t, true)
		workspace := sandbox.WorkspaceDir(t.TempDir())

		err := op.CreateAgent(operator.CreateAgentParams{Workspace: workspace, AllowGeneratedName: true})
		require.NoError(t, err, "CreateAgent should allow generated names when the flag is enabled")

		result, err := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
		require.NoError(t, err, "ListCodeAgents should return the generated-name agent")
		require.NotEmpty(t, result.Agents, "Generated-name agent should be stored")
		assert.Contains(t, result.Agents[0].Name, "agent-", "Generated names should use the agent- prefix")
	})

	t.Run("InteractiveCreateBootstrapsAndResumesSession", func(t *testing.T) {
		op := newTestOperator(t, true)
		runtime := &stubCodeAgent{}
		op.newCodeAgent = func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			assert.Equal(t, codeagent.Provider(operator.DefaultProvider), provider, "CreateAgent should use the default provider")
			assert.Empty(t, model, "CreateAgent should let the connector resolve the default model")
			assert.NotEmpty(t, workDir, "CreateAgent should pass a workspace to the connector")
			return runtime, nil
		}

		workspace := sandbox.WorkspaceDir(t.TempDir())
		err := op.CreateAgent(operator.CreateAgentParams{Workspace: workspace, Name: "operator-interactive", Interactive: true})
		require.NoError(t, err, "Interactive CreateAgent should bootstrap and resume the initial session")

		require.Len(t, runtime.createCalls, 1, "CreateAgent should create exactly one connector session")
		assert.Equal(t, "operator-interactive", runtime.createCalls[0].Name, "Connector session should inherit the agent name")
		assert.Equal(t, string(workspace), runtime.createCalls[0].WorkDir, "Connector session should target the workspace")
		assert.Subset(t, runtime.createCalls[0].Envs, []string{
			"AXO_LINK_MCP_AUTH_TOKEN=tunnel-mcp-dev-token",
			"AXO_LINK_MCP_SENDER_ID=operator-interactive",
			"AXO_LINK_MCP_SENDER_TYPE=omni_agent",
			"AXO_LINK_MCP_AGENT_WORKSPACE=" + string(workspace),
		}, "CreateAgent should pass MCP environment variables to connector create")
		require.Len(t, runtime.resumeCalls, 1, "Interactive CreateAgent should resume the created session")
		assert.Equal(t, runtime.createResultID, runtime.resumeCalls[0].ID, "Resume should target the created session ID")
		assert.Subset(t, runtime.resumeCalls[0].Envs, []string{
			"AXO_LINK_MCP_AUTH_TOKEN=tunnel-mcp-dev-token",
			"AXO_LINK_MCP_SENDER_ID=operator-interactive",
			"AXO_LINK_MCP_SENDER_TYPE=omni_agent",
			"AXO_LINK_MCP_AGENT_WORKSPACE=" + string(workspace),
		}, "CreateAgent should pass MCP environment variables to connector resume")
	})

	t.Run("NonInteractiveCreateSkipsResume", func(t *testing.T) {
		op := newTestOperator(t, true)
		runtime := &stubCodeAgent{}
		op.newCodeAgent = func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			return runtime, nil
		}

		err := op.CreateAgent(operator.CreateAgentParams{
			Workspace:   sandbox.WorkspaceDir(t.TempDir()),
			Name:        "operator-batch",
			Interactive: false,
		})
		require.NoError(t, err, "Non-interactive CreateAgent should still create the initial session")

		require.Len(t, runtime.createCalls, 1, "CreateAgent should still bootstrap a connector session")
		assert.Empty(t, runtime.resumeCalls, "Non-interactive CreateAgent should not resume the session")
	})

	t.Run("CreateAgentPersistsEvenWhenSessionBootstrapFails", func(t *testing.T) {
		op := newTestOperator(t, true)
		workspace := sandbox.WorkspaceDir(t.TempDir())
		op.newCodeAgent = func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			return nil, errors.New("connector unavailable")
		}

		err := op.CreateAgent(operator.CreateAgentParams{
			Workspace:   workspace,
			Name:        "operator-fail",
			Interactive: true,
		})
		require.NoError(t, err, "CreateAgent should persist agent even when session bootstrap fails")

		result, listErr := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
		require.NoError(t, listErr, "ListCodeAgents should find persisted agent after failed session bootstrap")
		require.NotEmpty(t, result.Agents, "Agent should be persisted despite session failure")
		assert.Equal(t, "operator-fail", result.Agents[0].Name, "Persisted agent name should match")
	})

	t.Run("ResumeIfExistsResumesInsteadOfCreatingDuplicate", func(t *testing.T) {
		op := newTestOperator(t, true)
		workspace := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace:   workspace,
			Name:        "operator-resume-existing",
			Interactive: false,
		}), "Initial create should succeed before resume-if-exists flow")

		resumed := &stubCodeAgent{}
		op.newCodeAgent = func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			return resumed, nil
		}

		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace:      workspace,
			Name:           "operator-resume-existing",
			ResumeIfExists: true,
			Interactive:    true,
		}), "CreateAgent with resume-if-exists should resume an existing agent")

		agents, err := op.store.ListAgentsByDir(workspace)
		require.NoError(t, err, "Existing agent lookup should succeed")
		assert.Len(t, agents, 1, "Resume-if-exists should not create duplicate agent rows")
		require.Len(t, resumed.resumeCalls, 1, "Resume-if-exists should invoke resume exactly once")
	})
}

func TestTeamInit(t *testing.T) {
	t.Run("MemoryEnabledInitialisesRootTemplate", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git is required for TeamInit")
		}

		op := newTestOperator(t, true)
		workspace := t.TempDir()

		err := op.TeamInit(operator.TeamInitParams{Workspace: sandbox.WorkspaceDir(workspace)})
		require.NoError(t, err, "TeamInit should succeed when memory is enabled")

		assert.FileExists(t, filepath.Join(workspace, "memory", "memory.yaml"), "Workspace memory.yaml should be created")
		assert.FileExists(t, filepath.Join(workspace, "memory", "semantics.yaml"), "Workspace semantics.yaml should be created")
		assert.FileExists(t, filepath.Join(workspace, "memory", "metadata.yaml"), "memory metadata should be created")
		assert.DirExists(t, filepath.Join(workspace, "memory", ".git"), "memory directory should be initialised as a git repo")
		assert.FileExists(t, filepath.Join(workspace, "agent_memory.md"), "Workspace should include the memory entry markdown")
		assert.DirExists(t, filepath.Join(workspace, "memory", "agents", "guide"), "TeamInit should create the default guide agent")
		_, guideMemoryErr := os.Stat(filepath.Join(workspace, "memory", "guide", "entry", "data", "memory.yaml"))
		assert.ErrorIs(t, guideMemoryErr, os.ErrNotExist, "Guide agent should not receive per-agent memory data files")
		assert.FileExists(t, filepath.Join(workspace, "memory", "agent_guide.md"), "Guide agent workspace markdown should be created")

		result, err := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: sandbox.WorkspaceDir(workspace)})
		require.NoError(t, err, "ListCodeAgents should resolve agents for the initialised workspace")
		require.NotEmpty(t, result.Agents, "Guide agent should be stored")
		assert.Equal(t, "guide", result.Agents[0].Name, "TeamInit should create the guide agent by default")
	})

	t.Run("MemoryDisabledReturnsError", func(t *testing.T) {
		op := newTestOperator(t, false)
		err := op.TeamInit(operator.TeamInitParams{Workspace: sandbox.WorkspaceDir(t.TempDir())})
		require.ErrorIs(t, err, operator.ErrMemoryDisabled, "TeamInit should reject calls when memory is disabled")
	})

	t.Run("DefaultsWorkspaceToCwd", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git is required for TeamInit")
		}

		op := newTestOperator(t, true)
		workspace := t.TempDir()

		prev, err := os.Getwd()
		require.NoError(t, err, "Current working directory lookup should succeed")
		require.NoError(t, os.Chdir(workspace), "Changing to workspace should succeed")
		t.Cleanup(func() {
			require.NoError(t, os.Chdir(prev), "Working directory should be restored")
		})

		err = op.TeamInit(operator.TeamInitParams{})
		require.NoError(t, err, "TeamInit should default workspace to cwd")
		assert.FileExists(t, filepath.Join(workspace, "memory", "memory.yaml"), "Memory root should be created in cwd")
	})
}

func TestValidation(t *testing.T) {
	t.Run("ParamValidation", func(t *testing.T) {
		assert.NoError(t, (operator.GetCodeAgentsParams{}).Validate(), "GetCodeAgentsParams should allow empty workspace")
		assert.EqualError(t, (operator.CreateAgentParams{}).Validate(), "operator: agent name is required unless generated names are enabled", "CreateAgentParams should require a name unless generated names are enabled")
		assert.NoError(t, (operator.CreateAgentParams{AllowGeneratedName: true}).Validate(), "Generated-name flag should bypass explicit-name validation")
		assert.EqualError(t, (operator.DeleteAgentParams{}).Validate(), "operator: agent id is required", "DeleteAgentParams should require agent id")
		assert.EqualError(t, (operator.GetWorkSpaceParams{}).Validate(), "operator: workspace id is required", "GetWorkSpaceParams should require workspace id")
		assert.NoError(t, (operator.TeamInitParams{}).Validate(), "TeamInitParams should allow empty workspace")
		assert.EqualError(t, (operator.UpgradeAgentParams{}).Validate(), "operator: agent id is required", "UpgradeAgentParams should require agent id")
	})

	t.Run("OperatorMethodsRejectInvalidParams", func(t *testing.T) {
		op := newTestOperator(t, true)

		err := op.CreateAgent(operator.CreateAgentParams{})
		require.EqualError(t, err, "operator: agent name is required unless generated names are enabled", "CreateAgent should reject missing names by default")

		_, err = op.GetWorkspace(operator.GetWorkSpaceParams{})
		require.EqualError(t, err, "operator: workspace id is required", "GetWorkspace should reject empty workspace id")

		err = op.DeleteAgent(operator.DeleteAgentParams{})
		require.EqualError(t, err, "operator: agent id is required", "DeleteAgent should reject empty id")

		err = op.UpgradeAgent(operator.UpgradeAgentParams{})
		require.EqualError(t, err, "operator: agent id is required", "UpgradeAgent should reject empty id")
	})
}

func TestResumeAgent(t *testing.T) {
	t.Run("ResumesNamedAgentFromWorkspaceIndex", func(t *testing.T) {
		resumed := &stubCodeAgent{}
		op := newTestOperator(t, true)
		op.newCodeAgent = func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			return resumed, nil
		}

		workspace := sandbox.WorkspaceDir(t.TempDir())
		err := op.CreateAgent(operator.CreateAgentParams{Workspace: workspace, Name: "operator-resume"})
		require.NoError(t, err, "CreateAgent should succeed before resume")

		err = op.ResumeAgent(operator.ResumeAgentParams{Workspace: workspace, Name: "operator-resume"})
		require.NoError(t, err, "ResumeAgent should succeed for an indexed agent")
		require.Len(t, resumed.resumeCalls, 1, "Resume should be invoked exactly once")
		assert.NotEmpty(t, resumed.resumeCalls[0].ID, "Resume should be called with a session identifier")
	})

	t.Run("RejectsMissingName", func(t *testing.T) {
		op := newTestOperator(t, true)
		err := op.ResumeAgent(operator.ResumeAgentParams{})
		require.EqualError(t, err, "operator: agent name is required", "ResumeAgent should require an agent name")
	})

	t.Run("FallsBackToCreateWhenResumeSessionMissing", func(t *testing.T) {
		op := newTestOperator(t, true)
		createRuntime := &stubCodeAgent{}
		resumeRuntime := &stubCodeAgent{
			resumeErrs: []error{
				errors.New("no session found"),
				nil,
			},
		}
		calls := 0
		op.newCodeAgent = func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			calls++
			if calls == 1 {
				return createRuntime, nil
			}
			return resumeRuntime, nil
		}

		workspace := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace:   workspace,
			Name:        "operator-resume-fallback",
			Interactive: false,
		}), "CreateAgent should succeed before resume fallback test")

		err := op.ResumeAgent(operator.ResumeAgentParams{
			Workspace: workspace,
			Name:      "operator-resume-fallback",
		})
		require.NoError(t, err, "ResumeAgent should fallback to create when runtime reports missing session")
		require.Len(t, resumeRuntime.createCalls, 1, "ResumeAgent fallback should create a new session when missing")
		assert.Subset(t, resumeRuntime.createCalls[0].Envs, []string{
			"AXO_LINK_MCP_AUTH_TOKEN=tunnel-mcp-dev-token",
			"AXO_LINK_MCP_SENDER_ID=operator-resume-fallback",
			"AXO_LINK_MCP_SENDER_TYPE=omni_agent",
			"AXO_LINK_MCP_AGENT_WORKSPACE=" + string(workspace),
		}, "ResumeAgent fallback should pass MCP environment variables to connector create")
		require.Len(t, resumeRuntime.resumeCalls, 2, "ResumeAgent should attempt resume again after fallback create")
		assert.Subset(t, resumeRuntime.resumeCalls[1].Envs, []string{
			"AXO_LINK_MCP_AUTH_TOKEN=tunnel-mcp-dev-token",
			"AXO_LINK_MCP_SENDER_ID=operator-resume-fallback",
			"AXO_LINK_MCP_SENDER_TYPE=omni_agent",
			"AXO_LINK_MCP_AGENT_WORKSPACE=" + string(workspace),
		}, "ResumeAgent fallback should pass MCP environment variables to connector resume")
	})

	t.Run("InitIfMissingCreatesAgent", func(t *testing.T) {
		op := newTestOperator(t, true)
		workspace := sandbox.WorkspaceDir(t.TempDir())

		err := op.ResumeAgent(operator.ResumeAgentParams{
			Workspace:     workspace,
			Name:          "operator-init-missing",
			InitIfMissing: true,
		})
		require.NoError(t, err, "ResumeAgent with init-if-missing should create when agent is missing")

		result, listErr := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
		require.NoError(t, listErr, "ListCodeAgents should succeed after init-if-missing create")
		require.NotEmpty(t, result.Agents, "Init-if-missing should persist an agent")
		assert.Equal(t, "operator-init-missing", result.Agents[0].Name, "Init-if-missing should create with the requested name")
	})
}

func TestSwitchProvider(t *testing.T) {
	t.Run("CreatesNewSessionAndActivatesIt", func(t *testing.T) {
		op := newTestOperator(t, true)
		createRuntime := &stubCodeAgent{}
		switchRuntime := &stubCodeAgent{}
		calls := 0
		op.newCodeAgent = func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			calls++
			if calls == 1 {
				return createRuntime, nil
			}
			return switchRuntime, nil
		}

		workspace := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace:   workspace,
			Name:        "switch-me",
			Interactive: false,
		}), "CreateAgent should succeed before switching provider")

		result, err := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
		require.NoError(t, err, "ListCodeAgents should return created agent")
		require.NotEmpty(t, result.Agents, "Created agent should be listed")
		agentID := result.Agents[0].ID

		require.NoError(t, op.SwitchProvider(operator.SwitchProviderParams{
			ID:       agentID,
			Provider: codeagent.Provider("codex"),
		}), "SwitchProvider should create and activate a session for the new provider")

		active, err := op.sessionStore.GetSession(agentID)
		require.NoError(t, err, "Active session lookup should succeed after switch")
		require.NotNil(t, active.Model, "Active session model should be stored")
		assert.Equal(t, codeagent.Provider("codex"), active.Model.Provider, "SwitchProvider should set active provider to requested value")
		require.Len(t, switchRuntime.resumeCalls, 1, "SwitchProvider should resume the target provider session")
		assert.Subset(t, switchRuntime.createCalls[0].Envs, []string{
			"AXO_LINK_MCP_AUTH_TOKEN=tunnel-mcp-dev-token",
			"AXO_LINK_MCP_SENDER_ID=switch-me",
			"AXO_LINK_MCP_SENDER_TYPE=omni_agent",
			"AXO_LINK_MCP_AGENT_WORKSPACE=" + string(workspace),
		}, "SwitchProvider should pass MCP environment variables to connector create")
		assert.Subset(t, switchRuntime.resumeCalls[0].Envs, []string{
			"AXO_LINK_MCP_AUTH_TOKEN=tunnel-mcp-dev-token",
			"AXO_LINK_MCP_SENDER_ID=switch-me",
			"AXO_LINK_MCP_SENDER_TYPE=omni_agent",
			"AXO_LINK_MCP_AGENT_WORKSPACE=" + string(workspace),
		}, "SwitchProvider should pass MCP environment variables to connector resume")
	})

	t.Run("ReusesExistingProviderSessionWhenCleanStartDisabled", func(t *testing.T) {
		op := newTestOperator(t, true)
		createRuntime := &stubCodeAgent{}
		firstSwitchRuntime := &stubCodeAgent{}
		secondSwitchRuntime := &stubCodeAgent{}
		calls := 0
		op.newCodeAgent = func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			calls++
			switch calls {
			case 1:
				return createRuntime, nil
			case 2:
				return firstSwitchRuntime, nil
			default:
				return secondSwitchRuntime, nil
			}
		}

		workspace := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace:   workspace,
			Name:        "switch-reuse",
			Interactive: false,
		}), "CreateAgent should succeed before switching provider")
		result, err := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
		require.NoError(t, err, "ListCodeAgents should return created agent")
		require.NotEmpty(t, result.Agents, "Created agent should be listed")
		agentID := result.Agents[0].ID

		require.NoError(t, op.SwitchProvider(operator.SwitchProviderParams{
			ID:       agentID,
			Provider: codeagent.Provider("codex"),
		}), "First switch should create a provider session")

		sessionsBefore, err := op.sessionStore.ListSessions(agentID, nil)
		require.NoError(t, err, "Session listing should succeed after first switch")

		require.NoError(t, op.SwitchProvider(operator.SwitchProviderParams{
			ID:         agentID,
			Provider:   codeagent.Provider("codex"),
			CleanStart: false,
		}), "Second switch with clean_start disabled should reuse existing provider session")

		sessionsAfter, err := op.sessionStore.ListSessions(agentID, nil)
		require.NoError(t, err, "Session listing should succeed after second switch")
		assert.Len(t, sessionsAfter, len(sessionsBefore), "SwitchProvider should reuse existing provider session when clean start is disabled")
		assert.Empty(t, secondSwitchRuntime.createCalls, "SwitchProvider should not create a new session when reusing existing provider session")
	})
}

func TestSessionControls(t *testing.T) {
	t.Run("StopSessionUsesSafeStopWithResolvedActiveSession", func(t *testing.T) {
		op := newTestOperator(t, true)
		pty := &stubPTYDaemonClient{}
		op.ptyDaemon = pty

		workspace := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace:   workspace,
			Name:        "stop-me",
			Interactive: false,
		}))
		result, err := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
		require.NoError(t, err)
		require.Len(t, result.Agents, 1)

		stopped, err := op.StopSession(operator.StopSessionParams{AgentID: result.Agents[0].ID, Force: true})
		require.NoError(t, err)
		assert.Equal(t, result.Agents[0].ID, stopped.SessionID)
		require.Len(t, pty.stopSafeCalls, 1)
		assert.Equal(t, result.Agents[0].ID, pty.stopSafeCalls[0].sessionID)
		assert.True(t, pty.stopSafeCalls[0].force)
	})

	t.Run("DetachSessionUsesResolvedActiveSession", func(t *testing.T) {
		op := newTestOperator(t, true)
		pty := &stubPTYDaemonClient{}
		op.ptyDaemon = pty

		workspace := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace:   workspace,
			Name:        "detach-me",
			Interactive: false,
		}))
		result, err := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
		require.NoError(t, err)
		require.Len(t, result.Agents, 1)

		detached, err := op.DetachSession(operator.DetachSessionParams{AgentID: result.Agents[0].ID})
		require.NoError(t, err)
		assert.Equal(t, result.Agents[0].ID, detached.SessionID)
		require.Equal(t, []string{result.Agents[0].ID}, pty.detachCalls)
	})
}

func TestExecInSession(t *testing.T) {
	t.Run("AutoResumeUsesDetachedSession", func(t *testing.T) {
		runtime := &stubCodeAgent{}
		op := newTestOperator(t, true)
		op.ptyDaemon = &stubPTYDaemonClient{}
		op.newCodeAgent = func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			return runtime, nil
		}

		workspace := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace:   workspace,
			Name:        "exec-me",
			Interactive: false,
		}))

		_, err := op.ExecInSession(operator.ExecInSessionParams{AgentID: runtime.createResultID, Prompt: "hello"})
		require.NoError(t, err)
		require.Len(t, runtime.resumeCalls, 1)
		assert.True(t, runtime.resumeCalls[0].Detached)
		require.Len(t, runtime.execInSessionCalls, 1)
		assert.Equal(t, "hello", runtime.execInSessionCalls[0].Prompt)
	})
}

func TestPipe(t *testing.T) {
	t.Run("ResolvesActiveSessionWhenSessionIDMissing", func(t *testing.T) {
		op := newTestOperator(t, true)
		pty := &stubPTYDaemonClient{}
		op.ptyDaemon = pty

		workspace := sandbox.WorkspaceDir(t.TempDir())
		require.NoError(t, op.CreateAgent(operator.CreateAgentParams{
			Workspace:   workspace,
			Name:        "pipe-me",
			Interactive: false,
		}), "CreateAgent should create an active session for pipe")

		result, err := op.ListCodeAgents(operator.GetCodeAgentsParams{Workspace: workspace})
		require.NoError(t, err, "ListCodeAgents should return the pipe target agent")
		require.Len(t, result.Agents, 1, "ListCodeAgents should include one pipe target agent")
		agentID := result.Agents[0].ID
		prev, err := os.Getwd()
		require.NoError(t, err, "Current working directory lookup should succeed")
		require.NoError(t, os.Chdir(string(workspace)), "Changing to workspace should succeed")
		t.Cleanup(func() {
			require.NoError(t, os.Chdir(prev), "Working directory should be restored")
		})

		err = op.Pipe(operator.PipeParams{
			AgentID: "pipe-me",
			Data:    []byte("hello"),
		})
		require.NoError(t, err, "Pipe should resolve the active session when session_id is omitted")
		require.Len(t, pty.pipeCalls, 1, "Pipe should forward exactly one payload to the PTY daemon")
		assert.Equal(t, agentID, pty.pipeCalls[0].agentID, "Pipe should forward the resolved agent ID")
		assert.Equal(t, agentID, pty.pipeCalls[0].sessionID, "Pipe should use the active session ID")
		assert.Equal(t, []byte("hello"), pty.pipeCalls[0].data, "Pipe should forward the raw payload")
	})
}

func newTestOperator(t *testing.T, memoryEnabled bool) *DefaultOperator {
	t.Helper()

	db := newTestDB(t)
	store := operatorstore.NewWithDB(db)
	op := &DefaultOperator{
		config: config.OmniConfig{
			Features: &config.Features{Memory: memoryEnabled},
		},
		store:        store,
		sessionStore: codesession.NewWithDB(db),
		newCodeAgent: func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			return &stubCodeAgent{}, nil
		},
	}
	if memoryEnabled {
		op.agentMemory = operator.NewDefaultAgentMemory()
	}
	return op
}

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "operator-test.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err, "Opening temp sqlite database should succeed")
	t.Cleanup(func() {
		require.NoError(t, db.Close(), "Closing temp sqlite database should succeed")
	})

	_, err = db.Exec(testAgentSchema)
	require.NoError(t, err, "Creating agent tables should succeed")

	_, err = db.Exec(testWorkspaceSchema)
	require.NoError(t, err, "Creating workspaces table should succeed")

	_, err = db.Exec(testCodeSessionSchema)
	require.NoError(t, err, "Creating code sessions table should succeed")

	return db
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	row := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, name)
	var count int
	require.NoError(t, row.Scan(&count), "sqlite_master lookup should succeed")
	return count == 1
}

type stubCodeAgent struct {
	createResultID     string
	createCalls        []codeagent.CreateSessionParams
	resumeCalls        []codeagent.ResumeSessionParams
	resumeErrs         []error
	execInSessionCalls []codeagent.ExecInSessionParams
}

func (s *stubCodeAgent) Create(p codeagent.CreateSessionParams) (*codeagent.CreateSessionResult, error) {
	s.createCalls = append(s.createCalls, p)
	if s.createResultID == "" {
		s.createResultID = p.ID
		if s.createResultID == "" {
			s.createResultID = "stub-session"
		}
	}
	return &codeagent.CreateSessionResult{ID: s.createResultID, Name: p.Name}, nil
}

func (s *stubCodeAgent) Exec(codeagent.ExecuteParams) (*codeagent.ExecuteResult, error) {
	return &codeagent.ExecuteResult{}, nil
}

func (s *stubCodeAgent) Stream(codeagent.StreamParams) (*codeagent.StreamResult, error) {
	ch := make(chan codeagent.StreamEvent)
	close(ch)
	return &codeagent.StreamResult{Events: ch}, nil
}

func (s *stubCodeAgent) Resume(p codeagent.ResumeSessionParams) (*codeagent.ResumeSessionResult, error) {
	s.resumeCalls = append(s.resumeCalls, p)
	if len(s.resumeErrs) > 0 {
		err := s.resumeErrs[0]
		s.resumeErrs = s.resumeErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return &codeagent.ResumeSessionResult{SessionID: p.ID, ProcessID: "123"}, nil
}

func (s *stubCodeAgent) List(codeagent.ListSessionsParams) (*codeagent.ListSessionsResult, error) {
	return &codeagent.ListSessionsResult{}, nil
}

func (s *stubCodeAgent) Delete(codeagent.DeleteSessionParams) (*codeagent.DeleteSessionResult, error) {
	return &codeagent.DeleteSessionResult{}, nil
}

func (s *stubCodeAgent) GetSessionConfig(codeagent.GetSessionConfigParams) (*codeagent.GetSessionConfigResult, error) {
	return &codeagent.GetSessionConfigResult{}, nil
}

func (s *stubCodeAgent) GetSessionSandbox(codeagent.GetSessionSandboxParams) (*codeagent.GetSessionSandboxResult, error) {
	return &codeagent.GetSessionSandboxResult{}, nil
}

func (s *stubCodeAgent) UpdateSessionSandbox(codeagent.UpdateSessionSandboxParams) (*codeagent.UpdateSessionSandboxResult, error) {
	return &codeagent.UpdateSessionSandboxResult{}, nil
}

func (s *stubCodeAgent) Capabilities() (*codeagent.Capabilities, error) {
	return &codeagent.Capabilities{}, nil
}

func (s *stubCodeAgent) Info() *codeagent.CodeAgentInfo {
	return &codeagent.CodeAgentInfo{}
}

func (s *stubCodeAgent) GetUserIdentity() codeagent.UserIdentify {
	return codeagent.UserIdentify{}
}

func (s *stubCodeAgent) Stop() {}

func (s *stubCodeAgent) SetPTYClient(codeagent.PTYClient) {}

func (s *stubCodeAgent) ExecInSession(p codeagent.ExecInSessionParams) (*codeagent.ExecInSessionResult, error) {
	s.execInSessionCalls = append(s.execInSessionCalls, p)
	return &codeagent.ExecInSessionResult{SessionID: p.SessionID}, nil
}

func (s *stubCodeAgent) Discover() (codeagent.DiscoverResult, error) {
	return codeagent.DiscoverResult{}, nil
}

func (s *stubCodeAgent) Defaults() (*codeagent.Config, error) {
	return &codeagent.Config{}, nil
}

func (s *stubCodeAgent) UpdateDefaults(*codeagent.Config) error {
	return nil
}

func (s *stubCodeAgent) GetUserSettings() (*codeagent.Settings, error) {
	return &codeagent.Settings{}, nil
}

func (s *stubCodeAgent) GetWorkspaceSettings(sandbox.WorkspaceDir) (*codeagent.Settings, error) {
	return &codeagent.Settings{}, nil
}

func (s *stubCodeAgent) SaveDefaultSettings(*codeagent.Settings) error {
	return nil
}

func (s *stubCodeAgent) WatchDefaultSettings(func(*codeagent.Settings)) error {
	return nil
}

func (s *stubCodeAgent) SupportedHooks() (*hooks.Capabilities, error) {
	return &hooks.Capabilities{}, nil
}

func (s *stubCodeAgent) Register(hooks.RegisterHookParams) error {
	return nil
}

func (s *stubCodeAgent) GetRegisteredHooks() []*hooks.HookData {
	return nil
}

func (s *stubCodeAgent) DeleteHook(hooks.DeleteHookParams) (bool, error) {
	return false, nil
}

func (s *stubCodeAgent) PreToolUseParams(any) (*hooks.PreToolUseParams, error) {
	return nil, nil
}

func (s *stubCodeAgent) PostToolUseParams(any) (*hooks.PostToolUseParams, error) {
	return nil, nil
}

func (s *stubCodeAgent) PostToolUseFailureParams(any) (*hooks.PostToolUseFailureParams, error) {
	return nil, nil
}

func (s *stubCodeAgent) SessionStartParams(any) (*hooks.SessionStartParams, error) {
	return nil, nil
}

func (s *stubCodeAgent) SessionEndParams(any) (*hooks.SessionEndParams, error) {
	return nil, nil
}

func (s *stubCodeAgent) PrePromptInputParams(any) (*hooks.PrePromptInputParams, error) {
	return nil, nil
}

func (s *stubCodeAgent) PostPromptInputParams(any) (*hooks.PostPromptInputParams, error) {
	return nil, nil
}

func (s *stubCodeAgent) PreToolUseResult(any) (*hooks.PreToolUseResult, error) {
	return nil, nil
}

func (s *stubCodeAgent) PostToolUseResult(any) (*hooks.PostToolUseResult, error) {
	return nil, nil
}

func (s *stubCodeAgent) PostToolUseFailureResult(any) (*hooks.PostToolUseFailureResult, error) {
	return nil, nil
}

func (s *stubCodeAgent) SessionStartResult(any) (*hooks.SessionStartResult, error) {
	return nil, nil
}

func (s *stubCodeAgent) SessionEndResult(any) (*hooks.SessionEndResult, error) {
	return nil, nil
}

func (s *stubCodeAgent) PrePromptInputResult(any) (*hooks.PrePromptInputResult, error) {
	return nil, nil
}

func (s *stubCodeAgent) PostPromptInputResult(any) (*hooks.PostPromptInputResult, error) {
	return nil, nil
}

type stubPTYDaemonClient struct {
	pipeCalls []struct {
		agentID   string
		sessionID string
		data      []byte
	}
	stopSafeCalls []struct {
		sessionID string
		force     bool
	}
	detachCalls []string
}

func (s *stubPTYDaemonClient) Pipe(agentID, sessionID string, data []byte) error {
	s.pipeCalls = append(s.pipeCalls, struct {
		agentID   string
		sessionID string
		data      []byte
	}{
		agentID:   agentID,
		sessionID: sessionID,
		data:      append([]byte(nil), data...),
	})
	return nil
}

func (s *stubPTYDaemonClient) Start(string, []string, []string, string, string) error {
	return nil
}

func (s *stubPTYDaemonClient) Attach(context.Context, string) error {
	return nil
}

func (s *stubPTYDaemonClient) Exec(string, string) error {
	return nil
}

func (s *stubPTYDaemonClient) Stop(string) error {
	return nil
}

func (s *stubPTYDaemonClient) Register(string, string, string) error {
	return nil
}

func (s *stubPTYDaemonClient) StopSafe(sessionID string, force bool) error {
	s.stopSafeCalls = append(s.stopSafeCalls, struct {
		sessionID string
		force     bool
	}{sessionID: sessionID, force: force})
	return nil
}

func (s *stubPTYDaemonClient) Detach(sessionID string) error {
	s.detachCalls = append(s.detachCalls, sessionID)
	return nil
}

func (s *stubPTYDaemonClient) List(string) ([]*ptyclients.PTYTerminalInfo, error) {
	return nil, nil
}

func (s *stubPTYDaemonClient) Get(string, string) (*ptyclients.PTYTerminalInfo, error) {
	return nil, nil
}

func (s *stubPTYDaemonClient) ListAttached(string) ([]ptyclients.AttachedProcess, error) {
	return nil, nil
}

func (s *stubPTYDaemonClient) MetaAttached(string) (int, error) {
	return 0, nil
}
