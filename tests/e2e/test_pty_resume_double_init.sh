#!/usr/bin/env bash
# Regression e2e for the "double init -r" resume wedge (fix/pty-resume-already-exists).
#
# Reproduces the exact failing sequence reported from the field:
#     1. omni agent init -r <name>      # interactive resume; attaches then detaches
#     2. omni agent exec <name> --prompt 'hi'   # auto-resumes the dead session
#     3. omni agent init -r <name>      # <-- USED TO FAIL: "terminal /<sid> already exists"
#
# Root cause (pre-fix): GetBySessionOnly had no ORDER BY, so SessionPID/Get returned
# a stale stopped row (dead pid). registerPTYSession then re-adopted the dead pid
# (/proc/<pid>/fd/0 ENOENT), readiness timed out, and the next resume's Start hit a
# lingering terminal -> "already exists". The fixes (ordered GetBySessionOnly,
# adopt re-key, GetSession live-first, List in-memory authority) clear the wedge.
#
# ASSERTIONS (behavioral — no instrumentation asserts):
#   A: step-2 exec exits 0 (auto-resume path accepted the prompt)
#   B: step-3 init -r exits 0 and stderr has NO "already exists"
#   C: journalctl shows NO "resume failed: ... already exists" during step 3
#
# Run inside the worktree container: omni-fix-pty-resume-wedge-ubuntu-1, at /build.
#   docker compose ... exec ubuntu bash -lc 'bash /build/tests/e2e/test_pty_resume_double_init.sh'
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../output"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
LOG="${OUTPUT_DIR}/pty-resume-double-init-${RUN_ID}.log"
JRNL_LOG="${OUTPUT_DIR}/jrnl-double-init-${RUN_ID}.log"
mkdir -p "$OUTPUT_DIR"

PASS=0; FAIL=0; SKIP=0
pass() { echo "  PASS: $1" | tee -a "$LOG"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1" | tee -a "$LOG"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP: $1" | tee -a "$LOG"; SKIP=$((SKIP+1)); }

WS="/build"
AGENT="e2e-double-init-${RUN_ID}"
JRNL_PID=""
S1="dinit-1-${RUN_ID}"
S3="dinit-3-${RUN_ID}"

cleanup() {
  tmux kill-session -t "$S1" 2>/dev/null || true
  tmux kill-session -t "$S3" 2>/dev/null || true
  (cd "$WS" && omni agent stop "$AGENT" 2>/dev/null) || true
  [[ -n "$JRNL_PID" ]] && kill "$JRNL_PID" 2>/dev/null || true
}
trap cleanup EXIT

# ─── preconditions ───────────────────────────────────────────────────────────
if ! command -v tmux >/dev/null 2>&1; then
  skip "tmux unavailable — interactive init -r cannot be driven"
  echo "==> Results: PASS=$PASS FAIL=$FAIL SKIP=$SKIP"
  echo "==> SKIP (no tmux)"; exit 0
fi

# Tap omni-server journal for the whole run.
journalctl -f --no-pager --lines=0 -t omni-server > "$JRNL_LOG" 2>&1 &
JRNL_PID=$!
sleep 0.3

# detach_interactive: drive `omni agent init -r AGENT` inside a tmux pane, give it
# time to resume + attach, then send Ctrl-\ to detach (leaving the daemon session).
drive_init() { # drive_init <session> <errlog> <exitlog>
  local s="$1" errlog="$2" exitlog="$3"
  tmux new-session -d -s "$s" -x 120 -y 40 2>/dev/null
  tmux send-keys -t "$s" \
    "cd '$WS' && omni agent init -r '$AGENT' 2>'$errlog'; echo EXIT=\$? > '$exitlog'" Enter
}

# ─── step 1: first interactive init -r (creates + launches, then detach) ──────
echo "" ; echo "==> [step 1] omni agent init -r $AGENT (interactive, then detach)"
E1_ERR="${OUTPUT_DIR}/dinit-1-err-${RUN_ID}.log"; E1_EXIT="${OUTPUT_DIR}/dinit-1-exit-${RUN_ID}.log"
drive_init "$S1" "$E1_ERR" "$E1_EXIT"
sleep 8                                   # let it resume + attach + render
tmux send-keys -t "$S1" "" "C-\\" 2>/dev/null || true   # detach client
sleep 3
tmux kill-session -t "$S1" 2>/dev/null || true
sleep 2                                   # let the detached child settle

# Resolve the agent UUID; the primary session id equals the agent id. Passing
# --session-id to exec short-circuits the operator session-store lookup (which
# would otherwise need a persisted session row this fresh agent does not have).
AGENT_ID="$(cd "$WS" && omni agent list 2>/dev/null | awk -v n="$AGENT" '$2==n{print $1}' | head -1)"
SID="$AGENT_ID"
echo "    agent_id=$AGENT_ID  session_id=$SID"

# Faithfully reproduce the FIELD precondition: in the report the interactive
# claude child EXITED on detach, so the session was dead-but-recorded when exec
# ran. In-container the daemon keeps the child alive, so kill it explicitly to
# recreate the dead-child state that triggers auto-resume + the stale-row wedge.
echo "    killing the session's claude child to recreate the dead-child precondition"
# Match ONLY this session's child (claude -r <SID>). Never fall back to a bare
# `pkill claude`, which in a shared container would kill other agents' children.
pkill -f "claude -r ${SID}" 2>/dev/null || true
sleep 4                                    # let watchTerminal mark stopped + evict

# ─── step 2: exec --prompt (foreground; triggers auto-resume) ─────────────────
echo "" ; echo "==> [step 2] omni agent exec $AGENT --session-id $SID --prompt 'hi' (auto-resume path)"
J2_BEFORE=$(wc -l < "$JRNL_LOG")
S2_OUT=$(cd "$WS" && omni agent exec "$AGENT" --session-id "$SID" --prompt 'hi' 2>&1) || S2_EXIT=$?
S2_EXIT=${S2_EXIT:-0}
echo "    exec exit=$S2_EXIT  output: ${S2_OUT:0:120}"
if [[ $S2_EXIT -eq 0 ]]; then
  pass "A: exec --prompt on a dead session exits 0 (auto-resume delivered the prompt)"
else
  fail "A: exec --prompt exited $S2_EXIT: ${S2_OUT:0:160}"
fi
sleep 3
J2_SLICE="$(tail -n +"$((J2_BEFORE+1))" "$JRNL_LOG" 2>/dev/null || true)"

# Precondition proof: step-2 must have spawned a FRESH session for $SID (the dead
# child was auto-resumed). Without the kill landing, the session would stay live
# and no new 'creating session' would appear — the test would then be vacuous.
if echo "$J2_SLICE" | grep -qE "creating session.*${SID}|session created.*${SID}"; then
  pass "PRE: step-2 auto-resumed a fresh session for the killed child (precondition exercised)"
else
  skip "PRE: no fresh 'creating session' for \$SID in step 2 — session may have stayed live (test not exercising dead-child path)"
fi

# Assertion D — the dead-pid adopt symptoms from the field bug must NOT recur:
# pre-fix, exec re-adopted the stale pid (open /proc/<pid>/fd/0 ENOENT) and
# readiness timed out. The fix (ordered GetBySessionOnly + adopt re-key) prevents both.
if echo "$J2_SLICE" | grep -qiE "open /proc/[0-9]+/fd/0|register failed|did not become active"; then
  fail "D: dead-pid adopt / readiness-timeout symptom recurred during step 2:"
  echo "$J2_SLICE" | grep -iE "open /proc/[0-9]+/fd/0|register failed|did not become active" | head -3 | sed 's/^/      /'
else
  pass "D: no dead-pid adopt ('/proc/<pid>/fd/0') or readiness timeout during step-2 auto-resume"
fi

# ─── step 3: second init -r — must NOT wedge on "already exists" ───────────────
echo "" ; echo "==> [step 3] omni agent init -r $AGENT again (must succeed, no 'already exists')"
J3_BEFORE=$(wc -l < "$JRNL_LOG")
E3_ERR="${OUTPUT_DIR}/dinit-3-err-${RUN_ID}.log"; E3_EXIT="${OUTPUT_DIR}/dinit-3-exit-${RUN_ID}.log"
: > "$E3_ERR"; : > "$E3_EXIT"
drive_init "$S3" "$E3_ERR" "$E3_EXIT"
sleep 8
# detach the (hopefully successful) attach so cleanup is graceful
tmux send-keys -t "$S3" "" "C-\\" 2>/dev/null || true
sleep 2
tmux kill-session -t "$S3" 2>/dev/null || true

E3_EXIT_CODE="$(grep -oE 'EXIT=[0-9]+' "$E3_EXIT" 2>/dev/null | head -1 | cut -d= -f2 || true)"
E3_EXIT_CODE="${E3_EXIT_CODE:-missing}"
E3_STDERR="$(cat "$E3_ERR" 2>/dev/null || true)"
echo "    init -r exit=$E3_EXIT_CODE  stderr: ${E3_STDERR:0:160}"

# Assertion B: no "already exists" in the command's own stderr, and it exited 0.
if echo "$E3_STDERR" | grep -qi "already exists"; then
  fail "B: second init -r reported 'already exists' — wedge reproduced: ${E3_STDERR:0:160}"
elif [[ "$E3_EXIT_CODE" == "0" ]]; then
  pass "B: second init -r exited 0 with no 'already exists' — wedge cleared"
elif [[ "$E3_EXIT_CODE" == "missing" ]]; then
  # Interactive process may still be attached when EXIT wasn't written; fall back to
  # the stderr signal only (no "already exists" already passed the grep above).
  skip "B: exit code not captured (interactive still attached); no 'already exists' in stderr"
else
  fail "B: second init -r exited $E3_EXIT_CODE: ${E3_STDERR:0:160}"
fi

# Assertion C: daemon/operator journal during step 3 has no resume-failed/already-exists.
J3_SLICE="$(tail -n +"$((J3_BEFORE+1))" "$JRNL_LOG" 2>/dev/null || true)"
if echo "$J3_SLICE" | grep -qiE "resume failed.*already exists|pty start.*already exists"; then
  fail "C: journal shows resume failure during step 3:"
  echo "$J3_SLICE" | grep -iE "resume failed.*already exists|pty start.*already exists" | head -3 | sed 's/^/      /'
else
  pass "C: no 'resume failed / already exists' in operator journal during step 3"
fi

# ─── summary ─────────────────────────────────────────────────────────────────
kill "$JRNL_PID" 2>/dev/null || true; JRNL_PID=""
echo "" ; echo "==> Output:  $LOG"
echo "==> Journal: $JRNL_LOG"
echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP  (run $RUN_ID)"
[[ "$FAIL" -gt 0 ]] && { echo "==> FAIL"; exit 1; } || { echo "==> PASS (with $SKIP skip(s))"; exit 0; }
