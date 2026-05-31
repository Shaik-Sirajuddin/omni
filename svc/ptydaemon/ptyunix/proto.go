// Package ptyunix implements the raw Unix socket protocol for the PTY daemon.
package ptyunix

// Request is the JSON envelope sent by clients over the Unix socket.
type Request struct {
	Op        string   `json:"op"`
	AgentID   string   `json:"agent_id,omitempty"`
	SessionID string   `json:"session_id"`
	Command   []string `json:"command,omitempty"`
	Input     string   `json:"input,omitempty"`
	Key       string   `json:"key,omitempty"`
	Env       []string `json:"env,omitempty"`
	Dir       string   `json:"dir,omitempty"`
	SubmitKey string   `json:"submit_key,omitempty"`
	Data      []byte   `json:"data,omitempty"`
	PID       int      `json:"pid,omitempty"`
	// Safe and Force apply to the stop op.
	// Safe=true enables the attachment check; Force=true overrides it.
	// Omitting both preserves the original unconditional-kill behaviour.
	Safe  bool `json:"safe,omitempty"`
	Force bool `json:"force,omitempty"`
}

// Response is the JSON envelope returned to clients.
type Response struct {
	OK        bool           `json:"ok"`
	SessionID string         `json:"session_id,omitempty"`
	Error     string         `json:"error,omitempty"`
	Sessions  []SessionEntry `json:"sessions,omitempty"`
	// Count is set for meta-attached responses.
	Count int `json:"count,omitempty"`
	// Processes is set for list-attached responses.
	Processes []AttachedProcess `json:"processes,omitempty"`
}

// SessionEntry is a single session record returned in list/get responses.
// Field names match PTYTerminalInfo JSON tags so clients decode into the same type.
type SessionEntry struct {
	AgentID   string  `json:"agent_id,omitempty"`
	SessionID string  `json:"session_id"`
	Status    string  `json:"status"`
	StartedAt *string `json:"started_at,omitempty"`
	StoppedAt *string `json:"stopped_at,omitempty"`
}

// AttachedProcess describes a OS process that currently holds the PTY master fd.
type AttachedProcess struct {
	PID  int    `json:"pid"`
	Comm string `json:"comm"`
	Fd   int    `json:"fd"`
}

var keybinds = map[string][]byte{
	"ctrl+c": {0x03},
	"ctrl+d": {0x04},
	"ctrl+z": {0x1a},
	"ctrl+l": {0x0c},
	"ctrl+r": {0x12},
	"enter":  {0x0d},
	"escape": {0x1b},
	"tab":    {0x09},
}
