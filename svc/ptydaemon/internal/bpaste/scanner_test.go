package bpaste

import "testing"

// TestScannerFeed verifies DECSET/DECRST 2004 detection: default behaviour, the
// "changed" flag, last-marker-wins within a chunk, and a sequence split across
// two reads.
func TestScannerFeed(t *testing.T) {
	t.Run("no marker leaves state unreported", func(t *testing.T) {
		var s Scanner
		if _, changed := s.Feed([]byte("plain text")); changed {
			t.Fatal("changed should be false when no marker is present")
		}
	})

	t.Run("enable then disable", func(t *testing.T) {
		var s Scanner
		on, changed := s.Feed([]byte("hello \x1b[?2004h world"))
		if !changed || !on {
			t.Fatalf("expected ON+changed, got on=%v changed=%v", on, changed)
		}
		on, changed = s.Feed([]byte("bye \x1b[?2004l now"))
		if !changed || on {
			t.Fatalf("expected OFF+changed, got on=%v changed=%v", on, changed)
		}
	})

	t.Run("last marker wins within a chunk", func(t *testing.T) {
		var s Scanner
		on, changed := s.Feed([]byte("\x1b[?2004l ... \x1b[?2004h"))
		if !changed || !on {
			t.Fatalf("expected ON (enable is last), got on=%v changed=%v", on, changed)
		}
	})

	t.Run("split across two reads", func(t *testing.T) {
		var s Scanner
		seq := []byte("\x1b[?2004h")
		if _, changed := s.Feed(seq[:3]); changed {
			t.Fatal("partial sequence must not report a change yet")
		}
		on, changed := s.Feed(seq[3:])
		if !changed || !on {
			t.Fatalf("expected ON after split completes, got on=%v changed=%v", on, changed)
		}
	})
}
