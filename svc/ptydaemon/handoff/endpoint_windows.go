//go:build windows

package handoff

import (
	"crypto/rand"
	"encoding/hex"
)

// endpoint_windows.go: the Windows wiring — the HandoffSender (sends the relay pipe
// NAME over the control conn) and the relay pipe naming helper. The Endpoint itself
// is winEndpoint in winendpoint_core.go (OS-agnostic core) backed by winConPTY in
// conpty_windows.go.

// winSender sends the relay pipe name (JSON) to the client over the control conn.
// Unlike Unix's SCM_RIGHTS sender there is no fd to pass — the client opens the
// named pipe by name with CreateFile. The wire format lives in the OS-agnostic
// pipeSender (pipref.go) so it is unit-testable on Linux.
type winSender struct{ pipeSender }

var _ HandoffSender = winSender{}

// newPipeName mints a unique, unguessable relay pipe path. Unguessability is
// defence-in-depth only; the real gate is the SDDL ACL + PID verify (H4).
func newPipeName() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return `\\.\pipe\axo-handoff-` + hex.EncodeToString(b[:])
}
