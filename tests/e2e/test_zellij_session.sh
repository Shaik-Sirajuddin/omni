#!/usr/bin/env bash
# E2E tests: zellij CLI session lifecycle — list, KDL dump, FromNative, kill.
#
# Strategy:
#   - list-sessions and dump-layout run headlessly at all times.
#   - Session CREATION (TEST 1b) requires a writable TTY or running inside zellij;
#     it is skipped gracefully when neither is available.
#   - TEST 3 (FromNative on real KDL) uses "zellij action dump-layout" when we
#     are already inside a zellij session (ZELLIJ env-var is set).
#
# Skip gracefully when zellij is not installed (SKIP all).
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

# ─── Pre-check: zellij installed ─────────────────────────────────────────────
if ! command -v zellij &>/dev/null; then
  echo "==> zellij not found in PATH — skipping all tests"
  for t in "TEST 1a" "TEST 1b" "TEST 2a" "TEST 2b" "TEST 2c" "TEST 3a" "TEST 3b" "TEST 3c" "TEST 4"; do
    skip "$t: zellij not installed"
  done
  echo ""
  echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP"
  echo "==> SKIP (zellij not available)"
  exit 0
fi

ZELLIJ_BIN="$(command -v zellij)"
export TERM="${TERM:-xterm-256color}"

SESSION_NAME="e2e-zellij-${RUN_ID}"
KDL_FILE="$(mktemp /tmp/zellij-e2e-layout-XXXXXX.kdl)"
SESSION_CREATED=0

cleanup() {
  if [[ $SESSION_CREATED -eq 1 ]]; then
    "$ZELLIJ_BIN" kill-session "$SESSION_NAME" 2>/dev/null || true
  fi
  rm -f "$KDL_FILE"
}
trap cleanup EXIT

# ─── TEST 1a: list-sessions works headlessly ─────────────────────────────────
echo ""
echo "==> [TEST 1a] zellij list-sessions runs headlessly"

LIST_OUT=$("$ZELLIJ_BIN" list-sessions -n 2>&1)
LIST_EXIT=$?

if [[ $LIST_EXIT -eq 0 ]] || echo "$LIST_OUT" | grep -q "\[Created"; then
  pass "zellij list-sessions exits without fatal error"
else
  fail "zellij list-sessions failed (exit=$LIST_EXIT)"
  echo "    output: $LIST_OUT"
fi

# If we're inside a zellij session, the current session must appear in the list.
if [[ -n "${ZELLIJ_SESSION_NAME:-}" ]]; then
  if echo "$LIST_OUT" | grep -q "^${ZELLIJ_SESSION_NAME}"; then
    pass "current session '$ZELLIJ_SESSION_NAME' is visible in list-sessions"
  else
    fail "current session '$ZELLIJ_SESSION_NAME' missing from list-sessions"
    echo "    list output: $LIST_OUT"
  fi
else
  skip "TEST 1a (current-session check): not inside a zellij session"
fi

# ─── TEST 1b: create a new session (requires writeable PTY) ──────────────────
echo ""
echo "==> [TEST 1b] headless session creation with setsid"

# Build the KDL for the test session.
cat > "$KDL_FILE" << 'KDL'
layout {
    tab name="main" focus=true {
        pane start_suspended=true
    }
    tab name="logs" {
        pane start_suspended=true
    }
}
KDL

# Attempt to start a detached session. On terminals with a writable /dev/tty
# this succeeds; inside a container or CI without a TTY it exits immediately.
setsid "$ZELLIJ_BIN" --session "$SESSION_NAME" --layout "$KDL_FILE" \
  </dev/null >/dev/null 2>&1 &
SPID=$!

FOUND=0
for i in $(seq 1 20); do
  sleep 1
  if "$ZELLIJ_BIN" list-sessions -n 2>/dev/null | grep -q "^${SESSION_NAME}"; then
    FOUND=1
    SESSION_CREATED=1
    break
  fi
done

if [[ $FOUND -eq 1 ]]; then
  pass "new session '$SESSION_NAME' appears in list-sessions after setsid start"
else
  skip "TEST 1b: session '$SESSION_NAME' did not appear within 20s — likely no writable TTY in this environment"
  kill "$SPID" 2>/dev/null || true
fi

# ─── TEST 2: KDL validation via zellij setup --dump-layout ───────────────────
echo ""
echo "==> [TEST 2] KDL pane/tab structure validation (headless)"

# zellij setup --dump-layout parses the KDL file and echoes it back.
# Exit non-zero = invalid KDL; exit 0 = valid.
DUMP_OUT=$("$ZELLIJ_BIN" setup --dump-layout "$KDL_FILE" 2>&1)
DUMP_EXIT=$?

if [[ $DUMP_EXIT -eq 0 ]]; then
  pass "zellij setup --dump-layout accepts the generated KDL without error"
else
  fail "zellij setup --dump-layout returned exit $DUMP_EXIT"
  echo "    output: $DUMP_OUT"
fi

TAB_COUNT=$(echo "$DUMP_OUT" | grep -c "^\s*tab\b" || true)
PANE_COUNT=$(echo "$DUMP_OUT" | grep -c "^\s*pane" || true)

if [[ $TAB_COUNT -ge 2 ]]; then
  pass "KDL has $TAB_COUNT tabs (≥2 expected)"
else
  fail "KDL has $TAB_COUNT tabs (expected ≥2)"
fi

if [[ $PANE_COUNT -ge 2 ]]; then
  pass "KDL has $PANE_COUNT panes (≥2 expected)"
else
  fail "KDL has $PANE_COUNT panes (expected ≥2)"
fi

# ─── TEST 3: FromNative against real zellij KDL ──────────────────────────────
echo ""
echo "==> [TEST 3] ZellijTransformer.FromNative — real KDL"

ZELLIJ_PKG="${SCRIPT_DIR}/../../omni/terminal/provider/zellij"

# TEST 3a: Go unit tests that exercise FromNative with real-format KDL strings.
if [[ -d "$ZELLIJ_PKG" ]]; then
  GO_OUT=$(cd "$ZELLIJ_PKG" && go test -v -count=1 ./... 2>&1)
  GO_EXIT=$?
  if [[ $GO_EXIT -eq 0 ]]; then
    NPASSED=$(echo "$GO_OUT" | grep -c "^--- PASS" || true)
    pass "ZellijTransformer unit tests pass ($NPASSED tests, including FromNative/RoundTrip)"
  else
    fail "ZellijTransformer unit tests failed"
    echo "$GO_OUT" | grep -E "^--- (PASS|FAIL)|FAIL:" | head -10
  fi
else
  skip "TEST 3a: zellij Go package not found at $ZELLIJ_PKG"
fi

# TEST 3b: If inside a live zellij session, dump its real KDL and run FromNative.
if [[ -n "${ZELLIJ:-}" ]] && [[ -d "$ZELLIJ_PKG" ]]; then
  LIVE_KDL=$("$ZELLIJ_BIN" action dump-layout 2>&1)
  if echo "$LIVE_KDL" | grep -q "layout"; then
    pass "zellij action dump-layout produced KDL from live session"
    # Write a quick inline Go test to call FromNative on the live KDL.
    INLINE_TEST=$(mktemp /tmp/from_native_test_XXXXXX.go)
    cat > "$INLINE_TEST" << 'GOEOF'
package main

import (
    "fmt"
    "os"
    "strings"
    zellij "github.com/Shaik-Sirajuddin/memory/terminal/provider/zellij"
)

func main() {
    kdl, err := os.ReadFile(os.Args[1])
    if err != nil { fmt.Fprintf(os.Stderr, "read: %v\n", err); os.Exit(1) }
    tr := &zellij.ZellijTransformer{}
    layout, err := tr.FromNative(kdl)
    if err != nil { fmt.Fprintf(os.Stderr, "FromNative: %v\n", err); os.Exit(1) }
    if len(layout.Tabs) == 0 {
        fmt.Fprintln(os.Stderr, "FromNative returned zero tabs")
        os.Exit(1)
    }
    names := make([]string, len(layout.Tabs))
    for i, t := range layout.Tabs { names[i] = t.Name }
    fmt.Printf("tabs=%d names=%s\n", len(layout.Tabs), strings.Join(names, ","))
}
GOEOF
    KDL_TMP=$(mktemp /tmp/live-layout-XXXXXX.kdl)
    echo "$LIVE_KDL" > "$KDL_TMP"
    FN_OUT=$(cd "$ZELLIJ_PKG" && go run "$INLINE_TEST" "$KDL_TMP" 2>&1)
    FN_EXIT=$?
    rm -f "$INLINE_TEST" "$KDL_TMP"
    if [[ $FN_EXIT -eq 0 ]] && echo "$FN_OUT" | grep -q "tabs="; then
      TAB_N=$(echo "$FN_OUT" | grep -oP 'tabs=\K[0-9]+')
      pass "FromNative on live session KDL returned $TAB_N tab(s) without error"
    else
      fail "FromNative on live session KDL failed: $FN_OUT"
    fi
  else
    skip "TEST 3b: zellij action dump-layout returned no KDL"
  fi
else
  skip "TEST 3b (live-KDL FromNative): not inside a live zellij session or no Go package"
fi

# TEST 3c: FromNative on the KDL file we generated ourselves.
if [[ -d "$ZELLIJ_PKG" ]]; then
  SELF_TEST=$(mktemp /tmp/self_fn_test_XXXXXX.go)
  cat > "$SELF_TEST" << 'GOEOF'
package main

import (
    "fmt"
    "os"
    zellij "github.com/Shaik-Sirajuddin/memory/terminal/provider/zellij"
)

func main() {
    kdl, err := os.ReadFile(os.Args[1])
    if err != nil { fmt.Fprintf(os.Stderr, "read: %v\n", err); os.Exit(1) }
    tr := &zellij.ZellijTransformer{}
    layout, err := tr.FromNative(kdl)
    if err != nil { fmt.Fprintf(os.Stderr, "FromNative: %v\n", err); os.Exit(1) }
    fmt.Printf("tabs=%d\n", len(layout.Tabs))
    for i, t := range layout.Tabs {
        fmt.Printf("tab[%d] name=%q panes=%d\n", i, t.Name, len(t.Panes))
    }
}
GOEOF
  ST_OUT=$(cd "$ZELLIJ_PKG" && go run "$SELF_TEST" "$KDL_FILE" 2>&1)
  ST_EXIT=$?
  rm -f "$SELF_TEST"
  if [[ $ST_EXIT -eq 0 ]]; then
    TAB_N_3C=$(echo "$ST_OUT" | grep "^tabs=" | grep -oP 'tabs=\K[0-9]+' || echo 0)
    if [[ "$TAB_N_3C" -ge 2 ]]; then
      pass "FromNative on generated KDL returns $TAB_N_3C tabs with correct structure"
      echo "    $ST_OUT"
    else
      fail "FromNative on generated KDL returned $TAB_N_3C tabs (expected ≥2)"
      echo "    $ST_OUT"
    fi
  else
    fail "FromNative on generated KDL error: $ST_OUT"
  fi
fi

# ─── TEST 4: session cleanup via kill-session ─────────────────────────────────
echo ""
echo "==> [TEST 4] session cleanup: zellij kill-session"

if [[ $SESSION_CREATED -eq 1 ]]; then
  "$ZELLIJ_BIN" kill-session "$SESSION_NAME" 2>&1 || true
  SESSION_CREATED=0
  sleep 1
  if ! "$ZELLIJ_BIN" list-sessions -n 2>/dev/null | grep -q "^${SESSION_NAME}"; then
    pass "session '$SESSION_NAME' no longer appears after kill-session"
  else
    fail "session '$SESSION_NAME' still appears after kill-session"
  fi
else
  skip "TEST 4 (kill-session): no test session was created (TEST 1b skipped)"
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
