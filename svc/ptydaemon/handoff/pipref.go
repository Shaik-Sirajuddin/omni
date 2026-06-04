package handoff

import (
	"encoding/json"
	"errors"
	"net"
)

// pipref.go: the Windows HandoffRef and its wire encoding. Both are OS-agnostic
// (pure name + JSON over a net.Conn) so the sender protocol is unit-testable on
// Linux. The actual pipe creation/connect (Win32) lives in //go:build windows.

// pipeRef is the Windows HandoffRef. Unlike the Unix scmRef (which carries a real
// fd), it carries only the relay pipe NAME; the client opens it by name with
// CreateFile. Transport()==TransportNamedPipe.
type pipeRef struct {
	// Name is the full pipe path, e.g. `\\.\pipe\axo-handoff-<uuid>`.
	Name string `json:"name"`
}

func (r *pipeRef) Transport() Transport { return TransportNamedPipe }

// handoffMsg is the JSON control message the daemon sends to the client telling it
// which relay pipe to open. Kept tiny and explicit so it can evolve compatibly.
type handoffMsg struct {
	Transport string `json:"transport"` // "named-pipe"
	Pipe      string `json:"pipe"`
}

// encodeHandoffMsg serialises a pipeRef into the wire form (a single JSON line).
func encodeHandoffMsg(ref *pipeRef) ([]byte, error) {
	b, err := json.Marshal(handoffMsg{Transport: TransportNamedPipe.String(), Pipe: ref.Name})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// decodeHandoffMsg parses what the client receives. Exposed for the client side and
// for round-trip tests.
func decodeHandoffMsg(line []byte) (*pipeRef, error) {
	var m handoffMsg
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	if m.Transport != TransportNamedPipe.String() {
		return nil, errors.New("handoff: unexpected transport in handoff message")
	}
	if m.Pipe == "" {
		return nil, errors.New("handoff: empty pipe name in handoff message")
	}
	return &pipeRef{Name: m.Pipe}, nil
}

// pipeSender is the OS-agnostic HandoffSender body for the Windows relay: it writes
// the relay pipe NAME (JSON) over the control conn. The Windows winSender embeds it;
// keeping it untagged makes the wire protocol unit-testable on Linux with net.Pipe.
type pipeSender struct{}

func (pipeSender) Send(conn net.Conn, ref HandoffRef) error { return writeHandoffMsg(conn, ref) }

var _ HandoffSender = pipeSender{}

// writeHandoffMsg sends the handoff message over the control conn. This is the body
// of winSender.Send and is OS-agnostic (plain conn write), so it is testable on
// Linux with a net.Pipe.
func writeHandoffMsg(conn net.Conn, ref HandoffRef) error {
	pr, ok := ref.(*pipeRef)
	if !ok {
		return errors.New("handoff: windows sender requires a *pipeRef")
	}
	b, err := encodeHandoffMsg(pr)
	if err != nil {
		return err
	}
	_, err = conn.Write(b)
	return err
}
