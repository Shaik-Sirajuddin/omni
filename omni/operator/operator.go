package operator

import (
	"fmt"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/omniagent"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

const (
	DefaultProvider = "claude"
)

type GetCodeAgentsParams struct {
	Workspace sandbox.WorkspaceDir `json:"workspace,omitempty"`
}

type GetAgentsResult struct {
	Agents []*omniagent.AgentInfo `json:"agents"`
}

type CreateAgentParams struct {
	Workspace          sandbox.WorkspaceDir `json:"workspace,omitempty"`
	Name               string               `json:"name,omitempty"`
	Provider           codeagent.Provider   `json:"provider,omitempty"`
	Model              string               `json:"model,omitempty"`
	AllowGeneratedName bool                 `json:"allow_generated_name,omitempty"`
	ResumeIfExists     bool                 `json:"resume_if_exists,omitempty"`
	Interactive        bool                 `json:"interactive"` // launch after create; default true
	SessionID          string               `json:"session_id,omitempty"`
	// RunConfig holds pass-through runner flags and env vars to persist for this agent.
	RunConfig *omniagent.RunConfig `json:"run_config,omitempty"`
}

type ResumeAgentParams struct {
	Workspace     sandbox.WorkspaceDir `json:"workspace,omitempty"`
	Name          string               `json:"name,omitempty"`
	InitIfMissing bool                 `json:"init_if_missing,omitempty"`
	Provider      codeagent.Provider   `json:"provider,omitempty"`
	Model         string               `json:"model,omitempty"`
	SessionID     string               `json:"session_id,omitempty"`
	Detached      bool                 `json:"detached,omitempty"`
	// RunConfig overrides to merge into the persisted RunConfig.
	RunConfig *omniagent.RunConfig `json:"run_config,omitempty"`
	// ClearArgs wipes stored ExtraArgs before applying any RunConfig.ExtraArgs.
	ClearArgs bool `json:"clear_args,omitempty"`
	// ClearEnvs wipes stored Envs before applying any RunConfig.Envs.
	ClearEnvs bool `json:"clear_envs,omitempty"`
}

type UpdateAgentParams struct {
	Workspace sandbox.WorkspaceDir `json:"workspace,omitempty"`
	Name      string               `json:"name,omitempty"`
	// RunConfig overrides to merge into the persisted RunConfig.
	RunConfig *omniagent.RunConfig `json:"run_config,omitempty"`
	// ClearArgs wipes stored ExtraArgs before applying any RunConfig.ExtraArgs.
	ClearArgs bool `json:"clear_args,omitempty"`
	// ClearEnvs wipes stored Envs before applying any RunConfig.Envs.
	ClearEnvs bool `json:"clear_envs,omitempty"`
}

type DeleteAgentParams struct {
	ID string `json:"id"`
}

type TeamInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Remote       string `json:"remote"`
	WorkspaceDir string `json:"workspace_dir"`
	Agents       int    `json:"agents"`
}

type ListWorkspacesParams struct{}

type ListWorkspacesResult struct {
	Teams []*TeamInfo `json:"teams"`
}

type GetWorkSpaceParams struct {
	ID string `json:"id"`
}

type TeamInitParams struct {
	Workspace sandbox.WorkspaceDir `json:"workspace,omitempty"`
	// RepoURL is optional. When set the memory dir is tracked as a git submodule.
	// When empty an empty git repository is initialised inside the memory dir.
	RepoURL string `json:"repo_url,omitempty"`
	// Name is the team name to register. Defaults to the workspace folder basename.
	// When the name conflicts on the same remote a 5-word slug is appended automatically.
	Name string `json:"name,omitempty"`
	// Remote is the remote address this workspace belongs to. Defaults to "localhost".
	Remote string `json:"remote,omitempty"`
	// Layout is an optional path to a provision layout YAML file. When set the
	// operator batch-creates all agents declared in the file.
	Layout string `json:"layout,omitempty"`
	// TerminalLayout is an optional path to a terminal-native layout file
	// (e.g. a KDL file for zellij). Used together with Terminal.
	TerminalLayout string `json:"terminal_layout,omitempty"`
	// Terminal names the terminal provider to use for the session (e.g. "zellij").
	Terminal string `json:"terminal,omitempty"`
}

// TerminalStatus reports the health of a single terminal provider.
type TerminalStatus struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Binary    string `json:"binary,omitempty"`
}

// DoctorTerminalsResult holds health status for all registered terminal providers.
type DoctorTerminalsResult struct {
	Terminals []TerminalStatus `json:"terminals"`
}

type UpgradeAgentParams struct {
	ID string `json:"id"`
	// Version to upgrade to (e.g. "v2"). Empty means upgrade to the latest embedded template.
	Version string `json:"version,omitempty"`
}

// type TeamDefaults struct {
// 	AgentDefaults *codeagent.AgentDefaults
// 	AgentData     *codeagent.Data
// }

type GetTeamResult struct {
	Info   *TeamInfo              `json:"info"`
	Agents []*omniagent.AgentInfo `json:"agents"`
}

type ForkAgentParams struct {
	ID       string              `json:"id"`
	Settings *omniagent.Settings `json:"settings"`
}

type SwitchProviderParams struct {
	ID         string               `json:"id"`
	CleanStart bool                 `json:"clean_start,omitempty"`
	Provider   codeagent.Provider   `json:"provider,omitempty"`
	SessionID  string               `json:"session_id,omitempty"`
	RunConfig  *omniagent.RunConfig `json:"run_config,omitempty"`
	ClearArgs  bool                 `json:"clear_args,omitempty"`
	ClearEnvs  bool                 `json:"clear_envs,omitempty"`
}

type ExecInSessionParams struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
	// LiveOnly skips execution and returns an error when the session is not
	// currently attached. When false (default) the operator auto-resumes the
	// agent before executing.
	LiveOnly bool `json:"live_only,omitempty"`
	// Detached signals that the session was started in detached mode (no TTY
	// attachment). When true, waitPTYReady is always run before delivering the
	// prompt regardless of recentlyStarted so the TUI has time to initialise.
	Detached bool `json:"detached,omitempty"`
}

type ExecInSessionResult struct {
	SessionID string `json:"session_id"`
}

type StopSessionParams struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id,omitempty"`
	Force     bool   `json:"force,omitempty"`
}

type StopSessionResult struct {
	SessionID string `json:"session_id"`
}

type DetachSessionParams struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id,omitempty"`
}

type DetachSessionResult struct {
	SessionID string `json:"session_id"`
}

type PipeParams struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	Data      []byte `json:"data"`
}

// ProviderModels pairs a provider with its available model IDs.
type ProviderModels struct {
	Provider codeagent.Provider `json:"provider"`
	Models   []string           `json:"models"`
}

type DisocveryResult struct {
	Providers []codeagent.Provider `json:"providers"`
	Models    []ProviderModels     `json:"models"`
}

func (p GetCodeAgentsParams) Validate() error {
	return nil
}

func (p CreateAgentParams) Validate() error {
	if p.Name == "" && !p.AllowGeneratedName {
		return fmt.Errorf("operator: agent name is required unless generated names are enabled")
	}
	return nil
}

func (p DeleteAgentParams) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("operator: agent id is required")
	}
	return nil
}

func (p GetWorkSpaceParams) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("operator: workspace id is required")
	}
	return nil
}

func (p TeamInitParams) Validate() error {
	return nil
}

func (p UpgradeAgentParams) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("operator: agent id is required")
	}
	return nil
}

// ErrMemoryDisabled is returned when a memory operation is attempted but the feature is off.
var ErrMemoryDisabled = fmt.Errorf("operator: memory feature disabled")

// ErrResolverUnavailable is returned when a provider has no exported settings resolver.
var ErrResolverUnavailable = fmt.Errorf("operator: settings resolver unavailable for provider")

// Operator manages the state of default agents
// provisioning of new agent happens through operator
type Operator interface {
	// DisoverCodeAgents performs discover of available agents in local pc
	// GPT : DisoverCodeAgents calls codeagents info checks to filter available agents
	DisoverCodeAgents() (*DisocveryResult, error)
	ListCodeAgents(params GetCodeAgentsParams) (*GetAgentsResult, error)

	// Createagent creates an agent and creates a team when no agents exist in the workspace
	// else the agent is added to existing team
	CreateAgent(params CreateAgentParams) error

	ListWorkspaces(params ListWorkspacesParams) (ListWorkspacesResult, error)
	GetWorkspace(params GetWorkSpaceParams) (GetTeamResult, error)
	// DeleteAgent from index , memory is retained
	DeleteAgent(params DeleteAgentParams) error

	// UpdateAgent merges run_config overrides into the agent's persisted settings
	// without launching a session. Useful for updating --arg/--env flags in place.
	UpdateAgent(params UpdateAgentParams) error
	// ForkAgent(params)
	GetCodeAgentResolver(agent codeagent.Provider) (*codeagent.SettingsResolver, error)

	// TeamInit initialises the memory folder for a workspace. It runs git submodule add
	// when RepoURL is set, otherwise initialises a bare local git repo inside memory/.
	// Memory is seeded with the current template regardless of the git strategy.
	TeamInit(params TeamInitParams) error

	// UpgradeAgent applies a newer version template to an existing agent's memory dir.
	UpgradeAgent(params UpgradeAgentParams) error

	// Resume agent launches the codeagent process interactivesly
	// continues the last session
	// Launches a new session when no previous session exists
	ResumeAgent(ResumeAgentParams) error

	// SwtichProvider switches the underlying model of current agent
	// Retaining memories from the summaries generated
	SwitchProvider(SwitchProviderParams) error

	ExecInSession(ExecInSessionParams) (*ExecInSessionResult, error)
	StopSession(StopSessionParams) (*StopSessionResult, error)
	DetachSession(DetachSessionParams) (*DetachSessionResult, error)
	Pipe(PipeParams) error

	// ListTemplates returns the short-names of all embedded agent templates.
	ListTemplates() ([]string, error)

	// DoctorTerminals checks whether each registered terminal provider binary
	// is present on the system.
	DoctorTerminals() (*DoctorTerminalsResult, error)

	// InstallTerminal runs the install procedure for the named terminal provider.
	InstallTerminal(name string) error
}
