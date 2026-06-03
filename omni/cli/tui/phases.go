package tui

// Phase represents a named stage in agent initialisation.
type Phase int

const (
	PhaseInit     Phase = iota // resolving agent / binary checks
	PhaseStarting              // PTY Start() called
	PhaseWaiting               // waitPTYReady polling
	PhaseActive                // session live
	PhaseError                 // terminal failure
)

// MsgPhase is sent when the operator moves to a new initialisation stage.
type MsgPhase struct {
	Phase  Phase
	Detail string
}

// MsgReady is sent when the session is confirmed live.
type MsgReady struct {
	AgentName string
	Model     string
	SessionID string
}

// MsgError is sent when initialisation fails.
type MsgError struct {
	Err error
}

