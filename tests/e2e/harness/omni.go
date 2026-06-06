//go:build e2e

package harness

import (
	"context"
	"strings"
	"testing"
)

// RunOmni executes an omni subcommand and returns the output.
// Fails the test if the exit code is non-zero.
func RunOmni(t *testing.T, cfg TestConfig, args ...string) string {
	t.Helper()
	cmd := append([]string{cfg.OmniPath}, args...)
	exitCode, out, err := cfg.Exec.RunCommand(context.Background(), cmd)
	t.Logf("omni %v → exit=%d\n%s", args, exitCode, strings.TrimSpace(string(out)))
	if err != nil || exitCode != 0 {
		t.Fatalf("omni %v failed (exit=%d): %s", args, exitCode, out)
	}
	return string(out)
}

// RunOmniAllowFail runs an omni subcommand without failing the test.
// Returns combined output and exit code. Use for teardown and negative assertions.
func RunOmniAllowFail(t *testing.T, cfg TestConfig, args ...string) (string, int) {
	t.Helper()
	cmd := append([]string{cfg.OmniPath}, args...)
	exitCode, out, _ := cfg.Exec.RunCommand(context.Background(), cmd)
	t.Logf("omni %v → exit=%d\n%s", args, exitCode, strings.TrimSpace(string(out)))
	return string(out), exitCode
}

// TeardownAgent deletes an agent by name. Safe to call in t.Cleanup.
func TeardownAgent(t *testing.T, cfg TestConfig, name string) {
	t.Helper()
	t.Logf("teardown: deleting agent %q", name)
	_, _ = RunOmniAllowFail(t, cfg, "agent", "delete", name, "--workspace", cfg.Workspace)
}

// ResumeAgentBackground runs `omni agent resume <name>` in a background
// goroutine via StreamCommand. Returns a stop func.
func ResumeAgentBackground(t *testing.T, cfg TestConfig, name string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var buf SyncBuffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		cmd := []string{cfg.OmniPath, "agent", "resume", name, "--workspace", cfg.Workspace}
		_ = cfg.Exec.StreamCommand(ctx, &buf, cmd)
	}()
	stop = func() {
		cancel()
		<-done
		t.Logf("resume %s output:\n%s", name, buf.String())
	}
	t.Cleanup(stop)
	return stop
}

// ExecInContainer runs a raw shell command inside the container using sh -c.
// Returns combined output and exit code. Useful for file checks and log reads.
func ExecInContainer(t *testing.T, cfg TestConfig, shellCmd string) (string, int) {
	t.Helper()
	code, out, _ := cfg.Exec.RunCommand(context.Background(),
		[]string{"sh", "-c", shellCmd})
	return string(out), code
}
