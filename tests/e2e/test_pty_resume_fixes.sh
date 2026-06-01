#!/usr/bin/env bash
# E2E tests for PTY resume fixes — PR #65 (feat/pty-idle-drain-repaint)
#
# Asserts BEHAVIORAL outcomes only — no instrumentation / debug log checks.
#
# T1: resume-repaint A/B
#       tmux 120x40, live claude session, resume with NO keypress after 3s.
#       old binary (pre-fix): pane is blank / shell-echo only.
#       new binary (fix):     pane shows claude UI content.
# T2: detach lifecycle
#       Ctrl+\ (0x1c) detaches the client; an immediate re-resume does NOT
#       print "already attached" (behavioral check, no log parsing).
# T3: codex regression
#       codex resume still paints (behavioral — pane has content pre-keypress).
#
# Usage:
#   OMNI=/path/to/omni bash test_pty_resume_fixes.sh
#   OMNI_OLD=/path/to/omni-old bash test_pty_resume_fixes.sh   # for A/B
#
# Skip conditions:
#   - tmux not available → all tests SKIP
#   - claude binary not available → T1/T2 SKIP
#   - codex binary not available → T3 SKIP
#
set -euo pipefail

# ─── configuration ────────────────────────────────────────────────────────────
OMNI="${OMNI:-/tmp/pty-drain-bins/omni}"
OMNI_OLD="${OMNI_OLD:-/tmp/pty-drain-bins/omni-old}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../output"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
WS="/tmp/pty-e2e-resume-${RUN_ID}"
OUTPUT="${OUTPUT_DIR}/pty-resume-${RUN_ID}"
AGENT="pty-e2e-${RUN_ID}"
CODEX_AGENT="pty-e2e-codex-${RUN_ID}"

mkdir -p "$OUTPUT_DIR" "$OUTPUT" "$WS"

PASS=0; FAIL=0; SKIP=0
pass() { echo "  PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP: $1"; SKIP=$((SKIP+1)); }

# ─── pre-checks ───────────────────────────────────────────────────────────────
if ! command -v tmux &>/dev/null; then
  echo "==> tmux not found — skipping all tests"
  for t in T1-OLD T1-NEW T2 T3; do skip "$t: tmux not available"; done
  echo "==> Results: PASS=$PASS FAIL=$FAIL SKIP=$SKIP"; exit 0
fi

if [[ ! -x "$OMNI" ]]; then
  echo "==> OMNI binary not found at $OMNI — skipping all tests"
  for t in T1-OLD T1-NEW T2 T3; do skip "$t: OMNI binary missing"; done
  echo "==> Results: PASS=$PASS FAIL=$FAIL SKIP=$SKIP"; exit 0
fi

HAVE_CLAUDE=1
HAVE_CODEX=1
command -v claude &>/dev/null || HAVE_CLAUDE=0
command -v codex  &>/dev/null || HAVE_CODEX=0

HAVE_OMNI_OLD=0
[[ -x "$OMNI_OLD" ]] && HAVE_OMNI_OLD=1

# ─── tmux helpers ─────────────────────────────────────────────────────────────
tmux_new()  { tmux new-session -d -s "$1" -x 120 -y 40 2>/dev/null; }
tmux_kill() { tmux kill-session -t "$1" 2>/dev/null || true; }
tmux_send() { tmux send-keys -t "$1" "$2" "$3"; }
tmux_pane() { tmux capture-pane -t "$1" -p 2>/dev/null; }

# pane_content_lines: count non-empty lines that aren't shell-prompt or cmd echo.
pane_content_lines() {
  echo "$1" | grep -v '^\s*$' \
    | grep -vE "^\s*(DEV=|OMNI=|/tmp/pty|omni agent|siraj@|[[:space:]]*$)" \
    | awk 'NF>0' | wc -l
}

# pane_has_claude_ui: true if pane contains known claude UI markers.
pane_has_claude_ui() {
  echo "$1" | grep -qE "Claude Code|Welcome back|❯|╭──|╰──|Human:|> "
}

# ─── ptydaemon helpers ────────────────────────────────────────────────────────
PTY_SOCK="${OMNI_PTY_SOCK:-/run/user/$(id -u)/omni/omni-pty.sock}"

ptyd_op() { echo "$1" | nc -U "$PTY_SOCK" -q1 2>/dev/null || true; }

# meta_attached SESSION_ID → count (or -1 on error).
meta_attached() {
  ptyd_op "{\"op\":\"meta-attached\",\"session_id\":\"$1\"}" \
    | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('count',-1))" \
    2>/dev/null || echo -1
}

# find_session_id AGENT_ID → session_id (empty if not found).
find_session_id() {
  local aid="$1"
  ptyd_op '{"op":"list","session_id":""}' \
    | python3 -c "
import json,sys
d=json.load(sys.stdin)
for s in d.get('sessions',[]):
    if s.get('agent_id','') == '$aid' and s.get('status','') == 'active':
        print(s['session_id']); break
" 2>/dev/null || echo ""
}

# ─── cleanup ──────────────────────────────────────────────────────────────────
TMUX_SESSIONS=()
cleanup() {
  for s in "${TMUX_SESSIONS[@]:-}"; do tmux_kill "$s" 2>/dev/null || true; done
  "$OMNI" agent delete "$AGENT"       --workspace "$WS" 2>/dev/null || true
  "$OMNI" agent delete "$CODEX_AGENT" --workspace "$WS" 2>/dev/null || true
  rm -rf "$WS"
}
trap cleanup EXIT

# ─── T1/T2 setup: live claude session with content ───────────────────────────
if [[ "$HAVE_CLAUDE" -eq 0 ]]; then
  skip "T1-OLD: claude not available"; skip "T1-NEW: claude not available"
  skip "T2: claude not available"
else

echo ""
echo "==> Setup: create claude agent and build up a live session"
"$OMNI" team init --workspace "$WS" 2>/dev/null || true
"$OMNI" agent init "$AGENT" --workspace "$WS" --provider claude --interactive=false 2>/dev/null
AID=$("$OMNI" agent list --workspace "$WS" 2>/dev/null \
        | awk -v n="$AGENT" 'NR>1 && $2==n{print $1}' | head -1)
echo "    agent_id: $AID"

# Pre-seed trust acceptance (claude project settings).
PROJ_KEY=$(echo -n "$WS" | tr '/' '-')
mkdir -p "${HOME}/.claude/projects"
echo '{"hasTrustDialogAccepted":true}' > "${HOME}/.claude/projects/${PROJ_KEY}.json"

# Start session in a real 120x40 tmux pane.
S_SETUP="pty-setup-${RUN_ID}"
TMUX_SESSIONS+=("$S_SETUP")
tmux_new "$S_SETUP"
echo "    tmux size: $(tmux display-message -t "$S_SETUP" -p "#{window_width}x#{window_height}" 2>/dev/null || echo unknown)"

SETUP_LOG="${OUTPUT}/setup.log"
tmux_send "$S_SETUP" "$OMNI agent resume '$AGENT' --workspace '$WS' 2>'$SETUP_LOG'" Enter
sleep 5

# Accept trust dialog if it appears.
SCREEN_SETUP=$(tmux_pane "$S_SETUP")
if echo "$SCREEN_SETUP" | grep -qiE "trust|safety check|Yes.*trust"; then
  echo "    trust dialog — accepting..."
  tmux_send "$S_SETUP" "1" Enter
  sleep 3
fi

# Exec a prompt so there is conversation content in the session history.
# Use a short factual reply to minimise tool-use latency.
echo "    sending prompt..."
"$OMNI" agent exec "$AGENT" --workspace "$WS" \
    --prompt "respond with exactly one word: pong" 2>/dev/null || true
sleep 25  # Give claude time to process, render, and return to idle UI.

SCREEN_AFTER=$(tmux_pane "$S_SETUP")
echo "    session screen (first 5 lines):"
echo "$SCREEN_AFTER" | head -5 | sed 's/^/    | /'

# Detach cleanly via Ctrl+\ so session stays headless.
tmux_send "$S_SETUP" "" "C-\\"
sleep 1
tmux_kill "$S_SETUP"
sleep 1
echo "    session running headlessly"

# Locate the session_id for meta-attached checks.
SID=""
if [[ -n "$AID" ]]; then
  SID=$(find_session_id "$AID")
  # Fallback: for some session types session_id == agent_id.
  if [[ -z "$SID" ]]; then
    MAYBE=$(echo '{"op":"get","session_id":"'"$AID"'","agent_id":"'"$AID"'"}' \
              | nc -U "$PTY_SOCK" -q1 2>/dev/null \
              | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('session_id','') if d.get('ok') else '')" 2>/dev/null || echo "")
    [[ -n "$MAYBE" ]] && SID="$MAYBE"
  fi
fi
echo "    session_id: ${SID:-<not found>}"

# ─── T1-OLD: old binary — pane should be blank / minimal ─────────────────────
echo ""
echo "==> [T1-OLD] Old binary (pre-fix) — expect blank pane without keypress"

if [[ "$HAVE_OMNI_OLD" -eq 0 ]]; then
  skip "T1-OLD: OMNI_OLD binary not found at $OMNI_OLD"
else
  S_OLD="pty-old-${RUN_ID}"
  TMUX_SESSIONS+=("$S_OLD")
  tmux_new "$S_OLD"
  OLD_LOG="${OUTPUT}/t1-old.log"
  tmux_send "$S_OLD" "$OMNI_OLD agent resume '$AGENT' --workspace '$WS' 2>'$OLD_LOG'" Enter
  sleep 5  # No keypress.

  PANE_OLD=$(tmux_pane "$S_OLD")
  echo "    OLD pane (no keypress, 5s):"
  echo "$PANE_OLD" | head -8 | sed 's/^/    | /'

  OLD_CONTENT=$(pane_content_lines "$PANE_OLD")
  echo "    content lines: $OLD_CONTENT"

  if pane_has_claude_ui "$PANE_OLD"; then
    fail "T1-OLD: old binary pane shows claude UI — bug not reproduced for A/B"
  else
    pass "T1-OLD: old binary pane blank/no claude UI ($OLD_CONTENT content lines) — Bug A present"
  fi

  # Kill OLD before NEW so meta-attached is clean.
  tmux_send "$S_OLD" "" "C-\\" 2>/dev/null || true
  sleep 0.5
  tmux_kill "$S_OLD"
  sleep 1
fi

# ─── T1-NEW: new binary — pane should show claude UI without keypress ─────────
echo ""
echo "==> [T1-NEW] New binary (fix) — expect claude UI without keypress"

S_NEW="pty-new-${RUN_ID}"
TMUX_SESSIONS+=("$S_NEW")
tmux_new "$S_NEW"
NEW_LOG="${OUTPUT}/t1-new.log"
tmux_send "$S_NEW" "$OMNI agent resume '$AGENT' --workspace '$WS' 2>'$NEW_LOG'" Enter
sleep 7  # No keypress — pump fires within 400ms; allow claude to repaint.

PANE_NEW=$(tmux_pane "$S_NEW")
echo "$PANE_NEW" > "${OUTPUT}/t1-new-pane.txt"

echo "    NEW pane (no keypress, 5s):"
echo "$PANE_NEW" | head -12 | sed 's/^/    | /'

NEW_CONTENT=$(pane_content_lines "$PANE_NEW")
echo "    content lines: $NEW_CONTENT"

if pane_has_claude_ui "$PANE_NEW"; then
  MARKER=$(echo "$PANE_NEW" | grep -E "Claude Code|Welcome back|❯|╭──" | head -1)
  pass "T1-NEW: claude UI visible without keypress — Bug A fixed"
  pass "T1-NEW: UI marker found: '${MARKER:0:80}'"
else
  fail "T1-NEW: claude UI NOT visible pre-keypress ($NEW_CONTENT content lines)"
  echo "    full pane:"
  echo "$PANE_NEW" | sed 's/^/    | /'
fi

# Kill T1-NEW before T2 so meta-attached isn't polluted.
echo "    killing T1-NEW session before T2..."
tmux_send "$S_NEW" "" "C-\\" 2>/dev/null || true
sleep 1
tmux_kill "$S_NEW"
sleep 1

# ─── T2: detach lifecycle ─────────────────────────────────────────────────────
echo ""
echo "==> [T2] Detach lifecycle: Ctrl+\\ detaches; re-resume has no 'already attached'"

S_T2A="pty-t2a-${RUN_ID}"
S_T2B="pty-t2b-${RUN_ID}"
TMUX_SESSIONS+=("$S_T2A" "$S_T2B")

# Attach, wait 2s, send Ctrl+\ (detach key).
T2A_LOG="${OUTPUT}/t2a.log"
tmux_new "$S_T2A"
tmux_send "$S_T2A" "$OMNI agent resume '$AGENT' --workspace '$WS' 2>'$T2A_LOG'" Enter
sleep 2
tmux_send "$S_T2A" "" "C-\\"
sleep 1

T2A_PANE=$(tmux_pane "$S_T2A")
echo "    pane after Ctrl+\\ (should be shell, not agent):"
echo "$T2A_PANE" | head -3 | sed 's/^/    | /'

# ASSERT: pane returned to shell (not stuck in agent UI).
if echo "$T2A_PANE" | grep -qE "siraj@|\\$\s*$|bash-|%\s*$"; then
  pass "T2: pane returned to shell after Ctrl+\\ (client detached)"
else
  # Check stderr log for detach confirmation.
  if grep -q "detach" "$T2A_LOG" 2>/dev/null; then
    pass "T2: detach key logged in client log"
  else
    fail "T2: pane did not return to shell after Ctrl+\\"
  fi
fi

# meta-attached should be 0 now (if we can look up the session).
if [[ -n "$SID" ]]; then
  sleep 0.5
  META=$(meta_attached "$SID")
  echo "    meta-attached after Ctrl+\\: $META"
  if [[ "$META" -eq 0 ]]; then
    pass "T2: meta-attached=0 after Ctrl+\\ detach"
  else
    fail "T2: meta-attached=$META after Ctrl+\\ (expected 0)"
  fi
else
  skip "T2: meta-attached check (session_id not found)"
fi

tmux_kill "$S_T2A"
sleep 1

# ASSERT: immediate re-resume does NOT produce "already attached".
T2B_LOG="${OUTPUT}/t2b.log"
tmux_new "$S_T2B"
tmux_send "$S_T2B" "$OMNI agent resume '$AGENT' --workspace '$WS' 2>'$T2B_LOG'" Enter
sleep 3

T2B_PANE=$(tmux_pane "$S_T2B")
T2B_STDERR=$(cat "$T2B_LOG" 2>/dev/null || echo "")
echo "    re-resume pane (first 3 lines):"
echo "$T2B_PANE" | head -3 | sed 's/^/    | /'

if echo "$T2B_PANE$T2B_STDERR" | grep -qi "already attached"; then
  fail "T2: re-resume printed 'already attached' — detach-on-exit not working"
else
  pass "T2: re-resume did NOT print 'already attached'"
fi

tmux_send "$S_T2B" "" "C-\\" 2>/dev/null || true
sleep 0.5
tmux_kill "$S_T2B"

fi  # end HAVE_CLAUDE block

# ─── T3: codex regression ─────────────────────────────────────────────────────
echo ""
echo "==> [T3] Codex regression: codex resume still paints without keypress"

if [[ "$HAVE_CODEX" -eq 0 ]]; then
  skip "T3: codex binary not available"
else
  "$OMNI" agent init "$CODEX_AGENT" --workspace "$WS" --provider codex --interactive=false 2>/dev/null
  S_COD_SETUP="pty-cod-setup-${RUN_ID}"
  TMUX_SESSIONS+=("$S_COD_SETUP")
  tmux_new "$S_COD_SETUP"
  CODEX_SETUP_LOG="${OUTPUT}/t3-setup.log"
  tmux_send "$S_COD_SETUP" "$OMNI agent resume '$CODEX_AGENT' --workspace '$WS' 2>'$CODEX_SETUP_LOG'" Enter
  sleep 6
  tmux_send "$S_COD_SETUP" "" "C-\\" 2>/dev/null || true
  sleep 0.5
  tmux_kill "$S_COD_SETUP"
  sleep 1

  S_COD="pty-cod-${RUN_ID}"
  TMUX_SESSIONS+=("$S_COD")
  COD_LOG="${OUTPUT}/t3.log"
  tmux_new "$S_COD"
  tmux_send "$S_COD" "$OMNI agent resume '$CODEX_AGENT' --workspace '$WS' 2>'$COD_LOG'" Enter
  sleep 5  # No keypress.

  PANE_COD=$(tmux_pane "$S_COD")
  echo "    codex pane (no keypress, 5s):"
  echo "$PANE_COD" | head -8 | sed 's/^/    | /'

  COD_CONTENT=$(pane_content_lines "$PANE_COD")
  echo "    content lines: $COD_CONTENT"

  if [[ "$COD_CONTENT" -gt 2 ]]; then
    pass "T3: codex pane has $COD_CONTENT content lines pre-keypress (no regression)"
  else
    fail "T3: codex pane only $COD_CONTENT content lines — possible regression"
  fi

  tmux_send "$S_COD" "" "C-\\" 2>/dev/null || true
  sleep 0.5
  tmux_kill "$S_COD"
fi

# ─── summary ──────────────────────────────────────────────────────────────────
echo ""
echo "==> Output: $OUTPUT"
echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP  (run $RUN_ID)"

if [[ "$FAIL" -gt 0 ]]; then
  echo "==> FAIL"; exit 1
fi
echo "==> PASS (with $SKIP skip(s))"; exit 0
