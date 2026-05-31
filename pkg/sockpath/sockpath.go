// Package sockpath resolves Unix socket paths for omni services, handling both
// system-wide installs (/run/omni-<user>/) and user-local installs
// ($XDG_RUNTIME_DIR/omni/ = /run/user/<uid>/omni/).
//
// Priority:
//  1. Explicit env var (e.g. OMNI_PTY_SOCKET) — always wins; set by systemd unit
//  2. $XDG_RUNTIME_DIR/omni/<name> — user-local install
//  3. /run/user/<uid>/omni/<name> — user-local without XDG_RUNTIME_DIR
//  4. /run/omni-<username>/<name> — system-wide install (legacy default)
package sockpath

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

const (
	NamePTY          = "omni-pty.sock"
	NameHookOperator = "hook-operator.sock"
	NameService      = "service.sock"
	NameMCP          = "mcp.sock"
)

// Resolve returns the socket path for the given service socket name.
// envVar is the environment variable name to check first (e.g. "OMNI_PTY_SOCKET").
func Resolve(envVar, name string) string {
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v
	}

	// user-local: XDG_RUNTIME_DIR is set by systemd-logind / pam_systemd
	if xdg := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); xdg != "" {
		return filepath.Join(xdg, "omni", name)
	}

	// user-local: derive from UID when XDG_RUNTIME_DIR isn't exported
	if u, err := user.Current(); err == nil && u.Uid != "" {
		candidate := filepath.Join("/run/user", u.Uid, "omni", name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// system-wide fallback
	if u, err := user.Current(); err == nil && u.Username != "" {
		return filepath.Join("/run", "omni-"+u.Username, name)
	}
	return filepath.Join("/run", "omni-root", name)
}

// PTY returns the omni-pty socket path, honouring OMNI_PTY_SOCKET.
func PTY() string { return Resolve("OMNI_PTY_SOCKET", NamePTY) }

// HookOperator returns the hook-operator socket path, honouring HOOK_OPERATOR_SOCKET.
func HookOperator() string { return Resolve("HOOK_OPERATOR_SOCKET", NameHookOperator) }

// Service returns the omni service socket path, honouring OMNI_SERVICE_SOCKET.
func Service() string { return Resolve("OMNI_SERVICE_SOCKET", NameService) }
