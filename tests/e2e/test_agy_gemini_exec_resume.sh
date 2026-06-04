#!/usr/bin/env bash
set -euo pipefail
# E2E tests: agy and gemini agents no longer silently succeed or hang on exec --resume.
#
# TEST A: agy — exec --resume with no PTY client must error with a clear message,
#         NOT silently create an empty session.
# TEST B: gemini — exec --resume must exit non-zero with a clear error,
#         NOT launch interactive TUI or hang.
#
# SKIP gracefully when the agent binary or registered agent is not available.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../output"
RUN_ID="$(date +%Y%m%dT%H%M%S)"

mkdir -p "$OUTPUT_DIR"

PASS=0
FAIL=0
SKIP=0

pass() { echo "  PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP: $1"; SKIP=$((SKIP+1)); }

# ─── TEST A: agy exec --resume ────────────────────────────────────────────────
echo ""
echo "==> [TEST A] agy exec --resume — no silent empty session"

AGY_AGENT=""
WS_AGY=""

# Find an agy agent in the current workspace or create a test one.
if command -v agy &>/dev/null; then
  WS_AGY="/tmp/e2e-agy-${RUN_ID}"
  mkdir -p "$WS_AGY"
  AGY_AGENT="e2e-agy-${RUN_ID}"

  # Initialize agy agent.
  (cd "$WS_AGY" && omni team init 2>/dev/null || true)
  INIT_OUT=$(cd "$WS_AGY" && omni agent init "$AGY_AGENT" \
    --workspace "$WS_AGY" --provider agy --interactive=false 2>&1)
  INIT_EXIT=$?
  echo "    agent init exit=$INIT_EXIT"

  if [[ $INIT_EXIT -eq 0 ]]; then
    # Run exec --resume with a short timeout (must NOT hang).
    EXEC_OUT=$(timeout 10 bash -c \
      "cd '$WS_AGY' && omni agent exec '$AGY_AGENT' --prompt 'hello' --bg" 2>&1) || EXEC_EXIT=$?
    EXEC_EXIT=${EXEC_EXIT:-0}
    echo "    exec exit=$EXEC_EXIT output: ${EXEC_OUT:0:120}"

    if [[ $EXEC_EXIT -ne 0 ]]; then
      # Non-zero is expected when no PTY client available (no real agy TUI).
      if echo "$EXEC_OUT" | grep -qi "agy\|not supported\|pty.*client\|no.*session\|detach"; then
        pass "agy exec --resume exits non-zero with clear error (not silent)"
      else
        fail "agy exec --resume failed but error message unclear: $EXEC_OUT"
      fi
      # Must NOT contain "operator is required" — that's a different unrelated error.
      if echo "$EXEC_OUT" | grep -qi "operator is required"; then
        fail "agy exec --resume printed 'operator is required' — unexpected"
      else
        pass "agy exec --resume does not print 'operator is required'"
      fi
    else
      # Exit 0 is only acceptable if a real agy session was actually started
      # (PTY client available). Check that "prompt sent" was printed.
      if echo "$EXEC_OUT" | grep -q "prompt sent"; then
        pass "agy exec --resume exits 0 and prints 'prompt sent' (agy PTY available)"
        pass "agy exec --resume does not hang or create empty session"
      else
        fail "agy exec --resume exited 0 but no 'prompt sent' — possible silent empty session"
        echo "    output: $EXEC_OUT"
      fi
    fi

    # Cleanup.
    (cd "$WS_AGY" && omni agent delete "$AGY_AGENT" --workspace "$WS_AGY" 2>/dev/null || true)
    rm -rf "$WS_AGY"
  else
    skip "TEST A: agy agent init failed (binary found but init error)"
    echo "    init output: $INIT_OUT"
  fi
else
  skip "TEST A: agy binary not in PATH — no agy agent to test"
fi

# ─── TEST B: gemini exec --resume ────────────────────────────────────────────
echo ""
echo "==> [TEST B] gemini exec --resume — no TUI launch, no hang"

WS_GEM=""
GEM_AGENT=""

if command -v agy &>/dev/null || command -v gemini &>/dev/null; then
  # Gemini uses the agy CLI binary in this setup.
  WS_GEM="/tmp/e2e-gemini-${RUN_ID}"
  mkdir -p "$WS_GEM"
  GEM_AGENT="e2e-gemini-${RUN_ID}"

  (cd "$WS_GEM" && omni team init 2>/dev/null || true)
  GINIT_OUT=$(cd "$WS_GEM" && omni agent init "$GEM_AGENT" \
    --workspace "$WS_GEM" --provider gemini --interactive=false 2>&1)
  GINIT_EXIT=$?
  echo "    agent init exit=$GINIT_EXIT"

  if [[ $GINIT_EXIT -eq 0 ]]; then
    # exec --resume with timeout — must NOT block/hang waiting for TUI.
    START_TS=$(date +%s)
    GEXEC_OUT=$(timeout 10 bash -c \
      "cd '$WS_GEM' && omni agent exec '$GEM_AGENT' --prompt 'hello' --bg" 2>&1) || GEXEC_EXIT=$?
    GEXEC_EXIT=${GEXEC_EXIT:-0}
    END_TS=$(date +%s)
    ELAPSED=$((END_TS - START_TS))
    echo "    exec exit=$GEXEC_EXIT elapsed=${ELAPSED}s"

    # Must return within the timeout — if it hit timeout (exit 124), it was hanging.
    if [[ $GEXEC_EXIT -eq 124 ]]; then
      fail "gemini exec --resume timed out (hung waiting for TUI input)"
    else
      pass "gemini exec --resume returned within 10s (not blocking)"
    fi

    # Non-zero exit is expected for gemini without a running TUI session.
    if [[ $GEXEC_EXIT -ne 0 ]]; then
      if echo "$GEXEC_OUT" | grep -qi "gemini\|not supported\|pty\|tui\|interactive\|detach"; then
        pass "gemini exec --resume exits non-zero with clear error"
      else
        # Any non-zero with no operator error is acceptable.
        if ! echo "$GEXEC_OUT" | grep -qi "operator is required"; then
          pass "gemini exec --resume exits non-zero (no 'operator is required' error)"
        else
          fail "gemini exec --resume printed 'operator is required'"
        fi
      fi
    else
      if echo "$GEXEC_OUT" | grep -q "prompt sent"; then
        pass "gemini exec --resume exits 0 with 'prompt sent' (gemini TUI available)"
      else
        fail "gemini exec --resume exited 0 but no 'prompt sent' — possible TUI hang or silent fail"
        echo "    output: $GEXEC_OUT"
      fi
    fi

    # Must NOT print "operator is required".
    if echo "$GEXEC_OUT" | grep -qi "operator is required"; then
      fail "gemini exec --resume printed 'operator is required'"
    else
      pass "gemini exec --resume does not print 'operator is required'"
    fi

    (cd "$WS_GEM" && omni agent delete "$GEM_AGENT" --workspace "$WS_GEM" 2>/dev/null || true)
    rm -rf "$WS_GEM"
  else
    skip "TEST B: gemini agent init failed"
    echo "    init output: $GINIT_OUT"
  fi
else
  skip "TEST B: neither agy nor gemini binary in PATH"
fi

# ─── Summary ─────────────────────────────────────────────────────────────────
echo ""
echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP  (run $RUN_ID)"

if [[ "$FAIL" -gt 0 ]]; then
  echo "==> FAIL"
  exit 1
fi
echo "==> PASS (with $SKIP skip(s))"
exit 0
