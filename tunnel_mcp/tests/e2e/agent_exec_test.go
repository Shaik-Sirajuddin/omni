//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

const (
	execTestAgent  = "e2e-exec-test"
	codexTestAgent = "e2e-codex-test"
	execStartWait  = 10 * time.Second
	execPromptWait = 45 * time.Second // max wait for exec in session to appear
)

// TestAgentExecNoSession verifies that resuming an agent with --detach then
// calling exec delivers the prompt without a TTY.
func TestAgentExecNoSession(t *testing.T) {
	cfg := newConfig(t)
	teardownAgent(t, cfg, execTestAgent)
	t.Cleanup(func() { teardownAgent(t, cfg, execTestAgent) })

	_, logBuf := captureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)

	runOmni(t, cfg, "team", "init")
	runOmni(t, cfg, "agent", "init", execTestAgent,
		"--workspace", cfg.workspace, "--provider", "claude", "--interactive=false")

	// --detach: starts session in ptydaemon, returns immediately, no TTY needed
	runOmni(t, cfg, "agent", "resume", execTestAgent, "--detach", "--workspace", cfg.workspace)
	time.Sleep(execStartWait)

	out := runOmni(t, cfg, "agent", "exec", execTestAgent,
		"--prompt", "reply with the single word: pong")

	// Stream-wait: accept either CLI confirmation or server-side ExecInSession log.
	delivered := strings.Contains(out, "prompt sent") ||
		logBuf.WaitFor("exec in session", execPromptWait)
	if !delivered {
		t.Errorf("prompt not delivered within %s; exec output=%q", execPromptWait, out)
	}

	log := logBuf.String()
	assertNoLogErrors(t, log)
	assertNoExecSessionFailed(t, log)
	if t.Failed() {
		t.Logf("=== journalctl (%d bytes) ===\n%s", len(log), log)
	}
}

// TestAgentResumeDetached verifies that `omni agent resume --detach` exits 0
// without attaching to a terminal, and a subsequent exec delivers a prompt.
func TestAgentResumeDetached(t *testing.T) {
	cfg := newConfig(t)
	name := execTestAgent + "-detach"
	teardownAgent(t, cfg, name)
	t.Cleanup(func() { teardownAgent(t, cfg, name) })

	_, logBuf := captureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)

	runOmni(t, cfg, "team", "init")
	runOmni(t, cfg, "agent", "init", name,
		"--workspace", cfg.workspace, "--provider", "claude", "--interactive=false")

	runOmni(t, cfg, "agent", "resume", name, "--detach", "--workspace", cfg.workspace)
	time.Sleep(execStartWait)

	out := runOmni(t, cfg, "agent", "exec", name,
		"--prompt", "reply with the single word: pong")

	delivered := strings.Contains(out, "prompt sent") ||
		logBuf.WaitFor("exec in session", execPromptWait)
	if !delivered {
		t.Errorf("prompt not delivered within %s; exec output=%q", execPromptWait, out)
	}

	log := logBuf.String()
	assertNoLogErrors(t, log)
	assertNoExecSessionFailed(t, log)
	if t.Failed() {
		t.Logf("=== journalctl (%d bytes) ===\n%s", len(log), log)
	}
}

// TestCodexAgentDelivery verifies a claude agent delivers a tunnel-mcp send_message
// to a codex-provider receiver. Validates sender (claude) calls the tool successfully.
func TestCodexAgentDelivery(t *testing.T) {
	cfg := newConfig(t)
	sender := "e2e-codex-sender"
	receiver := codexTestAgent

	teardownAgent(t, cfg, sender)
	teardownAgent(t, cfg, receiver)
	t.Cleanup(func() {
		teardownAgent(t, cfg, sender)
		teardownAgent(t, cfg, receiver)
	})

	_, logBuf := captureLog(t, cfg)
	time.Sleep(300 * time.Millisecond)

	runOmni(t, cfg, "team", "init")
	runOmni(t, cfg, "agent", "init", sender,
		"--workspace", cfg.workspace, "--provider", "claude", "--interactive=false")
	runOmni(t, cfg, "agent", "init", receiver,
		"--workspace", cfg.workspace, "--provider", "codex", "--interactive=false")

	runOmni(t, cfg, "agent", "resume", receiver, "--detach", "--workspace", cfg.workspace)
	runOmni(t, cfg, "agent", "resume", sender, "--detach", "--workspace", cfg.workspace)
	time.Sleep(execStartWait)

	prompt := "Use the tunnel-mcp send_message tool to send 'hi from claude' to the agent named '" + receiver + "'. Call the tool now."
	runOmni(t, cfg, "agent", "exec", sender, "--prompt", prompt)

	if !logBuf.WaitFor("tool=send_message", sendMessageWait) {
		t.Errorf("send_message tool call not observed within %s", sendMessageWait)
		t.Logf("=== journalctl (%d bytes) ===\n%s", len(logBuf.String()), logBuf.String())
		return
	}
	if !logBuf.WaitFor("exec in session", execInSessionWait) {
		t.Logf("WARN: exec in session not observed for codex receiver within %s (server-bugs.md #7)", execInSessionWait)
	}

	log := logBuf.String()
	assertNoLogErrors(t, log)
	assertNoExecSessionFailed(t, log)
	if t.Failed() {
		t.Logf("=== journalctl (%d bytes) ===\n%s", len(log), log)
	}
}
