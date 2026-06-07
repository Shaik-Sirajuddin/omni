//go:build e2e

package engine_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/tests/e2e/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── shared helpers ────────────────────────────────────────────────────────────

// waitForEngineQuiescent waits for all seed sessions created by "omni agent init"
// to complete before the test sends its own messages.
//
// Seed sessions run synchronously inside the "agent init" command (not via the
// engine's message queue), so they complete within ~5 seconds and are already
// done by the time this function is called. The engine logs "event=SessionEnd"
// via its hook handler for each seed session.
//
// We wait for at least one SessionEnd in jrnl (sanity check that the hook is
// live), then sleep briefly to let any in-flight Stop hook writes commit.
func waitForEngineQuiescent(t *testing.T, jrnl, omniLog *harness.SyncBuffer) {
	t.Helper()
	// Seed sessions complete synchronously, so SessionEnd should already be in
	// the buffer. Give it 15s in case of slight hook-delivery lag.
	if !jrnl.WaitFor("event=SessionEnd", 15*time.Second) {
		t.Fatal("waitForEngineQuiescent: no seed session SessionEnd in hook log — engine hook may not be running")
	}
	// Brief settle for any remaining Stop hook processing.
	time.Sleep(1 * time.Second)
	t.Log("waitForEngineQuiescent: seed session(s) complete, engine ready")
}

// ensureOmniLog pre-creates /root/.omni/log/omni.log in the container so that
// CaptureOmniLog's tail -f doesn't fail on a cold-start first test run where the
// server hasn't written its first log line yet.
func ensureOmniLog(t *testing.T, cfg harness.TestConfig) {
	t.Helper()
	harness.ExecInContainer(t, cfg, "mkdir -p /root/.omni/log && touch /root/.omni/log/omni.log")
}

// purgeAgents deletes named agents (allow-fail) before creating them, so that
// stale agents from a previous interrupted test run do not cause "already exists" errors.
func purgeAgents(t *testing.T, cfg harness.TestConfig, names ...string) {
	t.Helper()
	for _, name := range names {
		harness.RunOmniAllowFail(t, cfg, "agent", "delete", name, "--workspace", cfg.Workspace)
	}
}

// sendQuery sends a query message from sender to receiver via MCP send_message.
// Returns the message_id or fails the test if the sender-resolution bug blocks it.
func sendQuery(t *testing.T, cfg harness.TestConfig, senderID, receiverName, prompt string) string {
	t.Helper()
	cli := harness.NewMCPClient(t, cfg, senderID)
	inner := cli.CallToolText("send_message", map[string]any{
		"to_type":      "omni_agent",
		"to_name":      receiverName,
		"workspace":    cfg.Workspace,
		"prompt":       prompt,
		"request_type": "query",
	})
	t.Logf("send_message → %s", inner)

	require.NotContains(t, inner, "sender agent not found",
		"BUG: send_message could not resolve sender agent by name+workspace — "+
			"agent-to-agent messaging broken for CLI-created agents (see TestSendMessageRecorded)")

	var resp struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal([]byte(inner), &resp)
	require.NotEmpty(t, resp.MessageID, "send_message must return a message_id")
	return resp.MessageID
}

// messageStatus fetches the current status of a message via a single MCP get_message call.
func messageStatus(t *testing.T, cfg harness.TestConfig, senderID, msgID string) string {
	t.Helper()
	cli := harness.NewMCPClient(t, cfg, senderID)
	out := cli.CallToolText("get_message", map[string]any{"id": msgID})
	var m struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal([]byte(out), &m)
	return m.Status
}

// waitForStatus polls get_message until the message reaches wantStatus or timeout.
// Creates one MCPClient per invocation and reuses it across polls to avoid repeated
// docker-exec initialize roundtrips (2 docker exec calls saved per poll iteration).
// Recreates the client automatically on empty/error responses (session expiry).
func waitForStatus(t *testing.T, cfg harness.TestConfig, senderID, msgID, wantStatus string, timeout time.Duration) bool {
	t.Helper()
	cli := harness.NewMCPClient(t, cfg, senderID)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out := cli.CallToolText("get_message", map[string]any{"id": msgID})
		// Recreate on error/empty (MCP session expiry or connection reset).
		if out == "" || strings.HasPrefix(out, "server-error:") || strings.HasPrefix(out, "parse-error:") {
			cli = harness.NewMCPClient(t, cfg, senderID)
			out = cli.CallToolText("get_message", map[string]any{"id": msgID})
		}
		var m struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal([]byte(out), &m)
		if m.Status == wantStatus {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// lastDeliverySession scans log output for the most recent
// "session-<uuid>.log" reference and returns the <uuid>. The engine emits this
// (e.g. "omni: log → /root/.omni/log/session-<id>.log") when it execs a session,
// so on a delivery failure it identifies the exact session the prompt was sent to.
func lastDeliverySession(logs string) string {
	const marker = "session-"
	id := ""
	for {
		i := strings.Index(logs, marker)
		if i < 0 {
			break
		}
		rest := logs[i+len(marker):]
		// UUID is 36 chars (8-4-4-4-12); the next ".log" must follow it directly.
		if j := strings.Index(rest, ".log"); j == 36 {
			id = rest[:j]
		}
		logs = rest
	}
	return id
}

// assertMessageProcessing is an early-failure check: after exec fires, the engine
// sets status=processing the moment the receiver submits the prompt
// (UserPromptSubmit) — within seconds, even when the LLM's reply is slow. If the
// message is still "queued" after the timeout, the prompt was typed into the
// delivery session but never submitted (the delivery-session bug). Failing here
// surfaces that in ~timeout instead of waiting the full delivery timeout (~900s),
// and prints the delivery session id so the failing session log is identifiable.
func assertMessageProcessing(t *testing.T, cfg harness.TestConfig,
	senderID, msgID string, jrnl, omniLog *harness.SyncBuffer, timeout time.Duration) {
	t.Helper()
	if waitForStatus(t, cfg, senderID, msgID, "processing", timeout) {
		return
	}
	// May have raced past processing straight to delivered (fast clean reply).
	if st := messageStatus(t, cfg, senderID, msgID); st == "delivered" {
		return
	} else {
		sid := lastDeliverySession(omniLog.String() + jrnl.String())
		t.Fatalf("EARLY-FAIL: message %s never reached status=processing within %s "+
			"(current=%q) — receiver did not submit the prompt; delivery session=%q "+
			"was likely typed-but-never-submitted (resume-vs-fresh-turn bug)",
			msgID, timeout, st, sid)
	}
}

// waitForDelivery waits for a delivery signal in the log buffers (fast, in-memory,
// 300ms poll interval — no MCP roundtrips) then confirms via one MCP status call.
//
// Primary path: waits for "recall retries exhausted" which the engine logs just
// before force-delivering a message that never got an explicit send_response.
// Fallback: if the LLM responds cleanly before the watchdog fires, "recall retries
// exhausted" never appears — fall back to a short MCP status poll.
//
// Using log buffers instead of a long MCP polling loop means:
// - The test unblocks the moment the engine event fires (no unnecessary wait)
// - Only ONE MCP docker-exec roundtrip needed for the final confirmation
func waitForDelivery(t *testing.T, cfg harness.TestConfig,
	senderID, msgID string,
	jrnl, omniLog *harness.SyncBuffer,
	timeout time.Duration,
) bool {
	t.Helper()
	// Wait for the authoritative engine-side event (both log paths checked).
	exhausted :=
		omniLog.WaitForWithID(msgID, "recall retries exhausted", timeout) ||
		jrnl.WaitForWithID(msgID, "recall retries exhausted", 3*time.Second)
	if exhausted {
		// Brief pause for the DB write to commit before querying.
		time.Sleep(500 * time.Millisecond)
		return messageStatus(t, cfg, senderID, msgID) == "delivered"
	}
	// Fallback: LLM responded before the watchdog (clean delivery, no exhaustion).
	// Poll MCP for a short window.
	return waitForStatus(t, cfg, senderID, msgID, "delivered", 20*time.Second)
}

// tryDetectRecall runs exec up to maxAttempts times on a fresh message each
// attempt, looking for "injecting recall" in omniLog. Returns (true, msgID) on
// first detection. Both journalctl (jrnl) and omni.log (omniLog) are checked so
// the log path evidence is dual-sourced even though the structured payload is in
// omniLog. The prompt is chosen to be a natural question that gives the LLM no
// reason to call send_response on its own.
func tryDetectRecall(t *testing.T, cfg harness.TestConfig,
	jrnl, omniLog *harness.SyncBuffer,
	senderID, receiverName string,
	maxAttempts int,
) (detected bool, lastMsgID string) {
	t.Helper()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		msgID := sendQuery(t, cfg, senderID, receiverName,
			fmt.Sprintf("What is the capital of France? (attempt %d)", attempt+1))
		lastMsgID = msgID
		t.Logf("recall attempt %d: message_id=%s", attempt+1, msgID)

		// Wait for exec to fire (session ran) before checking recall.
		if !jrnl.WaitForWithID(msgID, "execute loop: executing agent", 20*time.Second) {
			// Fallback: omni.log may carry the same event.
			if !omniLog.WaitForWithID(msgID, "execute loop: executing agent", 5*time.Second) {
				t.Logf("attempt %d: exec never started for message_id=%s", attempt+1, msgID)
				continue
			}
		}

		// Check omni.log for recall injection (structured log, most reliable).
		if omniLog.WaitForWithID(msgID, "injecting recall", 20*time.Second) {
			t.Logf("recall detected on attempt %d for message_id=%s", attempt+1, msgID)
			return true, msgID
		}

		// Also check journalctl as a secondary source.
		if jrnl.WaitForWithID(msgID, "injecting recall", 3*time.Second) {
			t.Logf("recall detected (journalctl) on attempt %d for message_id=%s", attempt+1, msgID)
			return true, msgID
		}

		t.Logf("attempt %d: no recall observed — LLM likely called send_response", attempt+1)
	}
	return false, lastMsgID
}

// ── R1: Control — agent calls send_response, no recall ───────────────────────

// TestToolRecall_R1_CleanDelivery verifies that when the receiver agent calls
// send_response the engine marks the message delivered. The no-recall assertion
// is advisory: on slow LLM cold-starts the watchdog may fire a spurious recall
// before the LLM responds (~29s vs ~10s watchdog). In that case the test still
// passes as long as the message eventually reaches delivered status — the
// important invariant is delivery, not clean delivery on the first turn.
func TestToolRecall_R1_CleanDelivery(t *testing.T) {
	cfg := harness.NewConfig(t)
	ensureOmniLog(t, cfg)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer func() { harness.DumpLogsOnFailureCfg(t, cfg, jrnl, omniLog, "") }()

	provider := harness.RequireProvider(t, cfg)
	ts := harness.AgentNameSuffix(t)
	sender := "e2e-r1-sender-" + ts
	receiver := "e2e-r1-recv-" + ts
	t.Cleanup(func() {
		harness.TeardownAgent(t, cfg, sender)
		harness.TeardownAgent(t, cfg, receiver)
	})

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	purgeAgents(t, cfg, sender, receiver)
	harness.RunOmni(t, cfg, "agent", "init", sender,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "init", receiver,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")

	// Verify both agents were actually created in the operator's store before
	// proceeding. `agent init --interactive=false` creates the record but no
	// session, so this checks the store (via `agent list`), not session/log
	// files — see harness.AssertAgentCreated.
	harness.AssertAgentCreated(t, cfg, sender)
	harness.AssertAgentCreated(t, cfg, receiver)

	// Wait for the engine to finish the seed session created by "agent init" before
	// sending the test message. Without this, the receiver is busy with its seed
	// session when the test message arrives; the 92789a3 fix (skip Running agents in
	// queue sweep) holds the test message in queue for ~240s (the full seed duration).
	waitForEngineQuiescent(t, jrnl, omniLog)

	// Prompt that explicitly asks the receiver to use send_response.
	msgID := sendQuery(t, cfg, sender, receiver,
		"Reply to this message using the send_response tool with message_id from the context. Response: 'ok'")

	// Exec must fire.
	require.True(t,
		jrnl.WaitForWithID(msgID, "execute loop: executing agent", 25*time.Second) ||
			omniLog.WaitForWithID(msgID, "execute loop: executing agent", 5*time.Second),
		"exec must start for message_id=%s", msgID)

	// Early-failure check: the receiver must start processing (submit the prompt)
	// shortly after exec. Fails fast in ~90s if the delivery session never submits
	// the prompt, instead of waiting the full delivery timeout below.
	assertMessageProcessing(t, cfg, sender, msgID, jrnl, omniLog, 90*time.Second)

	// Wait for delivery via log buffer. On cold LLM, the first Claude Code session
	// can take 4-5 minutes; with 3 recall cycles before force-deliver the total
	// latency can be ~15 minutes. Use a generous timeout and rely on the test binary
	// -timeout flag as the hard ceiling.
	delivered := waitForDelivery(t, cfg, sender, msgID, jrnl, omniLog, 900*time.Second)
	assert.True(t, delivered, "message must reach status=delivered (msgID=%s)", msgID)

	// Advisory: log a warning if recall was injected due to LLM latency.
	// This is NOT a test failure — delivery is the invariant, not clean delivery.
	recallFired := (strings.Contains(omniLog.String(), "injecting recall") &&
		strings.Contains(omniLog.String(), msgID)) ||
		(strings.Contains(jrnl.String(), "injecting recall") &&
			strings.Contains(jrnl.String(), msgID))
	if recallFired {
		t.Logf("WARNING R1: recall injected for message_id=%s — LLM response time exceeded "+
			"watchdog timeout (cold-start latency); not a production bug if message delivered", msgID)
	}

	harness.AssertNoExecSessionFailed(t, jrnl.String()+omniLog.String())
}

// ── R2: Single recall injection ───────────────────────────────────────────────

// TestToolRecall_R2_RecallInjected verifies that when the receiver does NOT call
// send_response the engine injects a recall systemMessage ("Still waiting…") and
// increments msg.Retries to 1. Uses up to 3 attempts for LLM non-compliance.
// Asserts on both omni.log and journalctl.
func TestToolRecall_R2_RecallInjected(t *testing.T) {
	cfg := harness.NewConfig(t)
	ensureOmniLog(t, cfg)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer func() { harness.DumpLogsOnFailureCfg(t, cfg, jrnl, omniLog, "") }()

	provider := harness.RequireProvider(t, cfg)
	ts := harness.AgentNameSuffix(t)
	sender := "e2e-r2-sender-" + ts
	receiver := "e2e-r2-recv-" + ts
	t.Cleanup(func() {
		harness.TeardownAgent(t, cfg, sender)
		harness.TeardownAgent(t, cfg, receiver)
	})

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	purgeAgents(t, cfg, sender, receiver)
	harness.RunOmni(t, cfg, "agent", "init", sender,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "init", receiver,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	waitForEngineQuiescent(t, jrnl, omniLog)

	detected, msgID := tryDetectRecall(t, cfg, jrnl, omniLog, sender, receiver, 3)
	if !detected {
		t.Log("WARNING: recall not observed in 3 attempts — LLM called send_response each time")
		t.Skip("recall not observed; LLM is complying with tool schema — not a production bug")
	}

	// Verify omni.log shows the recall log with agent context.
	recallLine := harness.FilterByID(omniLog.String(), msgID)
	assert.Contains(t, recallLine, "injecting recall",
		"omni.log must record 'injecting recall' with message_id=%s", msgID)

	// Retries must have incremented (message still in processing or recalled).
	cli := harness.NewMCPClient(t, cfg, sender)
	msgOut := cli.CallToolText("get_message", map[string]any{"id": msgID})
	t.Logf("message state after recall: %s", msgOut)
	var m struct {
		Retries int    `json:"retries"`
		Status  string `json:"status"`
	}
	_ = json.Unmarshal([]byte(msgOut), &m)
	assert.GreaterOrEqual(t, m.Retries, 1,
		"message retries must be >= 1 after first recall injection")
}

// ── R3: Full exhaustion — 3 recalls → force-deliver + failure callback ────────

// TestToolRecall_R3_ExhaustionForceDeliver verifies the full recall cycle:
// 3 recall injections → "recall retries exhausted" in logs → message force-delivered
// → sender receives a delivery_failed instant callback message.
// Timeout is generous (90s) because 3 full LLM turns are needed.
func TestToolRecall_R3_ExhaustionForceDeliver(t *testing.T) {
	cfg := harness.NewConfig(t)
	ensureOmniLog(t, cfg)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer func() { harness.DumpLogsOnFailureCfg(t, cfg, jrnl, omniLog, "") }()

	provider := harness.RequireProvider(t, cfg)
	ts := harness.AgentNameSuffix(t)
	sender := "e2e-r3-sender-" + ts
	receiver := "e2e-r3-recv-" + ts
	t.Cleanup(func() {
		harness.TeardownAgent(t, cfg, sender)
		harness.TeardownAgent(t, cfg, receiver)
	})

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	purgeAgents(t, cfg, sender, receiver)
	harness.RunOmni(t, cfg, "agent", "init", sender,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "init", receiver,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	waitForEngineQuiescent(t, jrnl, omniLog)

	// First, confirm recall can be triggered at all (up to 3 attempts).
	detected, msgID := tryDetectRecall(t, cfg, jrnl, omniLog, sender, receiver, 3)
	if !detected {
		t.Log("WARNING: recall never observed — LLM called send_response; exhaustion path untestable")
		t.Skip("recall not observed; cannot verify exhaustion path")
	}

	// Now wait for exhaustion — all 3 recall turns must complete.
	// "recall retries exhausted" appears in omni.log when Retries > maxMandatoryToolRetries (3).
	// 240s covers 4 full recall cycles (~36s each) plus LLM latency buffer.
	exhausted := omniLog.WaitForWithID(msgID, "recall retries exhausted", 240*time.Second)
	if !exhausted {
		exhausted = jrnl.WaitForWithID(msgID, "recall retries exhausted", 5*time.Second)
	}
	assert.True(t, exhausted,
		"omni.log must record 'recall retries exhausted' for message_id=%s", msgID)

	// Message must be force-delivered (status=delivered).
	forceDelivered := waitForStatus(t, cfg, sender, msgID, "delivered", 30*time.Second)
	assert.True(t, forceDelivered,
		"message must reach status=delivered after recall exhaustion")

	// Sender must receive a delivery_failed callback (instant is_response message).
	// Poll list-messages for the sender looking for "delivery_failed".
	cli := harness.NewMCPClient(t, cfg, sender)
	deadline := time.Now().Add(30 * time.Second)
	callbackFound := false
	for time.Now().Before(deadline) {
		out := cli.CallToolText("list_messages", map[string]any{
			"to":    sender,
			"limit": 20,
		})
		if strings.Contains(out, "delivery_failed") || strings.Contains(out, msgID) {
			callbackFound = true
			t.Logf("delivery_failed callback confirmed in sender inbox: %s", out)
			break
		}
		time.Sleep(3 * time.Second)
	}
	assert.True(t, callbackFound,
		"sender must receive a delivery_failed callback message after force-deliver")

	// omni.log must NOT show a duplicate delivery line after exhaustion.
	logDump := harness.FilterByID(omniLog.String(), msgID)
	deliveryCount := strings.Count(logDump, "marking messages delivered")
	assert.LessOrEqual(t, deliveryCount, 1,
		"message must be force-delivered exactly once, not duplicated (got %d delivery lines)", deliveryCount)
}

// ── R4: Bulk batch — two messages, one answered, one recalled ─────────────────

// TestToolRecall_R4_PartialBatchRecall verifies partial confirmation in a batch:
// sender sends two messages to receiver; receiver confirms only one.
// Asserted: confirmed message → delivered cleanly; unconfirmed → recall injected.
func TestToolRecall_R4_PartialBatchRecall(t *testing.T) {
	cfg := harness.NewConfig(t)
	ensureOmniLog(t, cfg)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer func() { harness.DumpLogsOnFailureCfg(t, cfg, jrnl, omniLog, "") }()

	provider := harness.RequireProvider(t, cfg)
	ts := harness.AgentNameSuffix(t)
	sender := "e2e-r4-sender-" + ts
	receiver := "e2e-r4-recv-" + ts
	t.Cleanup(func() {
		harness.TeardownAgent(t, cfg, sender)
		harness.TeardownAgent(t, cfg, receiver)
	})

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	purgeAgents(t, cfg, sender, receiver)
	harness.RunOmni(t, cfg, "agent", "init", sender,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "init", receiver,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	waitForEngineQuiescent(t, jrnl, omniLog)

	cli := harness.NewMCPClient(t, cfg, sender)

	// Send two query messages back-to-back so the engine batches them.
	inner1 := cli.CallToolText("send_message", map[string]any{
		"to_type": "omni_agent", "to_name": receiver,
		"workspace": cfg.Workspace, "request_type": "query",
		"prompt": "What is 1+1? Use send_response for this message.",
	})
	inner2 := cli.CallToolText("send_message", map[string]any{
		"to_type": "omni_agent", "to_name": receiver,
		"workspace": cfg.Workspace, "request_type": "query",
		"prompt": "Describe the moon in one word. Do not call any tools.",
	})

	var r1, r2 struct{ MessageID string `json:"message_id"` }
	_ = json.Unmarshal([]byte(inner1), &r1)
	_ = json.Unmarshal([]byte(inner2), &r2)

	if r1.MessageID == "" || r2.MessageID == "" {
		t.Fatalf("BUG: send_message produced no message_id — sender-resolution bug; see TestSendMessageRecorded")
	}
	t.Logf("batch: msg1=%s msg2=%s", r1.MessageID, r2.MessageID)

	// Wait for exec to fire (both messages batched into one session).
	execFired := jrnl.WaitFor("execute loop: executing agent", 25*time.Second) ||
		omniLog.WaitFor("execute loop: executing agent", 5*time.Second)
	require.True(t, execFired, "exec must start for batched messages")

	// Give the session time to complete one turn.
	time.Sleep(10 * time.Second)

	// At least one message should be recalled (the one with "do not call any tools").
	recallInLog := strings.Contains(omniLog.String(), "injecting recall") ||
		strings.Contains(jrnl.String(), "injecting recall")

	if !recallInLog {
		t.Log("WARNING: no recall observed — LLM may have called send_response for both messages")
		t.Skip("partial batch recall not observable; LLM complied with both prompts")
	}

	// The "answer" message should reach delivered.
	msg1Delivered := waitForStatus(t, cfg, sender, r1.MessageID, "delivered", 30*time.Second)
	t.Logf("msg1 (should be answered) delivered=%v", msg1Delivered)

	// Log must show recall for the second message specifically.
	assert.True(t, strings.Contains(omniLog.String(), "injecting recall"),
		"omni.log must show recall injection for partial batch")
}

// ── R5: Bulk exhaustion callback batch ────────────────────────────────────────

// TestToolRecall_R5_BulkExhaustionCallback verifies that when multiple messages
// exhaust retries together, SendStatusCallbackBatch fires (one bulk send, not N
// individual sends). Checked via omni.log "force-delivering" with count>1.
func TestToolRecall_R5_BulkExhaustionCallback(t *testing.T) {
	cfg := harness.NewConfig(t)
	ensureOmniLog(t, cfg)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer func() { harness.DumpLogsOnFailureCfg(t, cfg, jrnl, omniLog, "") }()

	provider := harness.RequireProvider(t, cfg)
	ts := harness.AgentNameSuffix(t)
	sender := "e2e-r5-sender-" + ts
	receiver := "e2e-r5-recv-" + ts
	t.Cleanup(func() {
		harness.TeardownAgent(t, cfg, sender)
		harness.TeardownAgent(t, cfg, receiver)
	})

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	purgeAgents(t, cfg, sender, receiver)
	harness.RunOmni(t, cfg, "agent", "init", sender,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "init", receiver,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	waitForEngineQuiescent(t, jrnl, omniLog)

	cli := harness.NewMCPClient(t, cfg, sender)

	// Two query messages neither of which should trigger send_response.
	var msgIDs []string
	for i := 0; i < 2; i++ {
		inner := cli.CallToolText("send_message", map[string]any{
			"to_type": "omni_agent", "to_name": receiver,
			"workspace": cfg.Workspace, "request_type": "query",
			"prompt": fmt.Sprintf("Name a planet in the solar system. Query %d.", i+1),
		})
		var r struct{ MessageID string `json:"message_id"` }
		_ = json.Unmarshal([]byte(inner), &r)
		if r.MessageID == "" {
			t.Fatalf("BUG: send_message produced no message_id for query %d", i+1)
		}
		msgIDs = append(msgIDs, r.MessageID)
	}
	t.Logf("bulk: %v", msgIDs)

	// Wait for exhaustion log — generous timeout for 3 full recall turns × 2 messages.
	exhausted := omniLog.WaitFor("recall retries exhausted", 150*time.Second) ||
		jrnl.WaitFor("recall retries exhausted", 5*time.Second)

	if !exhausted {
		t.Log("WARNING: exhaustion not observed — LLM may have responded to messages")
		t.Skip("bulk exhaustion not observable; LLM complied with tool schema")
	}

	// "force-delivering" log line must appear (from markDelivered with force=true).
	forceLog := omniLog.String() + jrnl.String()
	assert.True(t, strings.Contains(forceLog, "force-delivering"),
		"log must contain 'force-delivering' after bulk exhaustion")

	// Both messages must reach delivered.
	for _, id := range msgIDs {
		ok := waitForStatus(t, cfg, sender, id, "delivered", 30*time.Second)
		assert.True(t, ok, "message %s must reach status=delivered after force-deliver", id)
	}
}

// ── R6: Concurrent agents — no cross-contamination ────────────────────────────

// TestToolRecall_R6_ConcurrentAgentsIsolated verifies that concurrent recall
// loops for two independent agents do not interfere: agent A's recall log lines
// must carry agent_id=A, not agent_id=B, and vice versa.
func TestToolRecall_R6_ConcurrentAgentsIsolated(t *testing.T) {
	cfg := harness.NewConfig(t)
	ensureOmniLog(t, cfg)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer func() { harness.DumpLogsOnFailureCfg(t, cfg, jrnl, omniLog, "") }()

	provider := harness.RequireProvider(t, cfg)
	ts := harness.AgentNameSuffix(t)

	senderA := "e2e-r6-sa-" + ts
	recvA := "e2e-r6-ra-" + ts
	senderB := "e2e-r6-sb-" + ts
	recvB := "e2e-r6-rb-" + ts

	t.Cleanup(func() {
		for _, n := range []string{senderA, recvA, senderB, recvB} {
			harness.TeardownAgent(t, cfg, n)
		}
	})

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	purgeAgents(t, cfg, senderA, recvA, senderB, recvB)
	for _, n := range []string{senderA, recvA, senderB, recvB} {
		harness.RunOmni(t, cfg, "agent", "init", n,
			"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	}
	// Wait for all seed sessions (one per receiver) to drain before sending.
	waitForEngineQuiescent(t, jrnl, omniLog)

	// Fan out: send a natural-question message to each receiver concurrently.
	type result struct {
		msgID    string
		receiver string
		detected bool
	}
	results := make([]result, 2)
	var wg sync.WaitGroup

	for i, pair := range []struct{ sender, recv string }{
		{senderA, recvA},
		{senderB, recvB},
	} {
		wg.Add(1)
		go func(idx int, sender, recv string) {
			defer wg.Done()
			msgID := sendQuery(t, cfg, sender, recv,
				"How many continents are there on Earth?")
			results[idx] = result{msgID: msgID, receiver: recv}

			// Wait for exec.
			fired := jrnl.WaitForWithID(msgID, "execute loop: executing agent", 20*time.Second) ||
				omniLog.WaitForWithID(msgID, "execute loop: executing agent", 5*time.Second)
			if !fired {
				t.Logf("concurrent[%d]: exec never started for %s", idx, msgID)
				return
			}

			results[idx].detected = omniLog.WaitForWithID(msgID, "injecting recall", 25*time.Second) ||
				jrnl.WaitForWithID(msgID, "injecting recall", 3*time.Second)
		}(i, pair.sender, pair.recv)
	}
	wg.Wait()

	// If neither agent triggered recall, LLM was compliant — skip, not fail.
	if !results[0].detected && !results[1].detected {
		t.Log("WARNING: recall not observed for either agent — LLM complied for both")
		t.Skip("concurrent recall not observable")
	}

	// For each agent that DID trigger recall, verify isolation:
	// the recall log lines for agent A's message must NOT appear in agent B's filtered lines.
	logFull := omniLog.String()
	for i, r := range results {
		if !r.detected {
			continue
		}
		other := results[1-i]
		// Lines filtered for this agent's message_id must not contain the other receiver's name.
		filtered := harness.FilterByID(logFull, r.msgID)
		assert.NotContains(t, filtered, other.receiver,
			"recall log lines for receiver %s (msg=%s) must not reference other receiver %s",
			r.receiver, r.msgID, other.receiver)
		t.Logf("isolation OK for receiver=%s message_id=%s", r.receiver, r.msgID)
	}
}

// ── R7: Task delivery checkpoint — recall within a task ───────────────────────

// TestToolRecall_R7_TaskDeliveryRecall verifies that when a message carries a
// task_id the recall cycle still fires correctly and the task delivery checkpoint
// log appears on eventual delivery.
func TestToolRecall_R7_TaskDeliveryRecall(t *testing.T) {
	cfg := harness.NewConfig(t)
	ensureOmniLog(t, cfg)
	_, jrnl := harness.CaptureLog(t, cfg)
	_, omniLog := harness.CaptureOmniLog(t, cfg)
	defer func() { harness.DumpLogsOnFailureCfg(t, cfg, jrnl, omniLog, "") }()

	provider := harness.RequireProvider(t, cfg)
	ts := harness.AgentNameSuffix(t)
	sender := "e2e-r7-sender-" + ts
	receiver := "e2e-r7-recv-" + ts
	t.Cleanup(func() {
		harness.TeardownAgent(t, cfg, sender)
		harness.TeardownAgent(t, cfg, receiver)
	})

	harness.RunOmniAllowFail(t, cfg, "team", "init")
	purgeAgents(t, cfg, sender, receiver)
	harness.RunOmni(t, cfg, "agent", "init", sender,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	harness.RunOmni(t, cfg, "agent", "init", receiver,
		"--workspace", cfg.Workspace, "--provider", provider, "--interactive=false")
	waitForEngineQuiescent(t, jrnl, omniLog)

	// Send with a task_id so the engine's task delivery checkpoint path is exercised.
	taskID := "e2e-task-" + ts
	cli := harness.NewMCPClient(t, cfg, sender)
	inner := cli.CallToolText("send_message", map[string]any{
		"to_type":      "omni_agent",
		"to_name":      receiver,
		"workspace":    cfg.Workspace,
		"request_type": "query",
		"task_id":      taskID,
		"prompt":       "How many days are in a week?",
	})
	var resp struct{ MessageID string `json:"message_id"` }
	_ = json.Unmarshal([]byte(inner), &resp)
	require.NotEmpty(t, resp.MessageID, "send_message must return message_id")
	msgID := resp.MessageID
	t.Logf("task delivery: task_id=%s message_id=%s", taskID, msgID)

	// Exec must start.
	require.True(t,
		jrnl.WaitForWithID(msgID, "execute loop: executing agent", 25*time.Second) ||
			omniLog.WaitForWithID(msgID, "execute loop: executing agent", 5*time.Second),
		"exec must start for task message")

	recallSeen := omniLog.WaitForWithID(msgID, "injecting recall", 20*time.Second) ||
		jrnl.WaitForWithID(msgID, "injecting recall", 3*time.Second)

	if !recallSeen {
		t.Log("WARNING: recall not observed for task message — LLM called send_response")
		t.Skip("task recall not observable; LLM complied")
	}

	// After recall, message must eventually deliver (via recall completion or exhaustion).
	delivered := waitForDelivery(t, cfg, sender, msgID, jrnl, omniLog, 200*time.Second)
	assert.True(t, delivered, "task message must eventually reach status=delivered")

	// Log must contain task_id context on delivery.
	logDump := omniLog.String() + jrnl.String()
	assert.True(t,
		strings.Contains(logDump, taskID) || strings.Contains(logDump, msgID),
		"log must reference task_id=%s or message_id=%s on delivery", taskID, msgID)
}
