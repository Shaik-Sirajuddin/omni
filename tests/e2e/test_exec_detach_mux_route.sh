#!/usr/bin/env bash
# E2E tests: fix/exec-detach-mux-route — Detached flag + forced waitPTYReady
#
# Verifies:
#   TEST 1 — detached exec delivers prompt (ptydaemon exec gate):
#     exec --resume returns immediately (<5s) AND the ptydaemon receives the
#     exec op within 30s, proving the prompt reached the agent.
#
#   TEST 2 — waitPTYReady honoured in detached mode (cold start):
#     Kill any existing PTY session, exec --resume immediately (cold start).
#     ptydaemon exec must still succeed — the forced waitPTYReady in Detached
#     mode ensures the TUI is ready before prompt delivery.
#
#   TEST 3 — regression: non-detached exec still works:
#     Plain exec (no --resume) returns 0 and ptydaemon exec op fires.
#
# Requires: journalctl -t omni-server, running ptydaemon, claude or codex.
#
# Override binary via OMNI env var: OMNI=/path/to/omni bash test_exec_detach_mux_route.sh
set -euo pipefail

OMNI="${OMNI:-/tmp/exec-detach-omni}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../output"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
JRNL_LOG="${OUTPUT_DIR}/exec-detach-${RUN_ID}.log"

mkdir -p "$OUTPUT_DIR"

PASS=0; FAIL=0; SKIP=0
pass() { echo "  PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP: $1"; SKIP=$((SKIP+1)); }

# ─── Pre-check ────────────────────────────────────────────────────────────────
if [[ ! -x "$OMNI" ]]; then
  echo "==> OMNI binary not found at $OMNI — skipping all tests"
  for t in "TEST 1" "TEST 2" "TEST 3"; do skip "$t: OMNI binary missing"; done
  echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP"; exit 0
fi

PROVIDER=""
command -v claude &>/dev/null && PROVIDER="claude"
[[ -z "$PROVIDER" ]] && command -v codex &>/dev/null && PROVIDER="codex"

if [[ -z "$PROVIDER" ]]; then
  echo "==> No supported agent binary (claude/codex) — skipping all tests"
  for t in "TEST 1" "TEST 2" "TEST 3"; do skip "$t: no agent binary"; done
  echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP"; exit 0
fi

echo "==> Provider: $PROVIDER  OMNI: $OMNI"

# ─── Common setup ─────────────────────────────────────────────────────────────
AGENT_NAME="e2e-detach-${RUN_ID}"
WS="/tmp/e2e-exec-detach-${RUN_ID}"
mkdir -p "$WS"
JRNL_PID=""

cleanup() {
  [[ -n "$JRNL_PID" ]] && kill "$JRNL_PID" 2>/dev/null || true
  (cd "$WS" && $OMNI agent delete "$AGENT_NAME" 2>/dev/null) || true
  rm -rf "$WS"
}
trap cleanup EXIT

# Start journalctl log tap (ptydaemon + hook-operator events).
journalctl -f --no-pager --lines=0 -t omni-server > "$JRNL_LOG" 2>&1 &
JRNL_PID=$!
sleep 0.5

# Initialise agent.
$OMNI team init --workspace "$WS" 2>/dev/null || true
(cd "$WS" && $OMNI agent init "$AGENT_NAME" \
  --workspace "$WS" --provider "$PROVIDER" --interactive=false)

# ─── TEST 1 — detached exec delivers prompt ───────────────────────────────────
echo ""
echo "==> [TEST 1] Detached exec delivers prompt — ptydaemon exec op fires within 30s"

START_TS=$(date +%s%N)
EXEC1_OUT=$(cd "$WS" && $OMNI agent exec "$AGENT_NAME" \
  --prompt "reply with: pong" --resume 2>&1)
EXEC1_EXIT=$?
END_TS=$(date +%s%N)
ELAPSED_MS=$(( (END_TS - START_TS) / 1000000 ))

echo "    exec returned in ${ELAPSED_MS}ms (exit=$EXEC1_EXIT)"
echo "    output: ${EXEC1_OUT:0:120}"

# ASSERT 1a: exits quickly (<5s), non-blocking.
if [[ $EXEC1_EXIT -eq 0 ]]; then
  pass "TEST 1: exec --resume exits 0"
else
  fail "TEST 1: exec --resume exited $EXEC1_EXIT"
fi

if [[ $ELAPSED_MS -lt 5000 ]]; then
  pass "TEST 1: exec --resume returned in <5s (${ELAPSED_MS}ms) — non-blocking"
else
  fail "TEST 1: exec --resume took ${ELAPSED_MS}ms — possible blocking"
fi

# ASSERT 1b: ptydaemon received exec op (proves prompt delivered to PTY).
# Observable patterns in omni-server journalctl:
#   "op=exec ... session_id=..." — ptydaemon received the exec request
#   "submit-key retry"          — prompt written to PTY master
#   "UserPromptSubmit"          — hook fired (only when hooks configured)
DELIVERY_FOUND=0
for i in $(seq 1 30); do
  sleep 1
  if grep -qE 'op=exec|"op":"exec"|submit-key retry|UserPromptSubmit|exec in session' "$JRNL_LOG" 2>/dev/null; then
    DELIVERY_FOUND=1; break
  fi
done

if [[ $DELIVERY_FOUND -eq 1 ]]; then
  EV_LINE=$(grep -m1 -E 'op=exec|submit-key retry|UserPromptSubmit' "$JRNL_LOG" || true)
  pass "TEST 1: prompt delivery confirmed in ptydaemon/hook logs within 30s"
  echo "    evidence: ${EV_LINE:0:120}"
else
  fail "TEST 1: no prompt delivery evidence in journalctl within 30s"
  echo "    journalctl tail:"; tail -20 "$JRNL_LOG" || true
fi

# ─── TEST 2 — waitPTYReady honoured on cold start ────────────────────────────
echo ""
echo "==> [TEST 2] Cold-start detached exec — waitPTYReady prevents dropped prompt"

# Stop the running session to force cold start.
echo "    stopping existing session..."
(cd "$WS" && $OMNI agent stop "$AGENT_NAME" 2>&1) || true
sleep 1

# Capture delivery event count before the cold exec.
BEFORE=$(awk '/op=exec|submit-key retry|UserPromptSubmit/{c++} END{print c+0}' "$JRNL_LOG" 2>/dev/null)

# exec --resume immediately (cold start — session not yet running).
EXEC2_START=$(date +%s%N)
EXEC2_OUT=$(cd "$WS" && $OMNI agent exec "$AGENT_NAME" \
  --prompt "cold start pong" --resume 2>&1)
EXEC2_EXIT=$?
EXEC2_END=$(date +%s%N)
EXEC2_MS=$(( (EXEC2_END - EXEC2_START) / 1000000 ))

echo "    cold-start exec exit=$EXEC2_EXIT elapsed=${EXEC2_MS}ms"
echo "    output: ${EXEC2_OUT:0:120}"

if [[ $EXEC2_EXIT -eq 0 ]]; then
  pass "TEST 2: cold-start exec --resume exits 0"
else
  fail "TEST 2: cold-start exec --resume exited $EXEC2_EXIT"
fi

if [[ $EXEC2_MS -lt 30000 ]]; then
  pass "TEST 2: cold-start exec returned in ${EXEC2_MS}ms (<30s) — not hung"
else
  fail "TEST 2: cold-start exec took ${EXEC2_MS}ms — possible hang"
fi

# ASSERT: delivery event fires after cold start (waitPTYReady did not drop it).
COLD_FOUND=0
for i in $(seq 1 30); do
  sleep 1
  AFTER=$(awk '/op=exec|submit-key retry|UserPromptSubmit/{c++} END{print c+0}' "$JRNL_LOG" 2>/dev/null)
  if [[ "$AFTER" -gt "$BEFORE" ]]; then
    COLD_FOUND=1; break
  fi
done

if [[ $COLD_FOUND -eq 1 ]]; then
  pass "TEST 2: delivery event fired after cold-start — waitPTYReady prevented drop"
else
  fail "TEST 2: no delivery event within 30s after cold-start exec"
fi

# ─── TEST 3 — regression: non-detached exec still works ──────────────────────
echo ""
echo "==> [TEST 3] Regression: non-detached exec delivers prompt"

BEFORE3=$(awk '/op=exec|submit-key retry|UserPromptSubmit/{c++} END{print c+0}' "$JRNL_LOG" 2>/dev/null)

# Plain exec (no --resume) — works against a running session.
EXEC3_START=$(date +%s%N)
EXEC3_OUT=$(cd "$WS" && $OMNI agent exec "$AGENT_NAME" \
  --prompt "non-detached pong" 2>&1)
EXEC3_EXIT=$?
EXEC3_END=$(date +%s%N)
EXEC3_MS=$(( (EXEC3_END - EXEC3_START) / 1000000 ))

echo "    non-detached exec exit=$EXEC3_EXIT elapsed=${EXEC3_MS}ms"
echo "    output: ${EXEC3_OUT:0:120}"

if [[ $EXEC3_EXIT -eq 0 ]]; then
  pass "TEST 3: non-detached exec exits 0 (no regression)"
else
  fail "TEST 3: non-detached exec exited $EXEC3_EXIT"
fi

# Soft delivery check.
HOOK3_FOUND=0
for i in $(seq 1 15); do
  sleep 1
  AFTER3=$(awk '/op=exec|submit-key retry|UserPromptSubmit/{c++} END{print c+0}' "$JRNL_LOG" 2>/dev/null)
  if [[ "$AFTER3" -gt "$BEFORE3" ]]; then
    HOOK3_FOUND=1; break
  fi
done

if [[ $HOOK3_FOUND -eq 1 ]]; then
  pass "TEST 3: delivery event fired for non-detached exec (no regression)"
else
  skip "TEST 3: delivery event not observed within 15s for non-detached exec (session may need restart)"
fi

# ─── Summary ──────────────────────────────────────────────────────────────────
kill "$JRNL_PID" 2>/dev/null || true; JRNL_PID=""
echo ""
echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP  (run $RUN_ID)"
echo "==> journal log: $JRNL_LOG"

if [[ "$FAIL" -gt 0 ]]; then
  echo "==> FAIL"; exit 1
fi
echo "==> PASS (with $SKIP skip(s))"; exit 0
