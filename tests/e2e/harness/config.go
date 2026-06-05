//go:build e2e

package harness

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestConfig holds the runtime parameters for a single e2e test.
type TestConfig struct {
	Exec      CommandExecutor
	OmniPath  string
	Workspace string
	Container string
}

// NewConfig sets up an isolated test environment backed by a per-test workspace.
//
// The workspace directory is provisioned fresh for each test and injected as
// OMNI_WORKSPACE into every command the executor runs. Because the omni binary
// respects OMNI_WORKSPACE for all subcommands (including `agent exec` and
// `agent stop` which do not accept --workspace flags), all DB operations are
// automatically routed to the same directory — no split-brain.
//
// Environment variables:
//
//	E2E_TARGET     = "docker" (default) | "local"
//	E2E_CONTAINER  = container name (default: omni-main-ubuntu-1)
//	OMNI_BIN       = path to omni binary (default: omni)
//	OMNI_WORKSPACE = pin all tests to an existing workspace (skips provisioning)
func NewConfig(t *testing.T) TestConfig {
	t.Helper()
	target := EnvOr("E2E_TARGET", "docker")
	ctr := EnvOr("E2E_CONTAINER", "omni-main-ubuntu-1")

	var baseEx CommandExecutor
	switch target {
	case "local":
		baseEx = &HostExecutor{}
	default:
		baseEx = NewDockerExecutor(t, ctr)
	}

	// Per-test workspace: each test gets its own isolated directory.
	// OMNI_WORKSPACE env override pins all tests to one existing workspace.
	workspace := os.Getenv("OMNI_WORKSPACE")
	if workspace == "" {
		workspace = ProvisionWorkspace(t, baseEx)
	}

	// Inject workspace into every command so subcommands that lack --workspace
	// (exec, stop) still operate on the correct database.
	// Set working directory to the workspace so all commands (including
	// `agent exec` which resolves names via CWD) operate on the right DB.
	wsEnv := "OMNI_WORKSPACE=" + workspace
	var ex CommandExecutor = baseEx
	switch e := baseEx.(type) {
	case *DockerExecutor:
		ex = e.WithWorkDir(workspace).WithEnv(wsEnv)
	case *HostExecutor:
		ex = e.WithWorkDir(workspace).WithEnv(wsEnv)
	}

	return TestConfig{
		Exec:      ex,
		OmniPath:  EnvOr("OMNI_BIN", "omni"),
		Workspace: workspace,
		Container: ctr,
	}
}

// EnvOr returns the value of key or def when the var is unset/empty.
func EnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ProvisionWorkspace creates a fresh /tmp/e2e-<test> directory and registers
// cleanup. Used both as the agent DB workspace and for test file artifacts.
func ProvisionWorkspace(t *testing.T, ex CommandExecutor) string {
	t.Helper()
	safe := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dir := fmt.Sprintf("/tmp/e2e-%s", safe)
	ctx := context.Background()
	code, _, _ := ex.RunCommand(ctx, []string{"mkdir", "-p", dir})
	if code != 0 {
		t.Fatalf("provisionWorkspace: could not create %s", dir)
	}
	t.Cleanup(func() {
		_, _, _ = ex.RunCommand(context.Background(), []string{"rm", "-rf", dir})
	})
	return dir
}
