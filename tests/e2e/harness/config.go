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

// NewConfig creates an isolated test environment.
// Each test gets its own workspace directory under /tmp inside the container.
//
// Environment variables:
//
//	E2E_TARGET     = "docker" (default) | "local"
//	E2E_CONTAINER  = container name (default: omni-main-ubuntu-1)
//	OMNI_BIN       = path to omni binary (default: omni)
//	OMNI_WORKSPACE = fixed workspace path (skips per-test provisioning)
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

	workspace := os.Getenv("OMNI_WORKSPACE")
	if workspace == "" {
		workspace = ProvisionWorkspace(t, baseEx)
	}

	// Inject OMNI_WORKSPACE so commands that don't accept --workspace still
	// operate in the correct workspace (e.g. `agent exec`, `agent stop`).
	wsEnv := "OMNI_WORKSPACE=" + workspace
	var ex CommandExecutor = baseEx
	switch e := baseEx.(type) {
	case *DockerExecutor:
		ex = e.WithWorkDir(workspace).WithEnv(wsEnv)
	case *HostExecutor:
		ex = e.WithEnv(wsEnv)
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

// ProvisionWorkspace creates a fresh /tmp/e2e-<test> directory inside the
// executor and registers a cleanup to remove it when the test finishes.
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
