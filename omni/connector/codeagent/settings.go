package codeagent

import (
	"github.com/Shaik-Sirajuddin/omni/connector/codeagent/hooks"
	sandbox "github.com/Shaik-Sirajuddin/omni/sandbox/provider"
)

// OutputFormat controls the format of Exec responses.
type OutputFormat string

const (
	OutputFormatText       OutputFormat = "text"
	OutputFormatJSON       OutputFormat = "json"
	OutputFormatStreamJSON OutputFormat = "stream-json"
)

// PermissionMode mirrors the permission modes supported across agents.
type PermissionMode string

const (
	PermissionDefault           PermissionMode = "default"
	PermissionPlan              PermissionMode = "plan"
	PermissionAcceptEdits       PermissionMode = "acceptEdits"
	PermissionAuto              PermissionMode = "auto"
	PermissionDontAsk           PermissionMode = "dontAsk"
	PermissionBypassPermissions PermissionMode = "bypassPermissions"
)

type Config struct {
	Model          Model
	PermissionMode PermissionMode
	Hooks          *hooks.HookData
	Sandbox        *sandbox.Config
}

// Settings define a response neutral layout of agent configs
type Settings struct {
	Provider Provider
	Config
	// implementation specific data
	any
}

// SettingsResolver defines stubs to manage data of  the raw config files
type SettingsResolver interface {
	GetUserSettings() (*Settings, error)
	GetWorkspaceSettings(sandbox.WorkspaceDir) (*Settings, error)
	SaveDefaultSettings(*Settings) error
	WatchDefaultSettings(func(*Settings)) error
}
