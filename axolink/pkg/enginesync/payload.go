// Package enginesync defines the wire contract and a client for pushing
// session-sync updates to the processing engine over its unix socket.
//
// The engine runs a SyncServer (see axolink/engine/sync.go) listening on the
// path returned by sockpath.EngineSync(). The operator (or any out-of-process
// component holding live session telemetry) imports this package, constructs a
// Client, and calls SyncSession/SyncUsage to keep the engine's in-memory agent
// state current without coupling to the engine package itself.
package enginesync

// Kind discriminates the action carried by a Payload. An empty Kind is treated
// as KindSessionSync for backward compatibility.
type Kind string

const (
	// KindSessionSync pushes session-usage telemetry for an agent.
	KindSessionSync Kind = "session_sync"
	// KindResume asks the engine to resume an agent: clear its interrupted flag
	// and re-trigger delivery (which hydrates the agent from the store if it is
	// not yet in the engine's in-memory state). Only Session is required.
	KindResume Kind = "resume"
)

// Payload is the JSON body sent to the engine on each engine-sync call.
// This is the single source of truth for the wire contract: the engine's
// SyncServer decodes into this type directly, and the operator's Client encodes
// it, so both sides stay in lockstep without sharing engine-internal types.
type Payload struct {
	Kind         Kind         `json:"kind,omitempty"`
	Session      string       `json:"session"`                // agent ID
	SessionUsage SessionUsage `json:"session_usage,omitempty"` // populated for KindSessionSync
}

// SessionUsage carries token-consumption telemetry for a session.
type SessionUsage struct {
	Consumed        ConsumedUsage    `json:"consumed"`
	Max             map[string]int64 `json:"max"`
	ConsumedPercent float64          `json:"consumed_percent"`
}

// ConsumedUsage is the per-category token breakdown for a session.
type ConsumedUsage struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CachedInput  int64 `json:"cached_input"`
	CachedOutput int64 `json:"cached_output"`
}
