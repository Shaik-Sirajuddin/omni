//go:build e2e

package harness

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// Checkpoint describes a single expected log line in a delivery chain.
type Checkpoint struct {
	Name    string
	Substr  string
	Timeout time.Duration
}

// AssertDeliveryChain verifies that messageID passed through all checkpoints.
// Each checkpoint must co-appear with messageID on a log line.
// Missing checkpoints are reported individually so all failures surface at once.
func AssertDeliveryChain(t *testing.T, buf *SyncBuffer, messageID string, checkpoints []Checkpoint) {
	t.Helper()
	for _, cp := range checkpoints {
		if !buf.WaitForWithID(messageID, cp.Substr, cp.Timeout) {
			t.Errorf("delivery chain: checkpoint %q not observed for message_id=%s within %s",
				cp.Name, messageID, cp.Timeout)
			t.Logf("=== log at checkpoint failure (message_id=%s) ===\n%s",
				messageID, FilterByID(buf.String(), messageID))
		}
	}
}

var (
	rePanic = regexp.MustCompile(`(?im)panic:|fatal error:`)

	// Known pre-existing noise — suppressed so tests only catch new regressions.
	reKnownNoise = regexp.MustCompile(
		`agent_id=""` +
			`|SQLITE_BUSY` +
			`|runtime create failed.*sandbox=gvisor` +
			`|sender agent not found` +
			`|GetAgent\(store\): query failed.*sql: no rows` +
			`|attach terminal setup failed` +
			`|duplicate name`,
	)
)

// isTopLevelError returns true when level=ERROR is the first level= field on
// the line — i.e. the line itself is an ERROR entry.
func isTopLevelError(line string) bool {
	idx := strings.Index(line, "level=")
	return idx >= 0 && strings.HasPrefix(line[idx:], "level=ERROR")
}

// AssertNoLogErrors reports each unexpected ERROR line as a separate test failure.
func AssertNoLogErrors(t *testing.T, log string) {
	t.Helper()
	for _, line := range strings.Split(log, "\n") {
		if isTopLevelError(line) && !reKnownNoise.MatchString(line) {
			t.Errorf("unexpected server ERROR: %s", line)
		}
	}
	for _, line := range strings.Split(log, "\n") {
		if rePanic.MatchString(line) {
			t.Errorf("panic/fatal in log: %s", line)
		}
	}
}

// AssertLogContains fails the test if substr is not found in log.
// On failure the full log is dumped.
func AssertLogContains(t *testing.T, log, substr, msg string) {
	t.Helper()
	if !strings.Contains(log, substr) {
		t.Errorf("%s\nsubstring %q not found in log", msg, substr)
		t.Logf("=== full log ===\n%s", log)
	}
}

// AssertNoExecSessionFailed reports each "exec in session failed" line.
func AssertNoExecSessionFailed(t *testing.T, log string) {
	t.Helper()
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "exec in session failed") {
			t.Errorf("exec in session delivery failure: %s", line)
		}
	}
}
