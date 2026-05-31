package omniagent

import (
	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

type ConfigPaths struct {
	GlobalConfigDirs    []string
	WorkspaceConfigDirs []string
}

var Config ConfigPaths = ConfigPaths{
	GlobalConfigDirs: []string{
		"memory/config",
	},
	WorkspaceConfigDirs: []string{
		"memory/config/",
	},
}

// Workspace level directory
const (
	AGENTS_ROOT_DIR = "/agents"
	CONFIG_FILE     = "/config.json"
)

//Example
//AGENT_DIR       = AGENTS_ROOT_DIR + "agent_name"

type AgentInfo struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	WorkspaceDir sandbox.WorkspaceDir `json:"workspace_dir"`
	// dir path for agent specific files
	MemoryDir string `json:"memory_dir"`
}

type CodeSession struct {
	Id             string           `json:"id"`
	Model          *codeagent.Model `json:"model"`
	Idx            int              `json:"idx"`
	IsActive       bool             `json:"is_active"`
	Prompts        int              `json:"prompts"`
	LastSyncPrompt int              `json:"last_sync_prompt"`
	Status         string           `json:"status"`       // "running" | "ready" | "stopped"
	StopReason     string           `json:"stop_reason"`  // "tokens_exhausted" | "interrupted" | "network" | "other"
	IsInterrupted  bool             `json:"is_interrupted"`

	TokensInput        int     `json:"tokens_input"`
	TokensOutput       int     `json:"tokens_output"`
	TokensCachedInput  int     `json:"tokens_cached_input"`
	TokensCachedOutput int     `json:"tokens_cached_output"`
	TokensMax          int     `json:"tokens_max"`
	TokensConsumedPct  float64 `json:"tokens_consumed_percent"`
}

type PersistentMemory struct {
	// agent write memory
}

type Settings struct {
	settings config.Settings
	// Default workspace
	Sandbox      *sandbox.Config  `json:"sandbox"`
	DefaultModel *codeagent.Model `json:"default_model"`
	hooks.Capabilities
}

type Data struct {
	Info *AgentInfo `json:"info"`
	// Current active workspace
	ActiveWorkSpace *sandbox.Config `json:"active_workspace"`
	ActiveSession   *CodeSession    `json:"active_session"`
	Settings        *Settings       `json:"settings"`
	Sessions        []*CodeSession  `json:"sessions"`
}

type OmniAgentActions interface {
	// New command provision an agent based on defaults with omniagent pre hooks
	New()
	// SyncMemory syncronizes session memory to persistant store from current model
	SyncMemory()
	// NewCodeSession creates a new session optionally can specify a different provider , model to use
	NewCodeSession()
}

// createsession ->

// OmniAgent implement [codeagent.SettingsResolver]
// OmniAgent utilizes codeagents to provision an agent , with its own memory , session management
type OmniAgent interface {
	codeagent.CodeAgent
	OmniAgentActions
	Data() *Data
}

type OmniAgentEntrypoint interface {
	PreToolUse()
	PrePrompt()
	PostPrompt()
	PostToolUse()
	SessionStart()
	SessionEnd()
}
