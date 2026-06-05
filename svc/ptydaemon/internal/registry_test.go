package internal

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// newTestDaemon builds a defaultDaemon backed by a throwaway sqlite store.
func newTestDaemon(t *testing.T) *defaultDaemon {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "pty.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &defaultDaemon{
		store:     store,
		terminals: make(map[string]*PTYTerminal),
	}
}

// createCat starts a long-lived child (`cat` blocks reading its PTY stdin) so the
// terminal stays active for the duration of the test. It registers cleanup.
func createCat(t *testing.T, d *defaultDaemon, agentID, sessionID string) *PTYTerminalInfo {
	t.Helper()
	info, err := d.Create(PTYCreateParams{
		AgentID:   agentID,
		SessionID: sessionID,
		Command:   "cat",
	})
	if err != nil {
		t.Fatalf("Create(%q,%q): %v", agentID, sessionID, err)
	}
	t.Cleanup(func() { _ = d.Stop(agentID, sessionID) })
	return info
}

// TestListFindsEmptyAgentIDTerminal reproduces the exact log bug: a terminal
// created with agent_id="" (Start path, before adopt rewrites the id) must be
// visible to List(realAgentID). The old `WHERE agent_id=?` SQL missed it, so the
// operator's guard looped forever. The fix makes the in-memory registry the
// authority for existence, so ListSessions (behind the client's List(agentID))
// finds the un-adopted session under any requested agent id.
func TestListFindsEmptyAgentIDTerminal(t *testing.T) {
	d := newTestDaemon(t)
	createCat(t, d, "", "sess-1")

	// List with the empty agent id (what the Start path itself would query).
	empty, err := d.ListSessions("")
	if err != nil {
		t.Fatalf("ListSessions(\"\"): %v", err)
	}
	if !containsSession(empty, "sess-1") {
		t.Fatalf("ListSessions(\"\") missing sess-1: %+v", empty)
	}

	// THE FIX: a real agent id must also find the still-un-adopted session.
	real, err := d.ListSessions("real-agent-id")
	if err != nil {
		t.Fatalf("ListSessions(real): %v", err)
	}
	if !containsSession(real, "sess-1") {
		t.Fatalf("ListSessions(\"real-agent-id\") missing the agent_id=\"\" terminal sess-1: %+v", real)
	}
}

// TestGetCreateConsistency asserts that once Create succeeds, the same session is
// immediately resolvable via GetSession and GetMasterFd with an empty agent id —
// no Start→adopt race window where Get disagrees with the in-memory map.
func TestGetCreateConsistency(t *testing.T) {
	d := newTestDaemon(t)
	createCat(t, d, "", "sess-2")

	rec, err := d.GetSession("", "sess-2")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rec == nil {
		t.Fatal("GetSession(\"\",\"sess-2\") returned nil record")
	}
	if rec.Status != StatusActive {
		t.Fatalf("GetSession status = %q, want %q", rec.Status, StatusActive)
	}

	f, err := d.GetMasterFd("", "sess-2")
	if err != nil {
		t.Fatalf("GetMasterFd: %v", err)
	}
	if f == nil {
		t.Fatal("GetMasterFd returned nil file")
	}
}

// TestPauseDrainIsImmediate verifies the drainLoop `continue` fix: pauseDrain
// must return promptly even when the child emits no output. Without the continue
// the loop falls through to a blocking unix.Read on a silent master and only
// parks after the next byte — for a quiet child that is effectively a hang.
func TestPauseDrainIsImmediate(t *testing.T) {
	// `sleep` keeps the slave open but never writes — the pathological quiet case.
	cmd := exec.Command("sleep", "60")
	m, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	term := &PTYTerminal{master: m, cmd: cmd}
	t.Cleanup(func() {
		term.closeMaster()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	term.startDrain()
	// Let the drainer reach its poll so the wake path (not first-iteration) is used.
	time.Sleep(20 * time.Millisecond)

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		term.pauseDrain()
		done <- time.Since(start)
	}()

	select {
	case d := <-done:
		if d > 100*time.Millisecond {
			t.Fatalf("pauseDrain took %v, want ≤100ms (missing drainLoop continue?)", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pauseDrain did not return — drainer blocked on a silent master (continue fix missing)")
	}

	// drainActive must be cleared once paused (read under drainMu to stay race-clean).
	term.drainMu.Lock()
	active := term.drainActive
	term.drainMu.Unlock()
	if active {
		t.Fatal("drainActive still true after pauseDrain")
	}
}

// TestCreateAlreadyExistsError asserts the create guard: the same agentID+
// sessionID key cannot be created twice.
func TestCreateAlreadyExistsError(t *testing.T) {
	d := newTestDaemon(t)
	createCat(t, d, "", "sess-4")

	_, err := d.Create(PTYCreateParams{AgentID: "", SessionID: "sess-4", Command: "cat"})
	if err == nil {
		t.Fatal("second Create with the same key returned nil error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), "already exists")
	}
}

func containsSession(recs []*PTYSessionRecord, sessionID string) bool {
	for _, r := range recs {
		if r.SessionID == sessionID {
			return true
		}
	}
	return false
}
