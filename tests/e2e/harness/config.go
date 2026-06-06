//go:build e2e

package harness

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

var reUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// TestConfig holds the runtime parameters for a single e2e test.
type TestConfig struct {
	Exec      CommandExecutor
	OmniPath  string
	Workspace string
	Container string
	// Provider is the agent provider to use (e.g. "claude", "codex").
	// Set via E2E_PROVIDER env var or auto-detected from available binaries.
	Provider string
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
//	E2E_PROVIDER   = agent provider to use (default: auto-detected from available binaries)
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

	provider := detectProvider(t, baseEx)

	return TestConfig{
		Exec:      ex,
		OmniPath:  EnvOr("OMNI_BIN", "omni"),
		Workspace: workspace,
		Container: ctr,
		Provider:  provider,
	}
}

// RequireProvider returns the globally-configured provider for a
// provider-flexible test (one whose logic is identical for any provider).
// It skips the test — with a visible WARNING — when no provider is available.
// In the docker container the entrypoint always installs a provider, so this
// only skips in a bare local environment without claude/codex.
func RequireProvider(t *testing.T, cfg TestConfig) string {
	t.Helper()
	if cfg.Provider == "" {
		t.Log("WARNING: skipping — no agent provider (claude/codex) available; " +
			"set E2E_PROVIDER or run inside the docker container")
		t.Skip("no agent provider available")
	}
	return cfg.Provider
}

// RequireSpecificProvider skips a provider-specific test — with a visible
// WARNING — when the named provider's binary is not present. Use this for
// tests that exercise one provider's particular behaviour by design.
func RequireSpecificProvider(t *testing.T, cfg TestConfig, provider string) {
	t.Helper()
	if _, code := ExecInContainer(t, cfg, "command -v "+provider); code != 0 {
		t.Logf("WARNING: skipping — %q binary not available (provider-specific test)", provider)
		t.Skipf("%s binary not available", provider)
	}
}

// detectProvider returns the agent provider to use for this test run.
// Checks E2E_PROVIDER env first; falls back to probing for claude then codex.
// Logs a warning if neither binary is found so failures are easy to diagnose.
func detectProvider(t *testing.T, ex CommandExecutor) string {
	t.Helper()
	if p := os.Getenv("E2E_PROVIDER"); p != "" {
		return p
	}
	ctx := context.Background()
	for _, p := range []string{"claude", "codex"} {
		code, _, _ := ex.RunCommand(ctx, []string{"sh", "-c", "command -v " + p})
		if code == 0 {
			return p
		}
	}
	t.Log("WARNING: no agent binary (claude/codex) found in PATH — provider-dependent tests will fail")
	return ""
}

// EnvOr returns the value of key or def when the var is unset/empty.
func EnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// AgentNameSuffix returns a short, deterministic, filesystem-safe suffix derived
// from the test name. Use this instead of time.Now().Format("150405") in agent
// names to avoid second-resolution collisions in parallel tests.
func AgentNameSuffix(t *testing.T) string {
	t.Helper()
	s := strings.ToLower(reUnsafe.ReplaceAllString(t.Name(), "-"))
	if len(s) > 24 {
		s = s[len(s)-24:]
	}
	return strings.Trim(s, "-")
}

// ProvisionWorkspace creates a fresh /tmp/e2e-<test> directory and registers
// cleanup. Used both as the agent DB workspace and for test file artifacts.
func ProvisionWorkspace(t *testing.T, ex CommandExecutor) string {
	t.Helper()
	// Allow only alphanumeric, hyphen, and underscore to avoid invalid paths on
	// filesystems that reject characters such as ':', '#', or spaces.
	safe := strings.ToLower(reUnsafe.ReplaceAllString(t.Name(), "_"))
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
