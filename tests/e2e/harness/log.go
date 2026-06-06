//go:build e2e

package harness

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// SyncBuffer is a bytes.Buffer safe for concurrent reads and writes.
type SyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *SyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *SyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *SyncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// WaitFor polls the buffer every 300ms until substr appears or timeout expires.
func (b *SyncBuffer) WaitFor(substr string, timeout time.Duration) bool {
	needle := []byte(substr)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		found := bytes.Contains(b.buf.Bytes(), needle)
		b.mu.Unlock()
		if found {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

// WaitForWithID waits for a log line that contains BOTH messageID AND substr.
func (b *SyncBuffer) WaitForWithID(messageID, substr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(b.String(), "\n") {
			if strings.Contains(line, messageID) && strings.Contains(line, substr) {
				return true
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

// CaptureLog starts streaming journalctl (omni-server identifier) into a buffer
// and waits 300ms for the stream connection to establish before returning.
// Returns a stop func and the live buffer.
func CaptureLog(t *testing.T, cfg TestConfig) (stop func(), buf *SyncBuffer) {
	t.Helper()
	buf = &SyncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cfg.Exec.StreamCommand(ctx, buf, []string{
			"journalctl", "-f", "--no-pager", "--lines=0", "-t", "omni-server",
		})
	}()
	stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	time.Sleep(300 * time.Millisecond)
	return stop, buf
}

// CaptureOmniLog tails ~/.omni/log/omni.log inside the container.
// This is the primary structured log source since pkg/log routes all levels there.
func CaptureOmniLog(t *testing.T, cfg TestConfig) (stop func(), buf *SyncBuffer) {
	t.Helper()
	buf = &SyncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cfg.Exec.StreamCommand(ctx, buf, []string{
			"tail", "-f", "-n", "0", "/root/.omni/log/omni.log",
		})
	}()
	stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return stop, buf
}

var reMessageID = regexp.MustCompile(`message_id=([a-f0-9-]{36})`)

// ExtractMessageID scans buf for the first "send_message succeeded" line and
// returns the message_id UUID. Returns "" if not found within timeout.
func ExtractMessageID(buf *SyncBuffer, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(buf.String(), "\n") {
			if strings.Contains(line, "send_message succeeded") {
				if m := reMessageID.FindStringSubmatch(line); len(m) > 1 {
					return m[1]
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

// FilterByID returns only lines from log that contain messageID.
func FilterByID(log, messageID string) string {
	var out strings.Builder
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, messageID) {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// relevantSignals are substrings that mark a log line as likely relevant to a
// failure: warnings/errors plus the message- and session-flow signals across the
// key components (operator, engine, tunnel_mcp service, ptydaemon).
var relevantSignals = []string{
	"level=warn", "level=error", "panic", "fatal error",
	"sender", "resolve", "not found", "is required", "iserror",
	"send_message", "get_message", "query_result", "send_response",
	"exec in session", "userpromptsubmit", "create session", "adopt",
	"terminal", "sqlite", "database is locked",
}

// FilterRelevant returns only the log lines that match a relevantSignal, so a
// failure dump leads with the lines that actually explain it.
func FilterRelevant(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		l := strings.ToLower(line)
		for _, kw := range relevantSignals {
			if strings.Contains(l, kw) {
				b.WriteString(line)
				b.WriteByte('\n')
				break
			}
		}
	}
	return b.String()
}

// DumpLogsOnFailure writes captured logs to t.Log when the test has failed.
// It leads with a filtered "relevant" extract (errors/warnings + message/session
// flow) from both sources so the cause is visible without re-fetching container
// logs, then includes the full buffers and any message_id-scoped view.
// Call deferred at the end of each test.
func DumpLogsOnFailure(t *testing.T, jrnl, omniLog *SyncBuffer, msgID string) {
	t.Helper()
	if !t.Failed() {
		return
	}

	// Relevant-only extract first — this is usually all you need.
	var relevant strings.Builder
	if jrnl != nil {
		relevant.WriteString(FilterRelevant(jrnl.String()))
	}
	if omniLog != nil {
		relevant.WriteString(FilterRelevant(omniLog.String()))
	}
	if r := strings.TrimSpace(relevant.String()); r != "" {
		t.Logf("=== relevant log lines (errors/warnings + msg/session flow) ===\n%s", r)
	}

	if jrnl != nil {
		s := jrnl.String()
		t.Logf("=== journalctl (%d bytes) ===\n%s", len(s), s)
	}
	if omniLog != nil {
		s := omniLog.String()
		t.Logf("=== omni.log (%d bytes) ===\n%s", len(s), s)
		if msgID != "" {
			t.Logf("=== omni.log filtered for message_id=%s ===\n%s", msgID, FilterByID(s, msgID))
		}
	}
}
