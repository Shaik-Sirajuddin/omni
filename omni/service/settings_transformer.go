package service

import (
	"github.com/Shaik-Sirajuddin/omni/config"
	"github.com/Shaik-Sirajuddin/omni/connector/codeagent"
	"github.com/Shaik-Sirajuddin/omni/omniagent"
	"github.com/Shaik-Sirajuddin/omni/operator"
)

// SettingsTransformer paris with a watcher to auto propogate changes at one of config to across all configs
// Its implemented for Global settings only
type SettingsTransformer interface {
	*config.OmniConfig

	Get() ([]*codeagent.Settings, error)
	// GetUnified resolves a merged config file by deduplicationg fields acorss settings
	GetUnified([]*codeagent.Settings) *omniagent.Settings
	// WatchUnified watches all config files and returns the unified settings if any one of config file changes
	// applies any addition to one of config to all other configs , deletions are not propogated unless removed from omniagent.settings
	// modifying multiple files is concurrent safe
	WatchUnified(config, callback func(*omniagent.Settings))
	// Apply Unified applies the settings across all codeagent configs
	ApplyUnified(*omniagent.Settings) error
	// Init faciliates auto watching and propogation
	Init(*config.OmniConfig) error
	// Sync refreshes current pointers by requesting new pointers through omniResolver
	Sync() error
}

// SettingsWatcher implements [SettingsTransformer]
// acquires agents settingResolvers data from operator
type DefaultSettingsTransformer struct {
	*config.OmniConfig
	operator operator.Operator
	// To write to default [config.OmniConfig] it utilizes
	omniResolver *config.DefaultOmniConfigResolver

	// uses resolvers for propogration and fetch
	// codeagent - resolver maps
	settingResolvers map[string]*codeagent.SettingsResolver
}

// Root , Workspace , Agent ,

