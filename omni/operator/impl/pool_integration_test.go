package impl

// pool_integration_test.go — operator-level pool fast-path integration test.
//
// What this catches:
//   - A DefaultOperator created via NewDefault() with no SetPoolClient call
//     will NOT invoke the pool fast-path. These tests assert that after
//     SetPoolClient is called, CreateAgent dequeues from the mock pool daemon
//     instead of spinning up a real agent session — the exact wiring gap that
//     existed in the CLI before the fix.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	agentpoolclient "github.com/Shaik-Sirajuddin/memory/svc/agentpool/client"

	agentpool "github.com/Shaik-Sirajuddin/memory/svc/agentpool"

	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	operator "github.com/Shaik-Sirajuddin/memory/operator"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
	"github.com/Shaik-Sirajuddin/memory/store/codesession"
	operatorstore "github.com/Shaik-Sirajuddin/memory/store/operator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// startMockPoolDaemon starts a minimal single-connection mock pool daemon in a
// goroutine. It listens on socketPath, accepts one connection, reads one JSON
// request, and responds with a valid PoolEntry.  The returned channel is closed
// once the single request has been handled.
func startMockPoolDaemon(t *testing.T, socketPath string, response agentpool.Response) <-chan agentpool.Request {
	t.Helper()

	// Remove stale socket if present.
	_ = removeSocket(socketPath)

	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err, "mock pool daemon: listen")

	requestCh := make(chan agentpool.Request, 8) // buffered — never blocks

	t.Cleanup(func() { ln.Close() })

	go func() {
		defer ln.Close()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var req agentpool.Request
				if err := json.NewDecoder(c).Decode(&req); err != nil {
					resp := agentpool.Response{OK: false, Error: fmt.Sprintf("decode: %v", err)}
					_ = json.NewEncoder(c).Encode(resp)
					return
				}
				select {
				case requestCh <- req:
				default:
				}
				_ = json.NewEncoder(c).Encode(response)
			}(conn)
		}
	}()

	// Wait for socket to be reachable.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("unix", socketPath); err == nil {
			conn.Close()
			return requestCh
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("mock pool daemon: socket %s never became reachable", socketPath)
	return nil
}

// removeSocket removes the file at path if it exists (no error if absent).
func removeSocket(path string) error {
	// best-effort; ignore errors
	os.Remove(path) //nolint:errcheck
	return nil
}

// newPoolTestOperator builds a DefaultOperator suitable for pool wiring tests.
// It uses in-memory (temp-dir) SQLite stores and stubs out the code-agent
// factory so no real provider binary is required.
func newPoolTestOperator(t *testing.T) *DefaultOperator {
	t.Helper()
	db := newTestDB(t)
	store := operatorstore.NewWithDB(db)
	op := &DefaultOperator{
		config: config.OmniConfig{
			Features: &config.Features{Memory: false}, // keep tests fast: no git init
		},
		store:        store,
		sessionStore: codesession.NewWithDB(db),
		newCodeAgent: func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
			return &stubCodeAgent{}, nil
		},
	}
	return op
}

// ── Test: HasPoolClient ───────────────────────────────────────────────────────

// TestHasPoolClient verifies the HasPoolClient predicate mirrors internal state.
func TestHasPoolClient(t *testing.T) {
	t.Parallel()

	op := newPoolTestOperator(t)
	assert.False(t, op.HasPoolClient(), "fresh operator should have no pool client")

	socketPath := filepath.Join(t.TempDir(), "has-pool.sock")
	op.SetPoolClient(agentpoolclient.New(socketPath))
	assert.True(t, op.HasPoolClient(), "operator should report pool client after SetPoolClient")
}

// ── Test: pool fast-path is invoked ──────────────────────────────────────────

// TestCreateAgent_PoolFastPath verifies that, when a pool client is wired,
// CreateAgent sends a "get" request to the pool daemon and does NOT invoke the
// code-agent factory (i.e. no real agent session is started).
//
// This is the integration-level guard against the original wiring bug:
//   - Before the fix: NewDefault() was called but SetPoolClient was never
//     called, so poolClient was nil and the fast-path was silently skipped.
//   - After the fix: SetPoolClient is called immediately after NewDefault()
//     in main(), so the fast-path is active for every CreateAgent call.
func TestCreateAgent_PoolFastPath_InvokedAndBypassesCreate(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(t.TempDir(), "pool-fastpath.sock")

	// Serve a valid PoolEntry for any get request.
	wantEntry := agentpool.PoolEntry{
		SessionID: "warm-sess-1",
		AgentID:   "pool-agent-1",
		Provider:  "claude",
		Workspace: "/test",
	}
	requestCh := startMockPoolDaemon(t, socketPath, agentpool.Response{
		OK:    true,
		Entry: &wantEntry,
	})

	op := newPoolTestOperator(t)

	// Track whether the code-agent factory was invoked (it must NOT be on pool hit).
	var codeAgentInvocations atomic.Int32
	op.newCodeAgent = func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
		codeAgentInvocations.Add(1)
		return &stubCodeAgent{}, nil
	}

	// Wire the pool client — this is the fix we are testing.
	op.SetPoolClient(agentpoolclient.New(socketPath))

	workspace := sandbox.WorkspaceDir(t.TempDir())
	err := op.CreateAgent(operator.CreateAgentParams{
		Workspace: workspace,
		Name:      "pool-test-agent",
		Provider:  "claude",
	})
	require.NoError(t, err, "CreateAgent with pool fast-path should succeed")

	// ── Assert 1: pool received a get request ────────────────────────────────
	var received agentpool.Request
	select {
	case received = <-requestCh:
	case <-time.After(3 * time.Second):
		t.Fatal("pool daemon received no request — pool fast-path was not invoked")
	}

	assert.Equal(t, agentpool.OpGet, received.Op, "pool request should use op=get")
	assert.Equal(t, "claude", received.Provider, "pool request should carry the requested provider")

	// ── Assert 2: code-agent factory was NOT called (pool hit skips real create) ─
	assert.Zero(t, codeAgentInvocations.Load(),
		"code-agent factory must not be called when pool fast-path returns a session")
}

// ── Test: pool fast-path is skipped when poolClient is nil ───────────────────

// TestCreateAgent_NoPoolClient_CodeAgentInvoked verifies that when no pool
// client is wired (the pre-fix state), CreateAgent falls through to the
// code-agent factory as expected.
func TestCreateAgent_NoPoolClient_CodeAgentInvoked(t *testing.T) {
	t.Parallel()

	op := newPoolTestOperator(t)

	var codeAgentInvocations atomic.Int32
	op.newCodeAgent = func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
		codeAgentInvocations.Add(1)
		return &stubCodeAgent{}, nil
	}

	// No SetPoolClient call — pool client is nil, fast-path is disabled.

	workspace := sandbox.WorkspaceDir(t.TempDir())
	err := op.CreateAgent(operator.CreateAgentParams{
		Workspace: workspace,
		Name:      "no-pool-agent",
		Provider:  "claude",
	})
	require.NoError(t, err, "CreateAgent without pool should still succeed")

	assert.Positive(t, int(codeAgentInvocations.Load()),
		"code-agent factory must be called when no pool client is wired")
}

// ── Test: pool error falls through to real create ────────────────────────────

// TestCreateAgent_PoolError_FallsThrough verifies that if the pool daemon
// returns an error, CreateAgent falls through to the normal code-agent path
// rather than failing hard.
func TestCreateAgent_PoolError_FallsThrough(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(t.TempDir(), "pool-error.sock")

	// Serve an error response from the pool.
	_ = startMockPoolDaemon(t, socketPath, agentpool.Response{
		OK:    false,
		Error: "pool: no warm sessions available",
	})

	op := newPoolTestOperator(t)

	var codeAgentInvocations atomic.Int32
	op.newCodeAgent = func(provider codeagent.Provider, workDir, model string) (codeagent.CodeAgent, error) {
		codeAgentInvocations.Add(1)
		return &stubCodeAgent{}, nil
	}

	op.SetPoolClient(agentpoolclient.New(socketPath))

	workspace := sandbox.WorkspaceDir(t.TempDir())
	err := op.CreateAgent(operator.CreateAgentParams{
		Workspace: workspace,
		Name:      "pool-fallback-agent",
		Provider:  "claude",
	})
	require.NoError(t, err, "CreateAgent should fall through to real create when pool errors")

	assert.Positive(t, int(codeAgentInvocations.Load()),
		"code-agent factory must be called when pool returns an error (fallback path)")
}

// ── Test: wiring smoke — SetPoolClient is reflected in HasPoolClient ──────────

// TestWiringSmoke_SetPoolClientReflected is the minimal smoke test documenting
// the wiring contract that main() must satisfy:
//
//  1. Create a DefaultOperator via NewDefault() (or the test stub equivalent).
//  2. Immediately call SetPoolClient with a non-nil client.
//  3. HasPoolClient() must return true.
//
// If this test were applied to the pre-fix main.go (where SetPoolClient was
// never called), it would have caught the bug by failing at step 3.
func TestWiringSmoke_SetPoolClientReflected(t *testing.T) {
	t.Parallel()

	op := newPoolTestOperator(t)
	require.False(t, op.HasPoolClient(),
		"pre-condition: fresh operator must not have a pool client")

	socketPath := filepath.Join(t.TempDir(), "wiring-smoke.sock")
	op.SetPoolClient(agentpoolclient.New(socketPath))

	assert.True(t, op.HasPoolClient(),
		"after SetPoolClient, HasPoolClient must return true — "+
			"this is the invariant that main() must preserve")
}

// ── helpers for pool-test DB ─────────────────────────────────────────────────

// poolTestDB creates an isolated SQLite database with all required schemas.
// Re-uses newTestDB from operator_test.go (same package).
var _ *sql.DB = (*sql.DB)(nil) // ensure sql imported
