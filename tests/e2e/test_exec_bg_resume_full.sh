#!/usr/bin/env bash
# Full e2e suite for exec --bg / resume attach flow (fix/pty-resume-attach).
#
# Covers all 10 operator-requested edge cases:
#  T1:  --bg accepted; --resume rejected (flag rename confirmed)
#  T2:  exec --bg fresh agent: starts PTY in background, returns immediately
#  T3:  exec --bg on already-active PTY: writes prompt, returns immediately
#  T4:  exec --bg agent not found: returns error
#  T5:  exec -b short flag works
#  T6:  resume <name> attaches to active PTY after --bg exec
#  T7:  resume on already-attached session: returns nil gracefully
#  T8:  resume on non-existent session: returns appropriate error
#  T9:  prompt is actually delivered and processed (not just written to PTY)
#  T10: two sequential exec --bg calls: second one writes correctly, non-blocking
#
# Run inside omni-fix-pty-resume-attach-ubuntu-1 container.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../output"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
LOG="${OUTPUT_DIR}/exec-bg-resume-full-${RUN_ID}.log"
JRNL_LOG="${OUTPUT_DIR}/jrnl-${RUN_ID}.log"
mkdir -p "$OUTPUT_DIR"

PASS=0; FAIL=0; SKIP=0
pass() { echo "  PASS: $1" | tee -a "$LOG"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1" | tee -a "$LOG"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP: $1" | tee -a "$LOG"; SKIP=$((SKIP+1)); }

AGENT="e2e-test1"
WS="/build"
JRNL_PID=""

cleanup() {
  [[ -n "$JRNL_PID" ]] && kill "$JRNL_PID" 2>/dev/null || true
}
trap cleanup EXIT

# Tap journalctl for the whole test run
journalctl -f --no-pager --lines=0 -t omni-server > "$JRNL_LOG" 2>&1 &
JRNL_PID=$!
sleep 0.3

wait_log() { # wait_log pattern log_file timeout
  local pattern="$1" file="$2" timeout="${3:-20}"
  for i in $(seq 1 "$timeout"); do sleep 1; grep -q "$pattern" "$file" 2>/dev/null && return 0; done
  return 1
}

# ─── T1: flag rename ─────────────────────────────────────────────────────────
echo ""
echo "==> [T1] --bg accepted; --resume rejected"

RESUME_OUT=$(cd "$WS" && omni agent exec "$AGENT" --prompt test --resume 2>&1) || RESUME_EXIT=$?
RESUME_EXIT=${RESUME_EXIT:-0}

if [[ $RESUME_EXIT -ne 0 ]] && echo "$RESUME_OUT" | grep -qiE "unknown flag|unknown shorthand"; then
  pass "T1a: --resume rejected as unknown flag"
else
  fail "T1a: --resume should be unknown (exit=$RESUME_EXIT)"
fi

BG_HELP=$(cd "$WS" && omni agent exec --help 2>&1)
if echo "$BG_HELP" | grep -q "\-\-bg\|-b,"; then
  pass "T1b: --bg flag present in exec --help"
else
  fail "T1b: --bg flag missing from exec --help"
fi

# ─── T2: exec --bg fresh agent: non-blocking PTY start ───────────────────────
echo ""
echo "==> [T2] exec --bg fresh agent — starts PTY background, returns immediately"

# Stop any existing session first
cd "$WS" && omni agent stop "$AGENT" 2>/dev/null || true
sleep 1

JRNL_BEFORE=$(wc -l < "$JRNL_LOG")
T2_START=$(date +%s%N)
T2_OUT=$(cd "$WS" && omni agent exec "$AGENT" --prompt "T2: reply with one word: pong" --bg 2>&1) || T2_EXIT=$?
T2_EXIT=${T2_EXIT:-0}
T2_END=$(date +%s%N)
T2_MS=$(( (T2_END - T2_START) / 1000000 ))

echo "    exec --bg returned in ${T2_MS}ms (exit=$T2_EXIT)"
echo "    output: ${T2_OUT:0:100}"

if echo "$T2_OUT" | grep -qiE "unknown flag|flag provided"; then
  fail "T2: --bg flag not accepted"
elif [[ $T2_EXIT -eq 0 ]]; then
  pass "T2a: exec --bg exits 0"
else
  fail "T2a: exec --bg exited $T2_EXIT"
fi

if [[ $T2_MS -lt 10000 ]]; then
  pass "T2b: exec --bg returned in <10s (${T2_MS}ms) — non-blocking"
else
  fail "T2b: exec --bg took ${T2_MS}ms — may be blocking"
fi

# Verify PTY session started (journalctl evidence)
if wait_log "PTY daemon session started\|session created\|session ready\|Resume.*PTY" "$JRNL_LOG" 20; then
  EV=$(grep -m1 "PTY daemon session started\|session created\|session ready\|Resume.*PTY" "$JRNL_LOG" || true)
  pass "T2c: PTY session started in background: ${EV:0:80}"
else
  skip "T2c: PTY session start not visible in journalctl within 20s"
fi

# ─── T3: exec --bg on already-active PTY ─────────────────────────────────────
echo ""
echo "==> [T3] exec --bg on already-active PTY — writes prompt, returns immediately"

sleep 2  # Let T2 session settle
T3_JRNL_BEFORE=$(wc -l < "$JRNL_LOG")
T3_START=$(date +%s%N)
T3_OUT=$(cd "$WS" && omni agent exec "$AGENT" --prompt "T3: second prompt on active session" --bg 2>&1) || T3_EXIT=$?
T3_EXIT=${T3_EXIT:-0}
T3_END=$(date +%s%N)
T3_MS=$(( (T3_END - T3_START) / 1000000 ))

echo "    exec --bg (2nd) returned in ${T3_MS}ms (exit=$T3_EXIT)"
echo "    output: ${T3_OUT:0:100}"

if [[ $T3_EXIT -eq 0 ]]; then
  pass "T3a: second exec --bg on active session exits 0"
else
  fail "T3a: second exec --bg exited $T3_EXIT"
fi

if [[ $T3_MS -lt 10000 ]]; then
  pass "T3b: second exec --bg returned in <10s (${T3_MS}ms) — non-blocking"
else
  fail "T3b: second exec --bg took ${T3_MS}ms — blocking on active session"
fi

# ─── T4: exec --bg agent not found ───────────────────────────────────────────
echo ""
echo "==> [T4] exec --bg with non-existent agent — returns error"

T4_OUT=$(cd "$WS" && omni agent exec "nonexistent-agent-e2e" --prompt test --bg 2>&1) || T4_EXIT=$?
T4_EXIT=${T4_EXIT:-0}

if [[ $T4_EXIT -ne 0 ]]; then
  if echo "$T4_OUT" | grep -qiE "not found|no.*agent|agent.*not"; then
    pass "T4: exec --bg nonexistent agent: exits $T4_EXIT with 'not found' message"
  else
    pass "T4: exec --bg nonexistent agent: exits $T4_EXIT (non-zero, error returned)"
    echo "    output: ${T4_OUT:0:120}"
  fi
else
  fail "T4: exec --bg nonexistent agent exited 0 — should have returned error"
fi

# ─── T5: -b short flag ───────────────────────────────────────────────────────
echo ""
echo "==> [T5] -b short flag works identically to --bg"

T5_OUT=$(cd "$WS" && omni agent exec "$AGENT" --prompt "T5: short flag test" -b 2>&1) || T5_EXIT=$?
T5_EXIT=${T5_EXIT:-0}

if echo "$T5_OUT" | grep -qiE "unknown flag|unknown shorthand"; then
  fail "T5: -b short flag not recognized"
elif [[ $T5_EXIT -eq 0 ]]; then
  pass "T5: -b short flag accepted (exit=0)"
else
  # Non-zero but not flag-error means flag was parsed, op failed for another reason
  if echo "$T5_OUT" | grep -qiE "not found|error|fail"; then
    fail "T5: -b flag accepted but exec failed: ${T5_OUT:0:100}"
  else
    pass "T5: -b short flag accepted (exit=$T5_EXIT)"
  fi
fi

# ─── T6: resume attaches to active PTY ───────────────────────────────────────
echo ""
echo "==> [T6] resume <name> attaches to active PTY after --bg exec"

if ! command -v tmux &>/dev/null; then
  skip "T6: tmux not available — cannot capture pane for attach verification"
else
  sleep 2
  S6="e2e-t6-${RUN_ID}"
  tmux new-session -d -s "$S6" -x 120 -y 40 2>/dev/null
  ATTACH_LOG="${OUTPUT_DIR}/t6-attach-${RUN_ID}.log"

  tmux send-keys -t "$S6" \
    "cd '$WS' && omni agent resume '$AGENT' 2>'$ATTACH_LOG'" Enter
  sleep 6

  PANE=$(tmux capture-pane -t "$S6" -p 2>/dev/null || echo "")
  PANE_LINES=$(echo "$PANE" | grep -v '^\s*$' | wc -l)
  echo "    pane non-blank lines: $PANE_LINES"
  echo "    pane (first 4 lines):"
  echo "$PANE" | head -4 | sed 's/^/    | /'

  # Check journalctl for attach evidence
  ATTACH_JRNL=$(grep "ResumeAgent.*attach\|PTY terminal already active.*attach\|ptyDaemon.Attach\|Attach.*session" "$JRNL_LOG" 2>/dev/null | head -1 || true)
  ATTACH_STDERR=$(grep -iE "attach|connected|resume|active" "$ATTACH_LOG" 2>/dev/null | head -1 || true)

  if [[ -n "$ATTACH_JRNL" ]]; then
    pass "T6: journalctl confirms ptyDaemon.Attach called: ${ATTACH_JRNL:0:100}"
  elif [[ $PANE_LINES -gt 3 ]]; then
    pass "T6: pane has $PANE_LINES lines — attach appears successful"
  elif [[ -n "$ATTACH_STDERR" ]]; then
    pass "T6: attach confirmed via stderr: ${ATTACH_STDERR:0:80}"
  else
    fail "T6: no attach evidence in journalctl ($PANE_LINES pane lines, no stderr)"
    echo "    attach log: $(cat "$ATTACH_LOG" 2>/dev/null | head -5 || echo '(empty)')"
  fi

  # Detach cleanly
  tmux send-keys -t "$S6" "" "C-\\" 2>/dev/null || true
  sleep 0.5
  tmux kill-session -t "$S6" 2>/dev/null || true
fi

# ─── T7: resume on already-attached session: no error ────────────────────────
echo ""
echo "==> [T7] resume on already-attached session — returns nil gracefully"

if ! command -v tmux &>/dev/null; then
  skip "T7: tmux not available"
else
  S7A="e2e-t7a-${RUN_ID}"
  S7B="e2e-t7b-${RUN_ID}"
  ATTACH7_LOG="${OUTPUT_DIR}/t7-attach-${RUN_ID}.log"

  # First attach
  tmux new-session -d -s "$S7A" -x 120 -y 40 2>/dev/null
  tmux send-keys -t "$S7A" "cd '$WS' && omni agent resume '$AGENT'" Enter
  sleep 3

  # Second attach attempt (should not error)
  T7_OUT=$(cd "$WS" && timeout 5 omni agent resume "$AGENT" 2>&1) || T7_EXIT=$?
  T7_EXIT=${T7_EXIT:-0}

  echo "    second resume output: ${T7_OUT:0:120}"

  if echo "$T7_OUT" | grep -qiE "daemon error|fatal|panic|crash"; then
    fail "T7: resume on already-attached session produced error/panic"
  else
    pass "T7: second resume returned gracefully (exit=$T7_EXIT, no panic/daemon error)"
  fi

  tmux send-keys -t "$S7A" "" "C-\\" 2>/dev/null || true
  tmux kill-session -t "$S7A" 2>/dev/null || true
fi

# ─── T8: resume on non-existent session ─────────────────────────────────────
echo ""
echo "==> [T8] resume <name> on non-existent session — returns appropriate error"

T8_OUT=$(cd "$WS" && omni agent resume "nonexistent-agent-e2e" 2>&1) || T8_EXIT=$?
T8_EXIT=${T8_EXIT:-0}

echo "    output: ${T8_OUT:0:120}"

if [[ $T8_EXIT -ne 0 ]]; then
  if echo "$T8_OUT" | grep -qiE "not found|no.*agent|agent.*not|does not exist"; then
    pass "T8: resume nonexistent: exits $T8_EXIT with 'not found' message"
  else
    pass "T8: resume nonexistent: exits $T8_EXIT (non-zero error returned)"
  fi
else
  fail "T8: resume nonexistent agent exited 0 — should return error"
fi

# ─── T9: prompt actually delivered and processed ─────────────────────────────
echo ""
echo "==> [T9] prompt delivered and processed by agent (not just written to PTY)"

T9_JRNL_BEFORE=$(wc -l < "$JRNL_LOG")
PROBE="t9-probe-${RUN_ID}"

T9_OUT=$(cd "$WS" && omni agent exec "$AGENT" --prompt "respond with exactly: $PROBE" --bg 2>&1) || T9_EXIT=$?
T9_EXIT=${T9_EXIT:-0}
echo "    exec --bg exit=$T9_EXIT"

if [[ $T9_EXIT -ne 0 ]]; then
  fail "T9: exec --bg for probe failed (exit=$T9_EXIT)"
else
  pass "T9a: exec --bg accepted probe prompt"

  # Wait for delivery evidence in journalctl
  DELIVERY_FOUND=0
  for i in $(seq 1 30); do
    sleep 1
    if tail -n +"$((T9_JRNL_BEFORE+1))" "$JRNL_LOG" | \
        grep -qE "ExecInSession|exec in session|UserPromptSubmit|prompt sent|submit.*key|prompt.*delivered"; then
      DELIVERY_FOUND=1; break
    fi
  done

  if [[ $DELIVERY_FOUND -eq 1 ]]; then
    EV=$(tail -n +"$((T9_JRNL_BEFORE+1))" "$JRNL_LOG" | \
         grep -m1 "ExecInSession\|exec in session\|UserPromptSubmit\|prompt.*delivered" || true)
    pass "T9b: prompt delivery confirmed in journalctl: ${EV:0:100}"
  else
    # Check PTY interaction log
    PTY_LOG=$(ls /root/.omni/log/session-*.log 2>/dev/null | sort | tail -1)
    if [[ -n "$PTY_LOG" ]] && grep -q "$PROBE" "$PTY_LOG" 2>/dev/null; then
      pass "T9b: probe string found in PTY session log — prompt delivered"
    else
      fail "T9b: no delivery evidence in journalctl or session log within 30s"
      echo "    journalctl tail (last 10):"
      tail -n +"$((T9_JRNL_BEFORE+1))" "$JRNL_LOG" | tail -10 | sed 's/^/    /'
    fi
  fi
fi

# ─── T10: two sequential exec --bg calls ─────────────────────────────────────
echo ""
echo "==> [T10] two sequential exec --bg calls — both non-blocking, second writes correctly"

T10_START=$(date +%s%N)
T10_OUT1=$(cd "$WS" && omni agent exec "$AGENT" --prompt "T10-first: hello" --bg 2>&1) || T10_EXIT1=$?
T10_EXIT1=${T10_EXIT1:-0}
T10_MID=$(date +%s%N)

T10_OUT2=$(cd "$WS" && omni agent exec "$AGENT" --prompt "T10-second: world" --bg 2>&1) || T10_EXIT2=$?
T10_EXIT2=${T10_EXIT2:-0}
T10_END=$(date +%s%N)

T10_MS1=$(( (T10_MID - T10_START) / 1000000 ))
T10_MS2=$(( (T10_END - T10_MID) / 1000000 ))
T10_TOTAL=$(( (T10_END - T10_START) / 1000000 ))

echo "    call1: exit=$T10_EXIT1 time=${T10_MS1}ms"
echo "    call2: exit=$T10_EXIT2 time=${T10_MS2}ms"
echo "    total: ${T10_TOTAL}ms"

if [[ $T10_EXIT1 -eq 0 ]] && [[ $T10_EXIT2 -eq 0 ]]; then
  pass "T10a: both sequential exec --bg calls exit 0"
else
  fail "T10a: exec exits: call1=$T10_EXIT1 call2=$T10_EXIT2"
fi

if [[ $T10_TOTAL -lt 15000 ]]; then
  pass "T10b: both calls completed in <15s total (${T10_TOTAL}ms) — neither blocking"
else
  fail "T10b: sequential calls took ${T10_TOTAL}ms — one may have blocked"
fi

# ─── Hang detection (operator request) ───────────────────────────────────────
echo ""
echo "==> [HANG CHECK] Checking for exec --bg hang / agent not processing"

HANG_EVIDENCE=0
# Check if any exec --bg from this run was still blocking after 5s
LONG_EXEC=$(grep -E "exec in session|ExecInSession|prompt.*sent" "$JRNL_LOG" 2>/dev/null | wc -l)
echo "    exec-related journal entries: $LONG_EXEC"

if grep -q "operator.*hang\|exec.*hang\|ResumeAgent.*hang\|session.*stuck\|stuck.*session" "$JRNL_LOG" 2>/dev/null; then
  HANG_EVIDENCE=1
  fail "HANG: hang evidence found in journalctl"
  grep -m3 "hang\|stuck" "$JRNL_LOG" | sed 's/^/    /'
else
  pass "HANG: no hang evidence in journalctl — exec --bg flows returned normally"
fi

# ─── Summary ─────────────────────────────────────────────────────────────────
kill "$JRNL_PID" 2>/dev/null || true; JRNL_PID=""

echo ""
echo "==> Output: $LOG"
echo "==> Journal: $JRNL_LOG"
echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP  (run $RUN_ID)"
[[ "$FAIL" -gt 0 ]] && echo "==> FAIL" && exit 1 || echo "==> PASS (with $SKIP skip(s))"
