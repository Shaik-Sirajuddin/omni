package clients

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/svc/ptydaemon/internal"
	"github.com/Shaik-Sirajuddin/memory/svc/ptydaemon/ptyunix"
)

// TestListRoundTripIncludesEmptyAgentID is the END-TO-END regression for the
// Start→adopt loop: it drives a REAL UnixSocketClient against a REAL ptyunix
// server and asserts the client does NOT strip agent_id="" terminals back out
// after the server correctly includes them. Without the unix.go filter fix the
// empty-agent terminal is dropped client-side and Scenario A fails.
func TestListRoundTripIncludesEmptyAgentID(t *testing.T) {
	dir, err := os.MkdirTemp("", "pd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	store, err := internal.NewStore(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	inner := internal.NewDaemon(store)

	// A Start-path terminal (agent_id="" — adopt has not run yet) and a fully
	// adopted terminal for a DIFFERENT real agent.
	if _, err := inner.Create(internal.PTYCreateParams{AgentID: "", SessionID: "sess-empty", Command: "cat"}); err != nil {
		t.Fatalf("Create(empty): %v", err)
	}
	t.Cleanup(func() { _ = inner.Stop("", "sess-empty") })
	if _, err := inner.Create(internal.PTYCreateParams{AgentID: "agent-A", SessionID: "sess-A", Command: "cat"}); err != nil {
		t.Fatalf("Create(agent-A): %v", err)
	}
	t.Cleanup(func() { _ = inner.Stop("agent-A", "sess-A") })

	// Serve on a temp unix socket.
	srv := ptyunix.NewDaemonWithInner(inner)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	socket := filepath.Join(dir, "s.sock")
	go func() { _ = srv.ListenAndServe(ctx, socket) }()
	waitListening(t, socket)

	c := NewUnixSocketClient(socket)

	// Scenario A — the original bug: List(realAgentID) must include the
	// still-un-adopted agent_id="" terminal.
	got, err := c.List("real-agent-id")
	if err != nil {
		t.Fatalf("List(real): %v", err)
	}
	if !hasSession(got, "sess-empty") {
		t.Fatalf("List(\"real-agent-id\") dropped the agent_id=\"\" terminal sess-empty: %+v", got)
	}

	// Scenario B — no cross-contamination: a terminal owned by a DIFFERENT,
	// non-empty agent must NOT leak into another agent's List. (Leniency is
	// scoped to agent_id="" only; an exact foreign agent_id is still excluded.)
	if hasSession(got, "sess-A") {
		t.Fatalf("List(\"real-agent-id\") leaked agent-A's terminal sess-A: %+v", got)
	}

	// And the operator guard, which matches by SessionID, would correctly ignore
	// the shared agent_id="" entry when hunting for a different session id.
	if hasSession(got, "sess-does-not-exist") {
		t.Fatal("List returned a session id that was never created")
	}
}

func waitListening(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ptyunix server did not start listening in time")
}

func hasSession(infos []*PTYTerminalInfo, sessionID string) bool {
	for _, i := range infos {
		if i != nil && i.SessionID == sessionID {
			return true
		}
	}
	return false
}
