package hookoperator

import "github.com/Shaik-Sirajuddin/omni/config"

// hookEntriesFromConfig extracts the hook entries map from an OmniConfig.
// Returns an empty map when the config has no agent hooks section.
func hookEntriesFromConfig(cfg *config.OmniConfig) map[string][]config.HookEntry {
	out := map[string][]config.HookEntry{}
	if cfg == nil || cfg.Agent == nil || cfg.Agent.Hooks == nil {
		return out
	}
	for eventName, entries := range cfg.Agent.Hooks {
		if len(entries) == 0 {
			continue
		}
		cp := make([]config.HookEntry, len(entries))
		copy(cp, entries)
		out[eventName] = cp
	}
	return out
}
