// Package bpaste detects bracketed-paste mode (DECSET/DECRST 2004) toggles in a
// terminal byte stream.
//
// A TUI announces paste-mode support by emitting ESC[?2004h (enable) / ESC[?2004l
// (disable) on its output. Whoever currently reads the PTY master is the only
// observer of those toggles: the daemon's drainLoop while detached, the attach
// client while it holds the master fd. Both drive this one Scanner so their
// detection — and therefore the framing decision in execPrompt — stays identical.
package bpaste

import "bytes"

var (
	// EnableSeq / DisableSeq are the DECSET/DECRST 2004 control sequences.
	EnableSeq  = []byte("\x1b[?2004h")
	DisableSeq = []byte("\x1b[?2004l")
)

// carrySize is the longest sequence (8 bytes) minus one — the most a match can
// straddle two consecutive reads.
const carrySize = 7

// Scanner tracks bracketed-paste state across successive reads, carrying the
// tail of each chunk so a sequence split across two reads is still detected.
// Not safe for concurrent use — drive it from a single reader goroutine.
type Scanner struct {
	carry  [carrySize]byte
	carryN int
}

// Feed scans b for DECSET/DECRST 2004 markers (including one straddling the
// boundary with the previous chunk) and returns the paste-mode state implied by
// the last marker seen. changed is false when b carries no marker, in which case
// the caller keeps its prior state and on is meaningless.
func (s *Scanner) Feed(b []byte) (on bool, changed bool) {
	if len(b) == 0 {
		return false, false
	}
	// Boundary: previous carry tail + head of this chunk catches a straddling
	// sequence. A marker fully inside b is later in the stream, so b's own scan
	// (run second) wins when both fire.
	if s.carryN > 0 {
		m := len(b)
		if m > carrySize {
			m = carrySize
		}
		edge := make([]byte, 0, s.carryN+m)
		edge = append(edge, s.carry[:s.carryN]...)
		edge = append(edge, b[:m]...)
		if st, ok := scan(edge); ok {
			on, changed = st, true
		}
	}
	if st, ok := scan(b); ok {
		on, changed = st, true
	}
	// Carry the tail for the next read.
	keep := len(b)
	if keep > carrySize {
		keep = carrySize
	}
	copy(s.carry[:], b[len(b)-keep:])
	s.carryN = keep
	return on, changed
}

// scan returns the state of the last enable/disable marker in buf and whether
// any marker was present.
func scan(buf []byte) (on bool, ok bool) {
	onIdx := bytes.LastIndex(buf, EnableSeq)
	offIdx := bytes.LastIndex(buf, DisableSeq)
	if onIdx < 0 && offIdx < 0 {
		return false, false
	}
	return onIdx > offIdx, true
}
