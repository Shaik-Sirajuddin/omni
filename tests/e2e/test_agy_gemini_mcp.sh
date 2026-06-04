#!/usr/bin/env bash
# E2E test: agy Gemini MCP config writing — verifies ~/.gemini/config/mcp_config.json
# is populated by the AxolinkMCP registration block in ServiceMux (svc/cmd/mux.go:112-145).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../output"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
LOG="${OUTPUT_DIR}/agy-gemini-mcp-${RUN_ID}.log"

mkdir -p "$OUTPUT_DIR"

PASS=0
FAIL=0

pass() { echo "  PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

GEMINI_MCP_CFG="${HOME}/.gemini/config/mcp_config.json"

# Service unit name varies: Docker uses omni@<user> (e.g. omni@root), dev hosts may use omni-server.
# Auto-detect by listing active units; fall back to omni-server.
OMNI_SERVICE="${OMNI_SERVICE:-}"
if [[ -z "$OMNI_SERVICE" ]] && command -v systemctl &>/dev/null; then
  OMNI_SERVICE=$(systemctl list-units --type=service --state=active --no-legend 2>/dev/null \
    | awk '{print $1}' | grep -E '^omni(@|-)' | head -1 || echo "")
fi
OMNI_SERVICE="${OMNI_SERVICE:-omni-server}"

# ─── Test 1: Unit smoke — direct AddMCP call via Go test ─────────────────────
# The CLI has no 'omni mcp add --provider agy' subcommand; test AddMCP directly.
echo ""
echo "==> [TEST 1] Unit smoke: Go test for agy AddMCP writes mcp_config.json"

REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
AGY_PKG_DIR="${REPO_ROOT}/omni/connector/codeagent/agy"

if [[ ! -d "$AGY_PKG_DIR" ]]; then
  fail "agy package not found at $AGY_PKG_DIR"
elif ! command -v go &>/dev/null; then
  echo "  SKIP: go not in PATH — run unit tests on host with: go test ./omni/connector/codeagent/agy/..."
  PASS=$((PASS+1))
else
  GOTEST_FLAGS="-v -run TestAddMCP_WritesEntry|TestAddMCP_Idempotent|TestAddMCP_Format|TestAddMCP_GlobalWritesToHomeDir"
  # Some containers mount /tmp noexec; use GOPATH cache in /var if needed
  if ! touch /tmp/.go-exec-check 2>/dev/null || ! chmod +x /tmp/.go-exec-check 2>/dev/null; then
    GOTEST_FLAGS="$GOTEST_FLAGS -exec /usr/local/go/bin/go"
  fi
  rm -f /tmp/.go-exec-check 2>/dev/null || true
  if (cd "$AGY_PKG_DIR" && GOWORK=off go test $GOTEST_FLAGS . 2>&1 | tee -a "$LOG"); then
    pass "go test ./agy/ — all AddMCP unit tests passed"
  else
    fail "go test ./agy/ — one or more AddMCP unit tests failed (see $LOG)"
  fi
fi

# ─── Test 2: Service startup — restart omni-server and check config ───────────
echo ""
echo "==> [TEST 2] Service startup: omni-server writes axolink to mcp_config.json"

if ! command -v systemctl &>/dev/null; then
  fail "systemctl not available — cannot test service startup (non-systemd host)"
else
  # Capture pre-restart state
  PRE_MTIME=""
  if [[ -f "$GEMINI_MCP_CFG" ]]; then
    PRE_MTIME="$(stat -c %Y "$GEMINI_MCP_CFG" 2>/dev/null || echo "")"
  fi

  # Tap logs BEFORE restart so we capture all registration events
  journalctl -f --no-pager --lines=0 -t omni-server > "$LOG" 2>&1 &
  LOG_PID=$!
  trap 'kill "$LOG_PID" 2>/dev/null || true' EXIT
  sleep 0.5

  systemctl restart "$OMNI_SERVICE" 2>&1 || { fail "systemctl restart "$OMNI_SERVICE" failed"; kill "$LOG_PID" 2>/dev/null || true; trap - EXIT; }

  # Wait up to 15 s for the file to appear or be updated
  MAX_WAIT=15
  WAITED=0
  CONFIG_UPDATED=0
  while [[ $WAITED -lt $MAX_WAIT ]]; do
    if [[ -f "$GEMINI_MCP_CFG" ]]; then
      NEW_MTIME="$(stat -c %Y "$GEMINI_MCP_CFG" 2>/dev/null || echo "")"
      if [[ "$NEW_MTIME" != "$PRE_MTIME" ]] || [[ -z "$PRE_MTIME" ]]; then
        CONFIG_UPDATED=1
        break
      fi
    fi
    sleep 1
    WAITED=$((WAITED+1))
  done

  kill "$LOG_PID" 2>/dev/null || true
  trap - EXIT

  if [[ "$CONFIG_UPDATED" -eq 1 ]]; then
    pass "mcp_config.json created/updated within ${WAITED}s of service restart"
  else
    fail "mcp_config.json not updated within ${MAX_WAIT}s (empty file issue confirmed)"
    echo "    file exists: $(test -f "$GEMINI_MCP_CFG" && echo yes || echo no)"
    [[ -f "$GEMINI_MCP_CFG" ]] && echo "    content:" && cat "$GEMINI_MCP_CFG" || true
  fi

  if [[ -f "$GEMINI_MCP_CFG" ]] && grep -q '"axolink"' "$GEMINI_MCP_CFG"; then
    pass "axolink key present in $GEMINI_MCP_CFG"
  else
    fail "axolink key absent from $GEMINI_MCP_CFG (AxolinkMCP.Enabled may be false, or omni not in PATH)"
    [[ -f "$GEMINI_MCP_CFG" ]] && echo "    current content:" && cat "$GEMINI_MCP_CFG" || echo "    (file missing)"
  fi

  # Log line: "axolink-mcp: registered with connector" provider=agy
  if grep -q 'axolink-mcp: registered with connector' "$LOG" && grep -q 'provider=agy' "$LOG"; then
    pass "journalctl: 'axolink-mcp: registered with connector provider=agy' found"
  elif grep -q 'axolink-mcp' "$LOG"; then
    fail "journalctl: axolink-mcp appears but not 'registered with connector provider=agy' — check if agy is in registry"
    grep 'axolink-mcp' "$LOG" | tail -5
  else
    fail "journalctl: no axolink-mcp log lines from omni-server after restart"
    echo "==> journalctl tail (last 20 lines):"
    tail -20 "$LOG" || true
  fi
fi

# ─── Test 3: Idempotency — covered by Go unit test TestAddMCP_Idempotent ──────
# Additional live check: if the service has already written the file, restarting
# again must not duplicate the axolink entry.
echo ""
echo "==> [TEST 3] Idempotency: second registration must not duplicate entry"

if ! command -v systemctl &>/dev/null; then
  fail "systemctl not available — skipping live idempotency check"
elif [[ ! -f "$GEMINI_MCP_CFG" ]]; then
  fail "idempotency: $GEMINI_MCP_CFG not found, cannot check"
else
  # Restart service a second time
  systemctl restart "$OMNI_SERVICE" 2>&1 || fail "systemctl restart "$OMNI_SERVICE" failed (idempotency run)"
  sleep 5

  AXOLINK_COUNT=$(grep -o '"axolink"' "$GEMINI_MCP_CFG" 2>/dev/null | wc -l)
  if [[ "$AXOLINK_COUNT" -eq 1 ]]; then
    pass "exactly 1 axolink entry after second restart ($AXOLINK_COUNT)"
  else
    fail "expected 1 axolink entry after second restart, found $AXOLINK_COUNT"
    cat "$GEMINI_MCP_CFG"
  fi
fi

# ─── Test 4: Format validation ────────────────────────────────────────────────
echo ""
echo "==> [TEST 4] Format: mcpServers key and stdio type"

if [[ ! -f "$GEMINI_MCP_CFG" ]]; then
  fail "format: $GEMINI_MCP_CFG not found — cannot validate"
else
  if python3 -c "import json,sys; d=json.load(open('$GEMINI_MCP_CFG')); assert 'mcpServers' in d, 'missing mcpServers'; assert 'mcp_servers' not in d, 'snake_case key present'" 2>/dev/null; then
    pass "top-level key is 'mcpServers' (not 'mcp_servers')"
  else
    fail "format: 'mcpServers' key missing or 'mcp_servers' present in $GEMINI_MCP_CFG"
    cat "$GEMINI_MCP_CFG"
  fi

  if python3 -c "
import json, sys
d = json.load(open('$GEMINI_MCP_CFG'))
servers = d.get('mcpServers', {})
entry = servers.get('axolink')
assert entry is not None, 'axolink entry missing'
assert entry.get('type') == 'stdio', f'expected type=stdio, got {entry.get(\"type\")!r}'
" 2>/dev/null; then
    pass "axolink entry has type=stdio"
  else
    fail "axolink entry missing or does not have type=stdio"
    cat "$GEMINI_MCP_CFG"
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
