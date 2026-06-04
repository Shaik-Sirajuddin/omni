//go:build windows

package handoff

// factory_windows.go: the Windows platform constructors.

// NewSender returns the platform HandoffSender. On Windows it transmits the relay
// pipe name over the control conn (the client opens it by name).
func NewSender() HandoffSender { return winSender{} }

// NewWinEndpoint creates a ConPTY of the given size running commandLine and returns
// an Endpoint that relays its output (the B1-safe always-on-reader design). The
// caller drives the lease via Session; SetClientPID must be called (via the
// returned concrete type) before Attach so the relay PID-verifies the client (H4).
func NewWinEndpoint(size Winsize, commandLine string) (Endpoint, error) {
	cp, err := NewWinConPTY(size, commandLine)
	if err != nil {
		return nil, err
	}
	return newWinEndpoint(cp), nil
}
