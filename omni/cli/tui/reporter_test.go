package tui_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	clitui "github.com/Shaik-Sirajuddin/memory/cli/tui"
	"github.com/Shaik-Sirajuddin/memory/operator"
)

// TestReporter_PhaseReady verifies that a Reporter driven through the normal
// init → starting → waiting → ready sequence renders an active panel and
// satisfies operator.StatusReporter.
func TestReporter_PhaseReady(t *testing.T) {
	var buf bytes.Buffer
	r := clitui.NewReporter(&buf)

	// Verify interface satisfaction at compile time.
	var _ operator.StatusReporter = r

	r.PhaseUpdate(operator.InitPhaseResolving, "Resolving agent…")
	r.PhaseUpdate(operator.InitPhaseStarting, "Starting PTY…")
	r.PhaseUpdate(operator.InitPhaseWaiting, "Waiting…")
	r.Ready("testagent", "o4-mini", "abc12345-dead-beef-0000-000000000000")
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "testagent") {
		t.Errorf("expected agent name in output, got:\n%s", out)
	}
	if !strings.Contains(out, "abc12345") {
		t.Errorf("expected session ID prefix in output, got:\n%s", out)
	}
}

// TestReporter_Error verifies that Error() causes the error panel to render.
func TestReporter_Error(t *testing.T) {
	var buf bytes.Buffer
	r := clitui.NewReporter(&buf)

	r.PhaseUpdate(operator.InitPhaseStarting, "Starting…")
	r.Error(errors.New("binary not found"))
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "binary not found") {
		t.Errorf("expected error message in output, got:\n%s", out)
	}
}

// TestReporter_NilSafe verifies that a nil Reporter does not panic.
func TestReporter_NilSafe(t *testing.T) {
	var r *clitui.Reporter
	r.PhaseUpdate(operator.InitPhaseResolving, "detail")
	r.Ready("agent", "model", "session")
	r.Error(errors.New("err"))
}
