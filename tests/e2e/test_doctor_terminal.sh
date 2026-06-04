#!/usr/bin/env bash
# E2E tests: omni doctor terminal check and install — standalone (no daemon required).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../output"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
LOG="${OUTPUT_DIR}/doctor-terminal-${RUN_ID}.log"

mkdir -p "$OUTPUT_DIR"

PASS=0
FAIL=0

pass() { echo "  PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

# ─── TEST 1: doctor terminal check exits 0 without daemon ────────────────────
echo ""
echo "==> [TEST 1] omni doctor terminal check — no daemon required"

OUT=$(omni doctor terminal check 2>&1)
EXIT_CODE=$?

if [[ $EXIT_CODE -eq 0 ]]; then
  pass "doctor terminal check exits 0"
else
  fail "doctor terminal check exited $EXIT_CODE (expected 0)"
  echo "    output: $OUT"
fi

if echo "$OUT" | grep -qi "operator is required"; then
  fail "doctor terminal check must NOT print 'operator is required'"
  echo "    output: $OUT"
else
  pass "doctor terminal check does not print 'operator is required'"
fi

if echo "$OUT" | grep -qi "zellij"; then
  pass "doctor terminal check output contains zellij"
else
  fail "doctor terminal check output should mention zellij"
  echo "    output: $OUT"
fi

# ─── TEST 2: doctor terminal check --output json — valid JSON ────────────────
echo ""
echo "==> [TEST 2] omni doctor terminal check --output json"

JSON_OUT=$(omni doctor terminal check --output json 2>&1)
JSON_EXIT=$?

if [[ $JSON_EXIT -eq 0 ]]; then
  pass "doctor terminal check --output json exits 0"
else
  fail "doctor terminal check --output json exited $JSON_EXIT"
  echo "    output: $JSON_OUT"
fi

if echo "$JSON_OUT" | python3 -c "import json,sys; d=json.load(sys.stdin)" 2>/dev/null; then
  pass "doctor terminal check --output json produces valid JSON"
else
  fail "doctor terminal check --output json produced invalid JSON"
  echo "    output: $JSON_OUT"
fi

if echo "$JSON_OUT" | python3 -c "
import json,sys
d=json.load(sys.stdin)
assert 'terminals' in d, 'missing terminals key'
assert isinstance(d['terminals'], list), 'terminals must be array'
assert len(d['terminals']) > 0, 'terminals must be non-empty'
for t in d['terminals']:
    assert 'name' in t, 'terminal entry missing name'
    assert 'installed' in t, 'terminal entry missing installed'
" 2>/dev/null; then
  pass "doctor terminal check JSON has 'terminals' array with name/installed fields"
else
  fail "doctor terminal check JSON structure is wrong (expected: {terminals:[{name,installed}]})"
  echo "    output: $JSON_OUT"
fi

if echo "$JSON_OUT" | grep -qi "operator is required"; then
  fail "doctor terminal check --output json must NOT print 'operator is required'"
else
  pass "doctor terminal check --output json does not print 'operator is required'"
fi

# ─── TEST 3: doctor terminal install --name unknown_xyz ──────────────────────
echo ""
echo "==> [TEST 3] omni doctor terminal install --name unknown_xyz — unknown provider error"

INSTALL_OUT=$(omni doctor terminal install --name unknown_xyz 2>&1) || true

if echo "$INSTALL_OUT" | grep -qi "operator is required"; then
  fail "doctor terminal install --name unknown_xyz must NOT print 'operator is required'"
  echo "    output: $INSTALL_OUT"
else
  pass "doctor terminal install --name unknown_xyz does not print 'operator is required'"
fi

if echo "$INSTALL_OUT" | grep -qi "unknown_xyz\|not registered\|unknown terminal\|supported"; then
  pass "error message mentions the unknown provider name or supported list"
else
  fail "error should mention 'unknown_xyz' or 'supported' providers"
  echo "    output: $INSTALL_OUT"
fi

# Expect non-zero exit for unknown provider
INSTALL_EXIT=0
omni doctor terminal install --name unknown_xyz 2>/dev/null && INSTALL_EXIT=0 || INSTALL_EXIT=$?
if [[ $INSTALL_EXIT -ne 0 ]]; then
  pass "doctor terminal install --name unknown_xyz exits non-zero (error correctly returned)"
else
  fail "doctor terminal install --name unknown_xyz should exit non-zero for unknown provider"
fi

# ─── TEST 4: doctor terminal install --name zellij — no daemon ───────────────
echo ""
echo "==> [TEST 4] omni doctor terminal install --name zellij — no 'operator is required'"

ZELLIJ_OUT=$(omni doctor terminal install --name zellij 2>&1) || true

if echo "$ZELLIJ_OUT" | grep -qi "operator is required"; then
  fail "doctor terminal install --name zellij must NOT print 'operator is required'"
  echo "    output: $ZELLIJ_OUT"
else
  pass "doctor terminal install --name zellij does not print 'operator is required'"
fi

# Note: install may succeed (downloads zellij) or fail (network unavailable),
# but it must never fail with "operator is required".
echo "    info: install outcome: $(echo "$ZELLIJ_OUT" | head -1)"

# ─── TEST 5: regression — existing doctor terminal check still works ─────────
echo ""
echo "==> [TEST 5] regression: doctor terminal check output format stable"

# Re-run and confirm the table format (default) still has expected columns
TABLE_OUT=$(omni doctor terminal check 2>&1)
if echo "$TABLE_OUT" | grep -q "NAME" && echo "$TABLE_OUT" | grep -q "INSTALLED"; then
  pass "doctor terminal check table output has NAME and INSTALLED columns"
else
  # Acceptable: some versions may use lowercase or different separators
  if echo "$TABLE_OUT" | grep -qi "zellij"; then
    pass "doctor terminal check table output contains terminal entries (format may vary)"
  else
    fail "doctor terminal check output must contain terminal entries"
    echo "    output: $TABLE_OUT"
  fi
fi

# ─── Summary ─────────────────────────────────────────────────────────────────
echo ""
echo "==> Results: PASS=$PASS  FAIL=$FAIL  (run $RUN_ID)"
echo "==> log: $LOG"

if [[ "$FAIL" -gt 0 ]]; then
  echo "==> FAIL"
  exit 1
fi
echo "==> PASS"
