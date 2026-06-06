//go:build e2e

package pty_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPTYResumeColdStart (T1) verifies that resume on a fresh agent starts a
// PTY session and returns exit 0 in detach mode.
func TestPTYResumeColdStart(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := harness.RequireProvider(t, cfg)

	agentName := "e2e-pty-cold-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	// Ensure stopped
	harness.RunOmniAllowFail(t, cfg, "agent", "stop", agentName)
	time.Sleep(time.Second)

	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "resume", agentName, "--detach", "--workspace", cfg.Workspace)
	require.Equal(t, 0, code, "resume cold-start must exit 0: %s", out)

	// PTY session started
	started := jrnl.WaitFor("PTY daemon session started", 30*time.Second) ||
		jrnl.WaitFor("session created", 30*time.Second) ||
		jrnl.WaitFor("session ready", 30*time.Second)
	assert.True(t, started, "PTY session must start after resume cold-start")
}

// TestPTYResumeIdempotent (T2) verifies that calling resume twice does not
// create a duplicate PTY session (idempotency).
func TestPTYResumeIdempotent(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, omniLog, "")

	provider := harness.RequireProvider(t, cfg)

	agentName := "e2e-pty-idem-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	// First resume
	out1, code1 := harness.RunOmniAllowFail(t, cfg,
		"agent", "resume", agentName, "--detach", "--workspace", cfg.Workspace)
	require.Equal(t, 0, code1, "first resume must exit 0: %s", out1)
	time.Sleep(3 * time.Second)

	// Second resume — must not crash or duplicate
	out2, code2 := harness.RunOmniAllowFail(t, cfg,
		"agent", "resume", agentName, "--detach", "--workspace", cfg.Workspace)
	assert.Equal(t, 0, code2, "second resume must exit 0 (idempotent): %s", out2)

	log := jrnl.String() + omniLog.String()
	assert.False(t, strings.Contains(log, "duplicate session") ||
		strings.Contains(log, "already exists"),
		"no duplicate session error after double resume")
	harness.AssertNoExecSessionFailed(t, log)
}

// TestPTYResumeAfterStop (T3) verifies that resume after agent stop restarts
// the PTY session cleanly.
func TestPTYResumeAfterStop(t *testing.T) {
	cfg := harness.NewConfig(t)
	_, jrnl := harness.CaptureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)
	defer harness.DumpLogsOnFailure(t, jrnl, nil, "")

	provider := harness.RequireProvider(t, cfg)

	agentName := "e2e-pty-restart-" + harness.AgentNameSuffix(t)
	t.Cleanup(func() { harness.TeardownAgent(t, cfg, agentName) })

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	harness.RunOmni(t, cfg, "agent", "init", agentName,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	// Start then stop
	harness.RunOmniAllowFail(t, cfg,
		"agent", "resume", agentName, "--detach", "--workspace", cfg.Workspace)
	time.Sleep(3 * time.Second)
	harness.RunOmniAllowFail(t, cfg, "agent", "stop", agentName)
	time.Sleep(2 * time.Second)

	// Resume after stop
	out, code := harness.RunOmniAllowFail(t, cfg,
		"agent", "resume", agentName, "--detach", "--workspace", cfg.Workspace)
	require.Equal(t, 0, code, "resume after stop must exit 0: %s", out)

	restarted := jrnl.WaitFor("session created", 20*time.Second) ||
		jrnl.WaitFor("session ready", 20*time.Second) ||
		jrnl.WaitFor("PTY daemon session started", 20*time.Second)
	assert.True(t, restarted, "PTY session must restart after stop+resume")
}

