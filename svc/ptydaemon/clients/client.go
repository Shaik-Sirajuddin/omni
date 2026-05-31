package clients

import (
	"context"
	"os"
	"strings"
	"time"
)

// Client is the interface used by operator to communicate with a PTY daemon.
type Client interface {
	Pipe(agentID, sessionID string, data []byte) error
	// Start spawns a new PTY session for the given command.
	// env is forwarded so the daemon can exec user-installed binaries.
	// dir sets the working directory; empty string inherits the daemon's cwd.
	// submitKey is the key sequence used to submit prompts (stored by the daemon for exec retries).
	Start(sessionID string, command []string, env []string, dir, submitKey string) error
	Attach(ctx context.Context, sessionID string) error
	Exec(sessionID, input string) error
	Stop(sessionID string) error
	StopSafe(sessionID string, force bool) error
	Detach(sessionID string) error
	Register(agentID, sessionID, processID string) error

	// List returns all known PTY sessions, optionally filtered by agentID.
	// Pass an empty agentID to return all sessions.
	List(agentID string) ([]*PTYTerminalInfo, error)

	// Get returns the session matching agentID+sessionID, or nil if not found.
	Get(agentID, sessionID string) (*PTYTerminalInfo, error)

	// ListAttached returns all OS processes currently holding the PTY master fd
	// of the given session, identified by /proc inode scan.
	ListAttached(sessionID string) ([]AttachedProcess, error)

	// MetaAttached returns the number of processes holding the PTY master fd.
	// A count of 1 means only the daemon holds it (safe to attach).
	// A count > 1 means another client is already attached.
	MetaAttached(sessionID string) (int, error)
}

// AttachedProcess describes an OS process that holds the PTY master fd.
type AttachedProcess struct {
	PID  int    `json:"pid"`
	Comm string `json:"comm"`
	Fd   int    `json:"fd"`
}

// PTYTerminalInfo is a session descriptor returned by List and Get.
type PTYTerminalInfo struct {
	AgentID   string     `json:"agent_id"`
	SessionID string     `json:"session_id"`
	Status    string     `json:"status"`
	StartedAt time.Time  `json:"started_at"`
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
}

// New returns a Client selected by the OMNI_PTY_CLIENT environment variable.
// "http" → HTTP-over-Unix-socket client (PTYDAEMON_SOCKET).
// anything else → raw Unix socket client (OMNI_PTY_SOCKET).
func New() Client {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OMNI_PTY_CLIENT"))) {
	case "http":
		return newHTTPClient()
	default:
		return newUnixClient()
	}
}
