package hookoperator

import (
	"path/filepath"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/pkg/sockpath"
)

const unixScheme = "unix://"

// canonicalHookSocket is the single unix socket the operator dispatches internal
// hooks to. All omni-internal unix:// hook URLs are collapsed onto this one path
// (see normalizeHookURL), so stale entries left behind in config.json don't make
// the same event fan out to several dead sockets. The engine serves /hook on the
// service socket, so that is the canonical target.
func canonicalHookSocket() string { return sockpath.Service() }

// isOmniSocketName reports whether name is a socket basename owned by omni. Hook
// URLs pointing at any of these — under any runtime dir (/run/omni-<user>/ vs
// $XDG_RUNTIME_DIR/omni/) or under an older socket name (mcp.sock) — all refer to
// the same internal /hook consumer and are collapsed onto canonicalHookSocket().
func isOmniSocketName(name string) bool {
	switch name {
	case sockpath.NameService, sockpath.NameMCP:
		return true
	}
	return false
}

// normalizeHookURL rewrites an omni-internal unix:// hook URL onto the canonical
// service socket, preserving its HTTP route. Non-unix URLs and unix sockets that
// aren't omni-owned (third-party hook consumers) are returned unchanged.
func normalizeHookURL(raw string) string {
	if !strings.HasPrefix(raw, unixScheme) {
		return raw
	}
	socketPath, route := splitUnixURL(strings.TrimPrefix(raw, unixScheme))
	if !isOmniSocketName(filepath.Base(socketPath)) {
		return raw
	}
	return unixScheme + canonicalHookSocket() + route
}

// hookEntriesFromConfig extracts the hook entries map from an OmniConfig.
// Returns an empty map when the config has no agent hooks section.
//
// Internal unix:// hook URLs are canonicalised onto the single service socket and
// de-duplicated per event, so that historical drift in the config (multiple
// runtime-dir conventions, the superseded mcp.sock name) collapses to exactly one
// socket call per hook at dispatch time.
func hookEntriesFromConfig(cfg *config.OmniConfig) map[string][]config.HookEntry {
	out := map[string][]config.HookEntry{}
	if cfg == nil || cfg.Agent == nil || cfg.Agent.Hooks == nil {
		return out
	}
	for eventName, entries := range cfg.Agent.Hooks {
		if len(entries) == 0 {
			continue
		}
		deduped := make([]config.HookEntry, 0, len(entries))
		seen := map[string]struct{}{}
		for _, e := range entries {
			cp := e
			if e.Url != nil && *e.Url != "" {
				norm := normalizeHookURL(*e.Url)
				if _, dup := seen[norm]; dup {
					continue
				}
				seen[norm] = struct{}{}
				if norm != *e.Url {
					u := norm
					cp.Url = &u
				}
			}
			deduped = append(deduped, cp)
		}
		if len(deduped) == 0 {
			continue
		}
		out[eventName] = deduped
	}
	return out
}
