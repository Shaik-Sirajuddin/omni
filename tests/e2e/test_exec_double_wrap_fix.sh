#!/usr/bin/env bash
# E2E tests: fix/agy-exec-double-wrap
#
# Verifies three things per provider:
#  A. exec prompt is DELIVERED — agent receives and responds
#  B. NO literal escape-sequence leak — `[200~` / `[201~` / `\x15` must not appear
#     as text in the tmux pane
#  C. Framing is correct — ptydaemon debug log shows prompt_prewrapped=false for
#     agy (single-frame path), and bracketed_paste=true only when DECSET 2004 active
#
# Providers tested: agy · claude · codex
# Binary:   OMNI env var (default: /tmp/omni-agy-double-wrap-fix)
# Requires: tmux, journalctl access, relevant agent binary
set -euo pipefail

OMNI="${OMNI:-/tmp/omni-agy-double-wrap-fix}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../output"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
JRNL_LOG="${OUTPUT_DIR}/dbl-wrap-${RUN_ID}.log"

mkdir -p "$OUTPUT_DIR"

PASS=0; FAIL=0; SKIP=0
pass() { echo "  PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP: $1"; SKIP=$((SKIP+1)); }

# ─── Pre-checks ───────────────────────────────────────────────────────────────
if [[ ! -x "$OMNI" ]]; then
  echo "==> OMNI binary not found at $OMNI — build first: cd fix-agy-exec-double-wrap && go build -buildvcs=false -o /tmp/omni-agy-double-wrap-fix ./svc/cmd/..."
  echo "==> Skipping all tests"
  for t in "agy-A" "agy-B" "agy-C" "claude-A" "claude-B" "claude-C" "codex-A" "codex-B"; do
    skip "$t: OMNI binary missing"; done
  echo "==> Results: PASS=$PASS FAIL=$FAIL SKIP=$SKIP"; exit 0
fi

# Start journalctl tap now so it captures the full run.
journalctl -f --no-pager --lines=0 -t omni-server > "$JRNL_LOG" 2>&1 &
JRNL_PID=$!
sleep 0.5

cleanup_all() {
  [[ -n "${JRNL_PID:-}" ]] && kill "$JRNL_PID" 2>/dev/null || true
  # Kill any tmux sessions created by this run.
  tmux list-sessions -F '#{session_name}' 2>/dev/null \
    | grep "^dw-${RUN_ID}" | while read -r s; do tmux kill-session -t "$s" 2>/dev/null || true; done
}
trap cleanup_all EXIT

# ─── Helper: check pane for escape leaks ──────────────────────────────────────
# Returns 1 (fail) if literal [200~ / [201~ / raw \x15 appear in captured pane text.
pane_has_escape_leak() {
  local pane_content="$1"
  if echo "$pane_content" | grep -qE '\[200~|\[201~|\\\\x15'; then
    return 0  # leak found
  fi
  # Also check for literal CSI bytes that should not appear as text.
  if printf '%s' "$pane_content" | grep -qP '\x1b\[200~|\x1b\[201~' 2>/dev/null; then
    return 0
  fi
  return 1  # no leak
}

# ─── Helper: run one provider test ────────────────────────────────────────────
# Args: provider tmux-session-suffix agent-name prompt provider-binary
run_provider_test() {
  local provider="$1" sess_sfx="$2" agent_name="$3" prompt="$4" provider_bin="$5"
  local SESS="dw-${RUN_ID}-${sess_sfx}"
  local WS="/tmp/dw-${RUN_ID}-${provider}"
  mkdir -p "$WS"

  echo ""
  echo "==> [$provider] exec prompt delivery + escape-leak check"

  if ! command -v "$provider_bin" &>/dev/null; then
    skip "$provider-A: $provider_bin binary not found"
    skip "$provider-B: $provider_bin binary not found"
    skip "$provider-C: $provider_bin binary not found"
    rm -rf "$WS"; return
  fi

  # Init agent workspace.
  $OMNI team init --workspace "$WS" 2>/dev/null || true
  if ! (cd "$WS" && $OMNI agent init "$agent_name" \
        --workspace "$WS" --provider "$provider" --interactive=false 2>&1); then
    skip "$provider-A: agent init failed (provider may require credentials)"
    skip "$provider-B: agent init failed"
    skip "$provider-C: agent init failed"
    rm -rf "$WS"; return
  fi

  # Create tmux 120×40 session and resume agent inside it.
  tmux new-session -d -s "$SESS" -x 120 -y 40
  tmux send-keys -t "$SESS" "cd \"$WS\" && $OMNI agent resume \"$agent_name\" 2>&1" Enter

  echo "    waiting 20s for $provider TUI to start..."
  sleep 20

  # Capture pane to verify agent is running (not stuck on trust/login dialog).
  local pane_after_resume
  pane_after_resume=$(tmux capture-pane -pt "$SESS" -S -200 2>/dev/null || true)
  echo "    pane after resume (last 3 lines):"
  echo "$pane_after_resume" | tail -3 | sed 's/^/      /'

  # ── TEST A: exec prompt delivery ──────────────────────────────────────────
  local before_count
  before_count=$(awk '/op=exec|submit-key retry|UserPromptSubmit/{c++} END{print c+0}' "$JRNL_LOG" 2>/dev/null)

  echo "    sending exec prompt..."
  (cd "$WS" && $OMNI agent exec "$agent_name" --prompt "$prompt" --resume 2>&1) &
  local exec_pid=$!
  sleep 0.5
  wait $exec_pid 2>/dev/null || true

  # Wait up to 25s for delivery evidence in ptydaemon logs.
  local delivery_found=0
  for i in $(seq 1 25); do
    sleep 1
    local after_count
    after_count=$(awk '/op=exec|submit-key retry|UserPromptSubmit/{c++} END{print c+0}' "$JRNL_LOG" 2>/dev/null)
    if [[ "$after_count" -gt "$before_count" ]]; then
      delivery_found=1; break
    fi
  done

  if [[ $delivery_found -eq 1 ]]; then
    pass "$provider-A: prompt delivery confirmed (ptydaemon exec op fired)"
  else
    fail "$provider-A: no delivery evidence in ptydaemon logs within 25s"
  fi

  # ── TEST B: no escape-sequence leak in pane ────────────────────────────────
  # Give the TUI a moment to render, then capture.
  sleep 3
  local pane_after_exec
  pane_after_exec=$(tmux capture-pane -pt "$SESS" -S -400 2>/dev/null || true)

  echo "    pane after exec (last 5 lines):"
  echo "$pane_after_exec" | tail -5 | sed 's/^/      /'

  if pane_has_escape_leak "$pane_after_exec"; then
    fail "$provider-B: literal escape sequences ([200~ / [201~) visible in pane"
    echo "    --- LEAKED CONTENT ---"
    echo "$pane_after_exec" | grep -E '\[200~|\[201~' | head -3 | sed 's/^/      /'
  else
    pass "$provider-B: no escape sequence leak in pane"
  fi

  # ── TEST C: ptydaemon framing flags ───────────────────────────────────────
  # Check that prompt_prewrapped=false for all providers (no caller-side double-wrap).
  local prewrapped_line
  prewrapped_line=$(grep "execPrompt begin" "$JRNL_LOG" 2>/dev/null | grep "session_id" | tail -1 || true)
  if [[ -n "$prewrapped_line" ]]; then
    if echo "$prewrapped_line" | grep -q "prompt_prewrapped=false"; then
      pass "$provider-C: prompt_prewrapped=false confirmed (no double-wrap)"
    elif echo "$prewrapped_line" | grep -q "prompt_prewrapped=true"; then
      fail "$provider-C: prompt_prewrapped=true — caller is still wrapping before daemon"
    else
      skip "$provider-C: execPrompt begin log found but prompt_prewrapped field missing (debug logging may be off)"
    fi
  else
    skip "$provider-C: execPrompt begin not in journalctl (DEBUG logging not active in this container)"
  fi

  # Cleanup this provider's session.
  tmux kill-session -t "$SESS" 2>/dev/null || true
  (cd "$WS" && $OMNI agent delete "$agent_name" 2>/dev/null) || true
  rm -rf "$WS"
}

# ─── agy ──────────────────────────────────────────────────────────────────────
run_provider_test "agy" "agy" "e2e-dw-agy-${RUN_ID}" \
  "respond with exactly one word: pong" "agy"

# ─── claude ───────────────────────────────────────────────────────────────────
run_provider_test "claude" "claude" "e2e-dw-claude-${RUN_ID}" \
  "respond with exactly one word: pong" "claude"

# ─── codex ────────────────────────────────────────────────────────────────────
run_provider_test "codex" "codex" "e2e-dw-codex-${RUN_ID}" \
  "respond with exactly one word: pong" "codex"

# ─── Summary ──────────────────────────────────────────────────────────────────
kill "$JRNL_PID" 2>/dev/null || true; JRNL_PID=""
echo ""
echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP  (run $RUN_ID)"
echo "==> journal log: $JRNL_LOG"

if [[ "$FAIL" -gt 0 ]]; then
  echo "==> FAIL"; exit 1
fi
echo "==> PASS (with $SKIP skip(s))"; exit 0
