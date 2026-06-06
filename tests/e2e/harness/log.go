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

// DumpLogsOnFailure writes journalctl + omni.log to t.Log when the test has
// failed. Also fetches the last 200 lines of omni.log directly from the
// container so nothing is missed if the streaming buffer started late.
// Call deferred at the end of each test.
func DumpLogsOnFailure(t *testing.T, jrnl, omniLog *SyncBuffer, msgID string) {
	t.Helper()
	if !t.Failed() {
		return
	}
	t.Log("=== FAILURE LOG DUMP ===")
	if jrnl != nil {
		s := jrnl.String()
		if msgID != "" {
			filtered := FilterByID(s, msgID)
			t.Logf("--- journalctl filtered for message_id=%s (%d lines) ---\n%s",
				msgID, strings.Count(filtered, "\n"), filtered)
		}
		t.Logf("--- journalctl full (%d bytes) ---\n%s", len(s), s)
	}
	if omniLog != nil {
		s := omniLog.String()
		if msgID != "" {
			filtered := FilterByID(s, msgID)
			t.Logf("--- omni.log filtered for message_id=%s (%d lines) ---\n%s",
				msgID, strings.Count(filtered, "\n"), filtered)
		}
		t.Logf("--- omni.log buffered (%d bytes) ---\n%s", len(s), s)
	}
}

// DumpLogsOnFailureCfg is like DumpLogsOnFailure but also reads the last 300
// lines of omni.log directly from the container so nothing is missed when the
// streaming buffer was attached after some events fired.
func DumpLogsOnFailureCfg(t *testing.T, cfg TestConfig, jrnl, omniLog *SyncBuffer, msgID string) {
	t.Helper()
	if !t.Failed() {
		return
	}
	t.Log("=== FAILURE LOG DUMP ===")
	// Pull a fresh snapshot from inside the container (covers pre-buffer events).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, snap, _ := cfg.Exec.RunCommand(ctx, []string{
		"sh", "-c", "tail -n 300 /root/.omni/log/omni.log 2>/dev/null || true",
	})
	if len(snap) > 0 {
		s := string(snap)
		if msgID != "" {
			filtered := FilterByID(s, msgID)
			t.Logf("--- omni.log snapshot filtered for message_id=%s ---\n%s", msgID, filtered)
		}
		t.Logf("--- omni.log snapshot (tail 300) ---\n%s", s)
	}
	if jrnl != nil {
		s := jrnl.String()
		if msgID != "" {
			filtered := FilterByID(s, msgID)
			t.Logf("--- journalctl filtered for message_id=%s ---\n%s", msgID, filtered)
		}
		t.Logf("--- journalctl buffered (%d bytes) ---\n%s", len(s), s)
	}
	if omniLog != nil {
		s := omniLog.String()
		if s != "" {
			t.Logf("--- omni.log streamed (%d bytes) ---\n%s", len(s), s)
		}
	}
}
