package config

import "github.com/Shaik-Sirajuddin/omni/config"

type WatchParams struct {
	Workspace string
}

type ConfigResult struct {
	Workspace string
	// Is Default Config
	Default bool
	config  *config.OmniConfig
}

// FileWatcher daemon process with singleton instance
// Only Launch and runs as one of omni agent is active
type FileWatcher struct {
}

// Deamon Backend

// Meant to be utilized by hooks.watcher (need all agent hooks) ,
// (omniconfig , all agent hooks)
type Manager interface {
	// Register register all workspace and all agent callbacks
	Register(component string, callback func(result ConfigResult)) error

	// Initializes Manager initial functions
	Init()

	// entrypoint
	DeRegister(component string) error
}

// read file dirs for each agent (workspace and ls)
// call agent execution in order (agent execute command)
// execute 