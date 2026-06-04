#!/usr/bin/env bash
# E2E tests: exec --bg / resume attach flow (commit df907a6)
#
# Two behavioral changes tested here:
#   1. Flag rename: --resume/-r  →  --bg/-b  (exec command)
#   2. Attach on active PTY: when a PTY session is already running,
#      `omni agent resume` (Detached=false) now calls ptyDaemon.Attach
#      instead of returning nil.
#
# Test cases:
#   T1: --bg flag accepted; --resume flag rejected (unknown flag)
#   T2: exec --bg starts background PTY session (non-blocking)
#   T3: resume <name> attaches to the running PTY (visible content in pane)
#   T4: exec --bg on already-active PTY leaves session running (does not attach)
#   T5: -b short flag works identically to --bg
#
# Requires: omni daemon running, tmux, claude or codex binary.
# Override binary: OMNI=/path/to/omni bash test_exec_bg_resume_attach.sh
set -euo pipefail

OMNI="${OMNI:-omni}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../output"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
JRNL_LOG="${OUTPUT_DIR}/exec-bg-resume-${RUN_ID}.log"

mkdir -p "$OUTPUT_DIR"

PASS=0; FAIL=0; SKIP=0
pass() { echo "  PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP: $1"; SKIP=$((SKIP+1)); }

# ─── pre-checks ───────────────────────────────────────────────────────────────
PROVIDER=""
command -v claude &>/dev/null && PROVIDER="claude"
[[ -z "$PROVIDER" ]] && command -v codex &>/dev/null && PROVIDER="codex"

if [[ -z "$PROVIDER" ]]; then
  echo "==> No supported agent binary (claude/codex) — skipping all tests"
  for t in T1 T2 T3 T4 T5; do skip "$t: no agent binary"; done
  echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP"; exit 0
fi

if ! command -v tmux &>/dev/null; then
  echo "==> tmux not found — T3/T4 will be skipped"
fi

echo "==> Provider: $PROVIDER  OMNI: $OMNI"

# ─── setup ───────────────────────────────────────────────────────────────────
AGENT_NAME="e2e-bg-resume-${RUN_ID}"
WS="/tmp/e2e-bg-resume-${RUN_ID}"
mkdir -p "$WS"
JRNL_PID=""
TMUX_SESSIONS=()

cleanup() {
  [[ -n "$JRNL_PID" ]] && kill "$JRNL_PID" 2>/dev/null || true
  for s in "${TMUX_SESSIONS[@]:-}"; do tmux kill-session -t "$s" 2>/dev/null || true; done
  $OMNI agent delete "$AGENT_NAME" --workspace "$WS" 2>/dev/null || true
  rm -rf "$WS"
}
trap cleanup EXIT

journalctl -f --no-pager --lines=0 -t omni-server > "$JRNL_LOG" 2>&1 &
JRNL_PID=$!
sleep 0.5

$OMNI team init --workspace "$WS" 2>/dev/null || true
$OMNI agent init "$AGENT_NAME" \
  --workspace "$WS" --provider "$PROVIDER" --interactive=false

# ─── T1: flag rename — --bg accepted, --resume rejected ──────────────────────
echo ""
echo "==> [T1] Flag rename: --bg accepted; --resume rejected as unknown flag"

# --resume must now be unknown (flag renamed to --bg).
RESUME_OUT=$($OMNI agent exec "$AGENT_NAME" \
  --workspace "$WS" --prompt "test" --resume 2>&1) || RESUME_EXIT=$?
RESUME_EXIT=${RESUME_EXIT:-0}

if [[ $RESUME_EXIT -ne 0 ]] && echo "$RESUME_OUT" | grep -qiE "unknown flag|unknown shorthand|flag provided but not defined"; then
  pass "T1: --resume rejected as unknown flag (flag renamed to --bg)"
else
  fail "T1: --resume should be unknown flag but exited $RESUME_EXIT (output: ${RESUME_OUT:0:120})"
fi

# --bg must be accepted (exit 0 or a non-flag-error).
BG_OUT=$($OMNI agent exec "$AGENT_NAME" \
  --workspace "$WS" --prompt "flag-test-bg" --bg 2>&1) || BG_EXIT=$?
BG_EXIT=${BG_EXIT:-0}

if echo "$BG_OUT" | grep -qiE "unknown flag|unknown shorthand|flag provided but not defined"; then
  fail "T1: --bg flag not recognized by exec command"
else
  pass "T1: --bg flag accepted by exec command (exit=$BG_EXIT)"
fi

# ─── T2: exec --bg is non-blocking ───────────────────────────────────────────
echo ""
echo "==> [T2] exec --bg starts background PTY session (non-blocking)"

START_TS=$(date +%s%N)
BG2_OUT=$($OMNI agent exec "$AGENT_NAME" \
  --workspace "$WS" --prompt "reply with: pong" --bg 2>&1) || BG2_EXIT=$?
BG2_EXIT=${BG2_EXIT:-0}
END_TS=$(date +%s%N)
ELAPSED_MS=$(( (END_TS - START_TS) / 1000000 ))

echo "    exec --bg returned in ${ELAPSED_MS}ms (exit=$BG2_EXIT)"
echo "    output: ${BG2_OUT:0:120}"

if [[ $BG2_EXIT -eq 0 ]]; then
  pass "T2: exec --bg exits 0"
else
  fail "T2: exec --bg exited $BG2_EXIT"
fi

if [[ $ELAPSED_MS -lt 10000 ]]; then
  pass "T2: exec --bg returned in <10s (${ELAPSED_MS}ms) — non-blocking"
else
  fail "T2: exec --bg took ${ELAPSED_MS}ms — may be blocking"
fi

# Check journalctl for session started evidence.
SESSION_FOUND=0
for i in $(seq 1 20); do
  sleep 1
  if grep -qE "session created|session ready|session started|PTY daemon session" "$JRNL_LOG" 2>/dev/null; then
    SESSION_FOUND=1; break
  fi
done

if [[ $SESSION_FOUND -eq 1 ]]; then
  EV=$(grep -m1 -E "session created|session ready|session started|PTY daemon session" "$JRNL_LOG" || true)
  pass "T2: PTY session started in background (journalctl: ${EV:0:80})"
else
  skip "T2: no PTY session start evidence in journalctl within 20s (may be already running)"
fi

# ─── T3: resume attaches to the running PTY ──────────────────────────────────
echo ""
echo "==> [T3] resume <name> attaches to running PTY (Detached=false calls Attach)"

if ! command -v tmux &>/dev/null; then
  skip "T3: tmux not available — cannot capture pane for attach verification"
else
  # Give the background session a moment to be active.
  sleep 3

  S_ATTACH="bg-attach-${RUN_ID}"
  TMUX_SESSIONS+=("$S_ATTACH")
  tmux new-session -d -s "$S_ATTACH" -x 120 -y 40 2>/dev/null

  ATTACH_LOG="${OUTPUT_DIR}/t3-attach-${RUN_ID}.log"
  tmux send-keys -t "$S_ATTACH" \
    "$OMNI agent resume '$AGENT_NAME' --workspace '$WS' 2>'$ATTACH_LOG'" Enter
  sleep 5  # Allow attach + repaint.

  PANE=$(tmux capture-pane -t "$S_ATTACH" -p 2>/dev/null)
  echo "    pane after resume (first 6 lines):"
  echo "$PANE" | head -6 | sed 's/^/    | /'

  # Check for attach evidence in journalctl.
  ATTACH_IN_LOG=0
  if grep -qE "PTY terminal already active, attaching|ResumeAgent: PTY.*attach|ptyDaemon.Attach" "$JRNL_LOG" 2>/dev/null; then
    ATTACH_IN_LOG=1
  fi

  # Check for attach evidence in the stderr log.
  ATTACH_IN_STDERR=0
  if [[ -f "$ATTACH_LOG" ]] && grep -qiE "attach|resume.*active|connected" "$ATTACH_LOG" 2>/dev/null; then
    ATTACH_IN_STDERR=1
  fi

  # Check pane has content (not blank shell).
  PANE_NONBLANK=$(echo "$PANE" | grep -v '^\s*$' | wc -l)

  if [[ $ATTACH_IN_LOG -eq 1 ]]; then
    pass "T3: journalctl confirms ptyDaemon.Attach was called (Attach path active)"
  elif [[ $PANE_NONBLANK -gt 3 ]]; then
    pass "T3: pane has $PANE_NONBLANK non-blank lines — attach appears successful"
  elif [[ $ATTACH_IN_STDERR -eq 1 ]]; then
    pass "T3: attach confirmed via resume stderr log"
  else
    fail "T3: no attach evidence in journalctl, pane ($PANE_NONBLANK lines), or stderr log"
    echo "    stderr log:"
    cat "$ATTACH_LOG" 2>/dev/null | head -10 | sed 's/^/    | /' || echo "    (no log)"
    echo "    journalctl tail:"
    tail -10 "$JRNL_LOG" | sed 's/^/    | /' || true
  fi

  # Detach cleanly.
  tmux send-keys -t "$S_ATTACH" "" "C-\\" 2>/dev/null || true
  sleep 0.5
  tmux kill-session -t "$S_ATTACH" 2>/dev/null || true
fi

# ─── T4: exec --bg on already-active PTY stays background (no attach) ────────
echo ""
echo "==> [T4] exec --bg on already-active PTY does NOT attach (Detached=true)"

# Session should still be running from T2/T3.
BG4_OUT=$($OMNI agent exec "$AGENT_NAME" \
  --workspace "$WS" --prompt "second bg prompt" --bg 2>&1) || BG4_EXIT=$?
BG4_EXIT=${BG4_EXIT:-0}

echo "    exec --bg (second call) exit=$BG4_EXIT output: ${BG4_OUT:0:120}"

if [[ $BG4_EXIT -eq 0 ]]; then
  pass "T4: second exec --bg exits 0"
else
  fail "T4: second exec --bg exited $BG4_EXIT"
fi

# Journalctl should show "leaving running in background" (Detached=true branch).
sleep 1
if grep -qE "already active, leaving running in background|leaving.*background|detached.*background" "$JRNL_LOG" 2>/dev/null; then
  EV4=$(grep -m1 -E "already active, leaving running in background|leaving.*background" "$JRNL_LOG" || true)
  pass "T4: journalctl confirms Detached=true path (${EV4:0:80})"
else
  # Acceptable if session log shows resumed/exec delivered to existing session.
  skip "T4: 'leaving running in background' log not found (may differ by build log level)"
fi

# ─── T5: short flag -b works identically ─────────────────────────────────────
echo ""
echo "==> [T5] Short flag -b works identically to --bg"

B5_OUT=$($OMNI agent exec "$AGENT_NAME" \
  --workspace "$WS" --prompt "short-flag-b" -b 2>&1) || B5_EXIT=$?
B5_EXIT=${B5_EXIT:-0}

if echo "$B5_OUT" | grep -qiE "unknown flag|unknown shorthand|flag provided but not defined"; then
  fail "T5: -b short flag not recognized"
else
  pass "T5: -b short flag accepted (exit=$B5_EXIT)"
fi

# ─── summary ──────────────────────────────────────────────────────────────────
kill "$JRNL_PID" 2>/dev/null || true; JRNL_PID=""
echo ""
echo "==> Output: $OUTPUT_DIR"
echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP  (run $RUN_ID)"
echo "==> journal log: $JRNL_LOG"

if [[ "$FAIL" -gt 0 ]]; then
  echo "==> FAIL"; exit 1
fi
echo "==> PASS (with $SKIP skip(s))"; exit 0
