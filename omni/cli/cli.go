package cli

import "github.com/Shaik-Sirajuddin/omni/config"

type Cli interface {
	// Install executes the CLI root command.
	Install() error
}

// ResolveConfig loads persisted config and ensures expected defaults.
func ResolveConfig(resolver config.OmniConfigResolver) (*config.OmniConfig, error) {
	cfg, err := resolver.GetUserSettings()
	if err != nil {
		return nil, err
	}
	return ensureConfigDefaults(cfg), nil
}

// SaveConfig persists config after applying defaults.
func SaveConfig(resolver config.OmniConfigResolver, cfg *config.OmniConfig) error {
	return resolver.SaveUserSettings(ensureConfigDefaults(cfg))
}

func ensureConfigDefaults(cfg *config.OmniConfig) *config.OmniConfig {
	return config.ApplyOmniConfigDefaults(cfg)
}
