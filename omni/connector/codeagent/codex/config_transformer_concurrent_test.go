package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/pkg/filelock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// concurrentTempPath returns a config.toml path in a fresh temp dir.
// Parent .codex directory is pre-created so lock acquisition never races mkdir.
func concurrentTempPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".codex")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return filepath.Join(dir, "config.toml")
}

// parseTOMLStrict reads and parses path; fatals on any error.
func parseTOMLStrict(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("parseTOMLStrict: read %s: %v", path, err)
	}
	raw := map[string]interface{}{}
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Logf("parseTOMLStrict: invalid TOML in %s:\n%s", path, string(data))
		t.Fatalf("parseTOMLStrict: unmarshal: %v", err)
	}
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("parseTOMLStrict: stat %s: %v", path, statErr)
	}
	t.Logf("parsed %s: size=%d mtime=%s", filepath.Base(path), fi.Size(), fi.ModTime())
	return raw
}

// dumpFileContents logs file contents — call in defer after assertions.
func dumpFileContents(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Logf("dumpFile %s: %v", path, err)
		return
	}
	t.Logf("file contents (%s):\n%s", path, string(data))
}

// statAndLog stats path and logs size + mtime after an operation.
func statAndLog(t *testing.T, op, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Logf("%s: stat %s: %v", op, path, err)
		return
	}
	t.Logf("%s: size=%d mtime=%s", op, fi.Size(), fi.ModTime())
}

// testAgentForPath returns a minimal codexAgent pointed at the directory
// containing path (workDir is not used by withMCPConfig directly, but needed
// for struct completeness).
func testAgentForPath(path string) *codexAgent {
	return &codexAgent{locker: filelock.New()}
}

// ─── ConcurrentAddMCP_NoLostEntry ─────────────────────────────────────────────

func TestConcurrentAddMCP_NoLostEntry(t *testing.T) {
	t.Parallel()
	path := concurrentTempPath(t)
	agent := testAgentForPath(path)

	const workers = 10
	var wg sync.WaitGroup
	errs := make([]error, workers)
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("mcp-server-%d", i)
			cmd := fmt.Sprintf("/usr/bin/server-%d", i)
			t.Logf("worker %d: AddMCP %s", i, name)
			errs[i] = agent.withMCPConfig(path, func(raw map[string]interface{}) error {
				servers := getMCPServersRaw(raw)
				entry, err := mcpServerToRaw(codeagent.MCPServer{
					Name:      name,
					Transport: codeagent.MCPTransportStdio,
					Command:   cmd,
				})
				if err != nil {
					return fmt.Errorf("worker %d mcpServerToRaw: %w", i, err)
				}
				servers[name] = entry
				raw["mcp_servers"] = servers
				return nil
			})
			if errs[i] == nil {
				statAndLog(t, fmt.Sprintf("worker %d AddMCP done", i), path)
			} else {
				t.Logf("worker %d: error: %v", i, errs[i])
			}
		}()
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "worker %d: AddMCP must not error", i)
	}

	defer dumpFileContents(t, path)
	raw := parseTOMLStrict(t, path)
	servers, _ := raw["mcp_servers"].(map[string]interface{})
	require.NotNil(t, servers, "mcp_servers section must exist after concurrent writes")

	for i := 0; i < workers; i++ {
		name := fmt.Sprintf("mcp-server-%d", i)
		assert.Contains(t, servers, name, "entry %q must not be silently lost under concurrent writes", name)
	}
	t.Logf("final mcp_servers count: %d (want %d)", len(servers), workers)
}

// ─── ConcurrentHookWrite_NoFileCorruption ─────────────────────────────────────

func TestConcurrentHookWrite_NoFileCorruption(t *testing.T) {
	t.Parallel()
	path := concurrentTempPath(t)
	locker := filelock.New()

	knownEvents := []string{
		"UserPromptSubmit", "PreToolUse", "PostToolUse",
		"PostToolUseFailure", "SessionStart", "Stop",
	}
	const workers = 10

	var wg sync.WaitGroup
	errs := make([]error, workers)
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			event := knownEvents[i%len(knownEvents)]
			cmd := fmt.Sprintf("omni hook --event %s --id %d", event, i)
			t.Logf("worker %d: writeHooksConfig event=%s", i, event)
			errs[i] = writeHooksConfig(locker, path, map[string][]codexHookMatcher{
				event: {{Hooks: []codexHookDef{{Type: "command", Command: cmd}}}},
			})
			if errs[i] == nil {
				statAndLog(t, fmt.Sprintf("worker %d writeHooksConfig done", i), path)
			}
		}()
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "worker %d: writeHooksConfig must not error", i)
	}

	defer dumpFileContents(t, path)
	raw := parseTOMLStrict(t, path)
	require.NotEmpty(t, raw, "config.toml must not be empty after concurrent hook writes")

	features, _ := raw["features"].(map[string]interface{})
	require.NotNil(t, features, "features section must be present")
	assert.Equal(t, true, features["hooks"], "features.hooks must be true")

	hooks, _ := raw["hooks"].(map[string]interface{})
	require.NotNil(t, hooks, "hooks section must be present")
	t.Logf("hooks events in final file: %v", func() []string {
		var ks []string
		for k := range hooks {
			ks = append(ks, k)
		}
		return ks
	}())
}

// ─── AddMCP_And_HookWrite_Interleaved ─────────────────────────────────────────

func TestAddMCP_And_HookWrite_Interleaved(t *testing.T) {
	t.Parallel()
	path := concurrentTempPath(t)
	locker := filelock.New()
	agent := &codexAgent{locker: locker}

	const workers = 10

	// Background poller: check TOML validity during concurrent writes.
	var pollCorrupted int32
	pollStop := make(chan struct{})
	var pollWg sync.WaitGroup
	pollWg.Add(1)
	go func() {
		defer pollWg.Done()
		for {
			select {
			case <-pollStop:
				return
			default:
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue // file may not exist yet or is being replaced
			}
			raw := map[string]interface{}{}
			if tomlErr := toml.Unmarshal(data, &raw); tomlErr != nil {
				t.Logf("poll: invalid TOML mid-run: %v\ncontent: %s", tomlErr, string(data))
				atomic.StoreInt32(&pollCorrupted, 1)
			}
		}
	}()

	var wg sync.WaitGroup
	errs := make([]error, workers)
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				name := fmt.Sprintf("interleave-mcp-%d", i)
				t.Logf("worker %d: AddMCP %s", i, name)
				errs[i] = agent.withMCPConfig(path, func(raw map[string]interface{}) error {
					servers := getMCPServersRaw(raw)
					entry, err := mcpServerToRaw(codeagent.MCPServer{
						Name:      name,
						Transport: codeagent.MCPTransportStdio,
						Command:   "/bin/mcp",
					})
					if err != nil {
						return err
					}
					servers[name] = entry
					raw["mcp_servers"] = servers
					return nil
				})
			} else {
				event := "UserPromptSubmit"
				cmd := fmt.Sprintf("omni hook --event %s --id %d", event, i)
				t.Logf("worker %d: writeHooksConfig", i)
				errs[i] = writeHooksConfig(locker, path, map[string][]codexHookMatcher{
					event: {{Hooks: []codexHookDef{{Type: "command", Command: cmd}}}},
				})
			}
			if errs[i] == nil {
				statAndLog(t, fmt.Sprintf("worker %d done", i), path)
			}
		}()
	}
	wg.Wait()

	close(pollStop)
	pollWg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "worker %d: operation must not error", i)
	}
	if atomic.LoadInt32(&pollCorrupted) != 0 {
		dumpFileContents(t, path)
		t.Error("invalid TOML detected during interleaved AddMCP + writeHooksConfig run")
	}

	defer dumpFileContents(t, path)
	parseTOMLStrict(t, path)
}

// ─── WriteAtomic_PreservesNonEventKeys ────────────────────────────────────────

func TestWriteAtomic_PreservesNonEventKeys(t *testing.T) {
	t.Parallel()
	path := concurrentTempPath(t)
	locker := filelock.New()

	// Seed: top-level scalar + non-event key in hooks section (simulates codex
	// CLI writing a trusted-hash entry that omni must not clobber).
	seedTOML(t, path, map[string]interface{}{
		"model": "o4-mini",
		"hooks": map[string]interface{}{
			"state": map[string]interface{}{
				"trusted_hash": "abc123deadbeef",
			},
		},
	})
	statAndLog(t, "after seed", path)

	hbe := map[string][]codexHookMatcher{
		"UserPromptSubmit": {{
			Hooks: []codexHookDef{{Type: "command", Command: "omni hook --event UserPromptSubmit"}},
		}},
	}
	require.NoError(t, writeHooksConfig(locker, path, hbe))
	statAndLog(t, "after writeHooksConfig", path)

	defer dumpFileContents(t, path)
	raw := parseTOMLStrict(t, path)

	assert.Equal(t, "o4-mini", raw["model"], "top-level model key must be preserved after writeHooksConfig")

	hooks, _ := raw["hooks"].(map[string]interface{})
	require.NotNil(t, hooks, "hooks section must be present")

	state, _ := hooks["state"].(map[string]interface{})
	require.NotNil(t, state, "hooks.state non-event key must be preserved after writeHooksConfig")
	assert.Equal(t, "abc123deadbeef", state["trusted_hash"],
		"trusted_hash must be preserved — writeHooksConfig must not clobber non-event hook keys")

	assert.Contains(t, hooks, "UserPromptSubmit", "UserPromptSubmit event entry must be written")
	t.Logf("hooks keys after write: %v", func() []string {
		var ks []string
		for k := range hooks {
			ks = append(ks, k)
		}
		return ks
	}())
}

// ─── Locker_FileLockExists_AfterOp ────────────────────────────────────────────

func TestLocker_FileLockExists_AfterOp(t *testing.T) {
	t.Parallel()
	path := concurrentTempPath(t)
	locker := filelock.New()
	agent := &codexAgent{locker: locker}

	lockPath := filepath.Join(filepath.Dir(path), ".config.toml.lock")

	// Pre-op: sidecar must not exist in the fresh temp dir.
	_, preErr := os.Stat(lockPath)
	require.True(t, os.IsNotExist(preErr), "lock sidecar must not exist before any operation")
	t.Log("pre-op: lock sidecar absent (correct)")

	// After withMCPConfig.
	t.Log("running withMCPConfig")
	err := agent.withMCPConfig(path, func(raw map[string]interface{}) error {
		servers := getMCPServersRaw(raw)
		entry, err := mcpServerToRaw(codeagent.MCPServer{
			Name:      "lock-test-mcp",
			Transport: codeagent.MCPTransportStdio,
			Command:   "/bin/lock-test",
		})
		if err != nil {
			return err
		}
		servers["lock-test-mcp"] = entry
		raw["mcp_servers"] = servers
		return nil
	})
	require.NoError(t, err, "withMCPConfig must not error")

	fi, err := os.Stat(lockPath)
	require.NoError(t, err, "lock sidecar must exist at %s after withMCPConfig", lockPath)
	t.Logf("after withMCPConfig: lock file size=%d mtime=%s", fi.Size(), fi.ModTime())

	// After writeHooksConfig.
	t.Log("running writeHooksConfig")
	hbe := map[string][]codexHookMatcher{
		"PreToolUse": {{Hooks: []codexHookDef{{Type: "command", Command: "omni hook --event PreToolUse"}}}},
	}
	require.NoError(t, writeHooksConfig(locker, path, hbe), "writeHooksConfig must not error")

	fi, err = os.Stat(lockPath)
	require.NoError(t, err, "lock sidecar must still exist after writeHooksConfig")
	t.Logf("after writeHooksConfig: lock file size=%d mtime=%s", fi.Size(), fi.ModTime())

	if !strings.HasSuffix(lockPath, ".config.toml.lock") {
		t.Errorf("lock path %q must end with .config.toml.lock", lockPath)
	}
}
