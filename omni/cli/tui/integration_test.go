package tui_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	clitui "github.com/Shaik-Sirajuddin/memory/cli/tui"
	"github.com/Shaik-Sirajuddin/memory/operator"
)

// drive applies msgs in sequence to a fresh Model and returns the final tea.Model.
func drive(msgs ...tea.Msg) tea.Model {
	var m tea.Model = clitui.NewModel()
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	return m
}

// TestView_PhaseProgression verifies the full Init→Starting→Waiting→Ready phase
// sequence. After MsgReady, View() must return "" so bubbletea erases the
// spinner frame and leaves the terminal clean for the PTY to take over.
func TestView_PhaseProgression(t *testing.T) {
	m := drive(
		clitui.MsgPhase{Phase: clitui.PhaseInit, Detail: "Resolving agent…"},
		clitui.MsgPhase{Phase: clitui.PhaseStarting, Detail: "Starting PTY…"},
		clitui.MsgPhase{Phase: clitui.PhaseWaiting, Detail: "Waiting…"},
		clitui.MsgReady{AgentName: "myagent", Model: "o4-mini", SessionID: "abc12345-dead-beef-0000-000000000000"},
	)
	if got := m.View(); got != "" {
		t.Errorf("View() after MsgReady must be empty for clean PTY handoff, got:\n%s", got)
	}
}

// TestView_ErrorPath verifies the Init→Starting→Error path and that View() shows
// the error box.
func TestView_ErrorPath(t *testing.T) {
	m := drive(
		clitui.MsgPhase{Phase: clitui.PhaseStarting, Detail: "Starting…"},
		clitui.MsgError{Err: errors.New("binary not found")},
	)
	view := m.View()
	for _, want := range []string{"✗ Initialisation failed", "binary not found"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() missing %q:\n%s", want, view)
		}
	}
}

// TestView_Ready_Fields verifies that View() after MsgReady returns "" so the
// TUI erases itself cleanly before the PTY takes over. Fields passed to
// MsgReady are stored in the model but not rendered (PTY handles its own UI).
func TestView_Ready_Fields(t *testing.T) {
	const (
		agentName = "testagent"
		modelName = "claude-3-sonnet"
		sessionID = "deadbeef-1234-5678-9abc-000000000000"
	)
	m := drive(clitui.MsgReady{AgentName: agentName, Model: modelName, SessionID: sessionID})
	if got := m.View(); got != "" {
		t.Errorf("View() after MsgReady must be empty for clean PTY handoff, got:\n%s", got)
	}
}

// TestView_Error_Fields verifies that View() after MsgError contains the failure
// marker and the error message text.
func TestView_Error_Fields(t *testing.T) {
	// Keep under the 52-char content width of the box to avoid word-wrap splitting.
	errMsg := "binary not found: /usr/local/bin/myagent"
	m := drive(clitui.MsgError{Err: errors.New(errMsg)})
	view := m.View()
	for _, want := range []string{"✗ Initialisation failed", errMsg} {
		if !strings.Contains(view, want) {
			t.Errorf("View() missing %q:\n%s", want, view)
		}
	}
}

// TestReporter_NilWait verifies that Wait() on a nil Reporter returns immediately
// without blocking or panicking.
func TestReporter_NilWait(t *testing.T) {
	var r *clitui.Reporter
	r.Wait()
}

// TestReporter_NilAllMethods verifies every Reporter method is safe on a nil receiver.
func TestReporter_NilAllMethods(t *testing.T) {
	var r *clitui.Reporter
	r.PhaseUpdate(operator.InitPhaseResolving, "detail")
	r.PhaseUpdate(operator.InitPhaseStarting, "starting")
	r.PhaseUpdate(operator.InitPhaseWaiting, "waiting")
	r.Ready("agent", "model", "session-id")
	r.Error(errors.New("err"))
	r.Wait()
}

// TestReporter_WaitAfterReady verifies that a second Wait() call after Ready()
// returns promptly and does not block.
func TestReporter_WaitAfterReady(t *testing.T) {
	var buf bytes.Buffer
	r := clitui.NewReporter(&buf)

	r.PhaseUpdate(operator.InitPhaseStarting, "Starting…")
	r.Ready("agent", "o4-mini", "abc12345-0000-0000-0000-000000000000")
	r.Wait()

	done := make(chan struct{})
	go func() {
		r.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second Wait() did not return after Ready()")
	}
}
