package config

import (
	"path/filepath"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/pkg/sockpath"
)

const unixScheme = "unix://"

// canonicalizeHookURL rewrites an omni-internal unix:// hook URL onto the single
// canonical service socket, preserving its HTTP route. All internal hook URLs
// reach the same /hook consumer, so collapsing them here keeps the same logical
// hook from being stored under several stale socket paths — whether the drift came
// from a different runtime dir (/run/omni-<user>/ vs $XDG_RUNTIME_DIR/omni/) or a
// superseded socket name (mcp.sock). Non-unix URLs, and unix sockets that aren't
// omni-owned (third-party hook consumers), are returned unchanged.
func canonicalizeHookURL(raw string) string {
	if !strings.HasPrefix(raw, unixScheme) {
		return raw
	}
	socketPath, route := splitUnixSocketURL(strings.TrimPrefix(raw, unixScheme))
	switch filepath.Base(socketPath) {
	case sockpath.NameService, sockpath.NameMCP:
		return unixScheme + sockpath.Service() + route
	}
	return raw
}

// splitUnixSocketURL splits a unix socket path from its HTTP route.
// Input: /run/omni/service.sock/hook -> /run/omni/service.sock , /hook
// Input: /run/omni/service.sock      -> /run/omni/service.sock , /
func splitUnixSocketURL(path string) (socketPath, route string) {
	const sockSuffix = ".sock"
	if idx := strings.Index(path, sockSuffix); idx != -1 {
		end := idx + len(sockSuffix)
		route = path[end:]
		if route == "" {
			route = "/"
		}
		return path[:end], route
	}
	return path, "/"
}

// canonicalizeEntry returns entry with any omni-internal unix:// URL canonicalised.
func canonicalizeEntry(entry HookEntry) HookEntry {
	if entry.Url == nil || *entry.Url == "" {
		return entry
	}
	canon := canonicalizeHookURL(*entry.Url)
	if canon == *entry.Url {
		return entry
	}
	entry.Url = &canon
	return entry
}

// canonicalizeHookEntries canonicalises every entry's URL and drops duplicates
// (matched via hookEntriesEqual). Returns the cleaned slice and whether anything
// changed (a URL was rewritten or a duplicate dropped), so callers can decide
// whether a re-write is needed.
func canonicalizeHookEntries(entries []HookEntry) ([]HookEntry, bool) {
	out := make([]HookEntry, 0, len(entries))
	changed := false
	for _, e := range entries {
		ce := canonicalizeEntry(e)
		if ce.Url != nil && (e.Url == nil || *ce.Url != *e.Url) {
			changed = true // URL was rewritten
		}
		dup := false
		for _, kept := range out {
			if hookEntriesEqual(kept, ce) {
				dup = true
				break
			}
		}
		if dup {
			changed = true // duplicate dropped
			continue
		}
		out = append(out, ce)
	}
	return out, changed
}

// healHooks canonicalises and de-duplicates every event's entries in place.
// Returns true if any event was modified.
func healHooks(hooks map[string][]HookEntry) bool {
	changed := false
	for event, entries := range hooks {
		cleaned, mod := canonicalizeHookEntries(entries)
		if mod {
			hooks[event] = cleaned
			changed = true
		}
	}
	return changed
}

// AddHook adds a HookEntry for the given event name.
// Returns false without writing if an identical entry (same URL or same Command) already exists.
func (r *DefaultOmniConfigResolver) AddHook(eventName string, entry HookEntry) (bool, error) {
	cfg, err := r.GetUserSettings()
	if err != nil {
		return false, err
	}

	if cfg.Agent == nil {
		cfg.Agent = &Settings{}
	}
	if cfg.Agent.Hooks == nil {
		cfg.Agent.Hooks = make(map[string][]HookEntry)
	}

	// Self-heal historical socket-path drift so the file converges to one entry
	// per internal hook rather than accumulating stale variants.
	healed := healHooks(cfg.Agent.Hooks)

	entry = canonicalizeEntry(entry)
	for _, existing := range cfg.Agent.Hooks[eventName] {
		if hookEntriesEqual(existing, entry) {
			if healed {
				return false, r.SaveUserSettings(cfg)
			}
			return false, nil
		}
	}

	cfg.Agent.Hooks[eventName] = append(cfg.Agent.Hooks[eventName], entry)
	return true, r.SaveUserSettings(cfg)
}

// AddHooks adds multiple HookEntries across one or more event names.
// Each entry is skipped if an identical one already exists for that event.
// Returns the count of entries actually added.
func (r *DefaultOmniConfigResolver) AddHooks(hooks map[string][]HookEntry) (int, error) {
	cfg, err := r.GetUserSettings()
	if err != nil {
		return 0, err
	}

	if cfg.Agent == nil {
		cfg.Agent = &Settings{}
	}
	if cfg.Agent.Hooks == nil {
		cfg.Agent.Hooks = make(map[string][]HookEntry)
	}

	// Self-heal historical socket-path drift so the file converges to one entry
	// per internal hook rather than accumulating stale variants.
	healed := healHooks(cfg.Agent.Hooks)

	added := 0
	for eventName, entries := range hooks {
		for _, entry := range entries {
			entry = canonicalizeEntry(entry)
			duplicate := false
			for _, existing := range cfg.Agent.Hooks[eventName] {
				if hookEntriesEqual(existing, entry) {
					duplicate = true
					break
				}
			}
			if !duplicate {
				cfg.Agent.Hooks[eventName] = append(cfg.Agent.Hooks[eventName], entry)
				added++
			}
		}
	}

	if added == 0 && !healed {
		return 0, nil
	}
	return added, r.SaveUserSettings(cfg)
}

// RemoveHook removes all entries matching the given HookEntry from the event.
// Returns true if at least one entry was removed.
func (r *DefaultOmniConfigResolver) RemoveHook(eventName string, entry HookEntry) (bool, error) {
	cfg, err := r.GetUserSettings()
	if err != nil {
		return false, err
	}

	if cfg.Agent == nil || cfg.Agent.Hooks == nil {
		return false, nil
	}

	existing := cfg.Agent.Hooks[eventName]
	filtered := existing[:0]
	for _, e := range existing {
		if !hookEntriesEqual(e, entry) {
			filtered = append(filtered, e)
		}
	}

	if len(filtered) == len(existing) {
		return false, nil
	}

	cfg.Agent.Hooks[eventName] = filtered
	return true, r.SaveUserSettings(cfg)
}

// hookEntriesEqual returns true if two HookEntry values refer to the same hook.
// Matches on URL (for webhook hooks) or Command (for subprocess hooks). URLs are
// compared after canonicalisation so that the same internal hook stored under a
// drifted socket path (different runtime dir or the superseded mcp.sock name) is
// recognised as a duplicate instead of being appended again.
func hookEntriesEqual(a, b HookEntry) bool {
	if a.Url != nil && b.Url != nil {
		return canonicalizeHookURL(*a.Url) == canonicalizeHookURL(*b.Url)
	}
	if a.Command != nil && b.Command != nil {
		return *a.Command == *b.Command
	}
	return false
}
