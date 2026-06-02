package internal

import "testing"

// TestNoteOutputBracketedPaste verifies DECSET/DECRST 2004 detection, including
// the default-off state and a sequence split across two reads.
func TestNoteOutputBracketedPaste(t *testing.T) {
	t.Run("default off", func(t *testing.T) {
		var term PTYTerminal
		if term.bpasteOn.Load() {
			t.Fatal("bpasteOn should default to false")
		}
	})

	t.Run("enable then disable", func(t *testing.T) {
		var term PTYTerminal
		term.noteOutput([]byte("hello \x1b[?2004h world"))
		if !term.bpasteOn.Load() {
			t.Fatal("expected paste mode ON after DECSET 2004")
		}
		term.noteOutput([]byte("bye \x1b[?2004l now"))
		if term.bpasteOn.Load() {
			t.Fatal("expected paste mode OFF after DECRST 2004")
		}
	})

	t.Run("last marker wins within a chunk", func(t *testing.T) {
		var term PTYTerminal
		term.noteOutput([]byte("\x1b[?2004l ... \x1b[?2004h"))
		if !term.bpasteOn.Load() {
			t.Fatal("expected ON: enable is the last marker in the chunk")
		}
	})

	t.Run("split across two reads", func(t *testing.T) {
		var term PTYTerminal
		seq := []byte("\x1b[?2004h")
		term.noteOutput(seq[:3]) // "\x1b[?"
		if term.bpasteOn.Load() {
			t.Fatal("partial sequence must not flip state yet")
		}
		term.noteOutput(seq[3:]) // "2004h"
		if !term.bpasteOn.Load() {
			t.Fatal("expected ON after the split sequence completes")
		}
	})

	t.Run("unrelated output leaves state untouched", func(t *testing.T) {
		var term PTYTerminal
		term.noteOutput([]byte("\x1b[?2004h"))
		term.noteOutput([]byte("plain text, no mode changes"))
		if !term.bpasteOn.Load() {
			t.Fatal("state should persist across chunks without markers")
		}
	})
}
