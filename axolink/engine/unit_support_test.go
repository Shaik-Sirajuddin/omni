//go:build unit

package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ─── localBlockingCLI ─────────────────────────────────────────────────────────

// localBlockingCLI captures each ExecInSession call and blocks until released.
type localBlockingCLI struct {
	mu      sync.Mutex
	execs   []string
	prompts []string
	execCh  chan localExecEntry
}

type localExecEntry struct {
	agentID string
	prompt  string
	relCh   chan struct{}
}

func newLocalBlockingCLI() *localBlockingCLI {
	return &localBlockingCLI{execCh: make(chan localExecEntry, 10)}
}

func (c *localBlockingCLI) ExecInSession(_ context.Context, agentID, _, _, prompt string) error {
	rel := make(chan struct{})
	c.mu.Lock()
	c.execs = append(c.execs, agentID)
	c.prompts = append(c.prompts, prompt)
	c.mu.Unlock()
	c.execCh <- localExecEntry{agentID: agentID, prompt: prompt, relCh: rel}
	<-rel
	return nil
}

func (c *localBlockingCLI) GetPromptState(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (c *localBlockingCLI) waitForExec(t *testing.T) localExecEntry {
	t.Helper()
	select {
	case e := <-c.execCh:
		return e
	case <-time.After(2 * time.Second):
		require.FailNow(t, "ExecInSession not called within timeout")
		return localExecEntry{}
	}
}

// ─── TestSessionGenerationGuard ───────────────────────────────────────────────
