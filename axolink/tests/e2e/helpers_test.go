//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type testConfig struct {
	exec      CommandExecutor
	omniPath  string
	workspace string
	container string
}

// newConfig creates an isolated test environment. Each test gets its own
// workspace directory under /tmp inside the container so tests never touch
// /build or the real agents (guide, tagy, tclaude, tecodex).
// Override with OMNI_WORKSPACE env var when a fixed path is required.
func newConfig(t *testing.T) testConfig {
	t.Helper()
	target := envOr("E2E_TARGET", "docker")
	ctr := envOr("E2E_CONTAINER", "development-ubuntu-1")

	var baseEx CommandExecutor
	switch target {
	case "local":
		baseEx = &HostExecutor{}
	default:
		baseEx = newDockerExecutor(t, ctr)
	}

	workspace := os.Getenv("OMNI_WORKSPACE")
	if workspace == "" {
		workspace = provisionWorkspace(t, baseEx)
	}

	// Pin the working directory to the isolated workspace so commands like
	// `omni team init` (which use os.Getwd()) operate in the right place.
	var ex CommandExecutor = baseEx
	if de, ok := baseEx.(*DockerExecutor); ok {
		ex = de.WithWorkDir(workspace)
	}

	return testConfig{
		exec:      ex,
		omniPath:  envOr("OMNI_BIN", "omni"),
		workspace: workspace,
		container: ctr,
	}
}

// provisionWorkspace creates a fresh temp directory inside the container and
// registers a cleanup to remove it when the test finishes.
func provisionWorkspace(t *testing.T, ex CommandExecutor) string {
	t.Helper()
	// Use a name derived from the test name so it's easy to spot in logs.
	safe := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dir := fmt.Sprintf("/tmp/e2e-%s", safe)

	ctx := context.Background()
	code, _, _ := ex.RunCommand(ctx, []string{"mkdir", "-p", dir})
	if code != 0 {
		t.Fatalf("provisionWorkspace: could not create %s", dir)
	}
	t.Cleanup(func() {
		// Best-effort cleanup — /tmp is tmpfs and wiped on container restart anyway.
		_, _, _ = ex.RunCommand(context.Background(), []string{"rm", "-rf", dir})
	})
	return dir
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// syncBuffer is a bytes.Buffer safe for concurrent reads and writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// WaitFor polls the buffer every 500ms until substr appears or timeout expires.
func (b *syncBuffer) WaitFor(substr string, timeout time.Duration) bool {
	needle := []byte(substr)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		found := bytes.Contains(b.buf.Bytes(), needle)
		b.mu.Unlock()
		if found {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// captureLog starts streaming journalctl (omni-server identifier) into a buffer.
// Returns a stop func and the live buffer. Always called first in each test.
func captureLog(t *testing.T, cfg testConfig) (stop func(), buf *syncBuffer) {
	t.Helper()
	buf = &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		// --lines=0 prevents replaying recent history from prior tests.
		_ = cfg.exec.StreamCommand(ctx, buf, []string{"journalctl", "-f", "--no-pager", "--lines=0", "-t", "omni-server"})
	}()

	stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return stop, buf
}

// newCtx returns a context that is cancelled when the test ends.
func newCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// runOmni executes an omni subcommand and returns the output.
// It fails the test if the exit code is non-zero.
func runOmni(t *testing.T, cfg testConfig, args ...string) string {
	t.Helper()
	cmd := append([]string{cfg.omniPath}, args...)
	exitCode, out, err := cfg.exec.RunCommand(context.Background(), cmd)
	t.Logf("omni %v → exit=%d\n%s", args, exitCode, out)
	if err != nil || exitCode != 0 {
		t.Fatalf("omni %v failed (exit=%d): %s", args, exitCode, out)
	}
	return string(out)
}

// runOmniAllowFail runs an omni subcommand and returns output + exit code
// without failing the test. Used for teardown and negative assertions.
func runOmniAllowFail(t *testing.T, cfg testConfig, args ...string) (string, int) {
	t.Helper()
	cmd := append([]string{cfg.omniPath}, args...)
	exitCode, out, _ := cfg.exec.RunCommand(context.Background(), cmd)
	t.Logf("omni %v → exit=%d\n%s", args, exitCode, out)
	return string(out), exitCode
}

// teardownAgent deletes an agent by name via CLI. Safe to call in t.Cleanup.
func teardownAgent(t *testing.T, cfg testConfig, name string) {
	t.Helper()
	t.Logf("teardown: deleting agent %q", name)
	_, _ = runOmniAllowFail(t, cfg, "agent", "delete", name, "--workspace", cfg.workspace)
}

// resumeAgentBackground runs `omni agent resume <name>` in a background goroutine
// via StreamCommand so it does not block the test.
func resumeAgentBackground(t *testing.T, cfg testConfig, name string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var buf syncBuffer
	done := make(chan struct{})

	go func() {
		defer close(done)
		cmd := []string{cfg.omniPath, "agent", "resume", name, "--workspace", cfg.workspace}
		_ = cfg.exec.StreamCommand(ctx, &buf, cmd)
	}()

	stop = func() {
		cancel()
		<-done
		t.Logf("resume %s output:\n%s", name, buf.String())
	}
	t.Cleanup(stop)
	return stop
}
