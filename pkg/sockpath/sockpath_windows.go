//go:build windows

// Package sockpath resolves named-pipe / socket paths for omni services on Windows.
//
// Priority:
//  1. Explicit env var (e.g. OMNI_PTY_SOCKET) — always wins
//  2. %LOCALAPPDATA%\omni\<name> — per-user local app data directory
//  3. %APPDATA%\omni\<name> — roaming app data fallback
//  4. %TEMP%\omni\<name> — last-resort fallback
package sockpath

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	NamePTY          = "omni-pty.sock"
	NameHookOperator = "hook-operator.sock"
	NameService      = "service.sock"
	NameMCP          = "mcp.sock"
	NameEngineSync   = "engine-sync.sock"
)

// Resolve returns the socket path for the given service socket name.
// envVar is the environment variable name to check first (e.g. "OMNI_PTY_SOCKET").
func Resolve(envVar, name string) string {
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v
	}

	// Prefer LocalAppData (non-roaming, per-machine user data).
	if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
		return filepath.Join(local, "omni", name)
	}

	// Fall back to roaming AppData.
	if appdata := strings.TrimSpace(os.Getenv("APPDATA")); appdata != "" {
		return filepath.Join(appdata, "omni", name)
	}

	// Last resort: system temp directory.
	return filepath.Join(os.TempDir(), "omni", name)
}

// PTY returns the omni-pty socket path, honouring OMNI_PTY_SOCKET.
func PTY() string { return Resolve("OMNI_PTY_SOCKET", NamePTY) }

// HookOperator returns the hook-operator socket path, honouring HOOK_OPERATOR_SOCKET.
func HookOperator() string { return Resolve("HOOK_OPERATOR_SOCKET", NameHookOperator) }

// Service returns the omni service socket path, honouring OMNI_SERVICE_SOCKET.
func Service() string { return Resolve("OMNI_SERVICE_SOCKET", NameService) }

// EngineSync returns the engine session-sync socket path, honouring OMNI_ENGINE_SYNC_SOCKET.
// The processing engine listens here; the operator dials it to push session-sync updates.
func EngineSync() string { return Resolve("OMNI_ENGINE_SYNC_SOCKET", NameEngineSync) }
