//go:build darwin

// Package sockpath resolves Unix socket paths for omni services on macOS.
//
// Priority:
//  1. Explicit env var (e.g. OMNI_PTY_SOCKET) — always wins
//  2. $TMPDIR/omni/<uid>/<name> — per-user ephemeral directory (launchd sets TMPDIR)
//  3. /tmp/omni/<uid>/<name> — fallback when TMPDIR is empty
//
// macOS enforces a 104-byte sun_path limit; keeping paths under that limit is
// a hard requirement. The scheme above stays well within the limit.
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

	// macOS: TMPDIR is set per-user by launchd (e.g. /var/folders/xx/.../T/).
	// Fall back to /tmp when TMPDIR is unset (rare, e.g. in CI).
	tmpDir := strings.TrimSpace(os.Getenv("TMPDIR"))
	if tmpDir == "" {
		tmpDir = "/tmp"
	}

	uid := "0"
	if u, err := user.Current(); err == nil && u.Uid != "" {
		uid = u.Uid
	}

	return filepath.Join(tmpDir, "omni", uid, name)
}

// PTY returns the omni-pty socket path, honouring OMNI_PTY_SOCKET.
func PTY() string { return Resolve("OMNI_PTY_SOCKET", NamePTY) }

// HookOperator returns the hook-operator socket path, honouring HOOK_OPERATOR_SOCKET.
func HookOperator() string { return Resolve("HOOK_OPERATOR_SOCKET", NameHookOperator) }

// Service returns the omni service socket path, honouring OMNI_SERVICE_SOCKET.
func Service() string { return Resolve("OMNI_SERVICE_SOCKET", NameService) }
