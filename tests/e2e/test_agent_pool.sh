#!/usr/bin/env bash
# E2E test: agent-pool unix-socket JSON daemon.
#
# Verifies:
#   TEST 1 — socket exists at the expected path
#   TEST 2 — register_config op returns ok:true (no credentials needed)
#   TEST 3 — invalid op returns ok:false + error (no credentials needed)
#   TEST 4 — get op returns ok:true and a non-empty session_id
#             (requires operator to be able to create agents — may be slow on first call)
#   TEST 5 — two sequential gets return different session_ids (pool uniqueness)
#
# Socket path resolution:
#   Primary:  /run/omni-root/agent-pool.sock
#   Fallback: $XDG_RUNTIME_DIR/omni/agent-pool.sock
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../output"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
LOG="${OUTPUT_DIR}/agent-pool-${RUN_ID}.log"

mkdir -p "$OUTPUT_DIR"

PASS=0; FAIL=0; SKIP=0
pass() { echo "  PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP: $1"; SKIP=$((SKIP+1)); }

# ─── Resolve socket path ──────────────────────────────────────────────────────
PRIMARY_SOCK="/run/omni-root/agent-pool.sock"
FALLBACK_SOCK="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/omni/agent-pool.sock"

SOCK=""
if [[ -S "$PRIMARY_SOCK" ]]; then
  SOCK="$PRIMARY_SOCK"
elif [[ -S "$FALLBACK_SOCK" ]]; then
  SOCK="$FALLBACK_SOCK"
fi

echo "==> agent-pool e2e  run=$RUN_ID"
echo "==> log: $LOG"
echo "==> socket: ${SOCK:-<not found>}"
echo ""

# ─── Transport ────────────────────────────────────────────────────────────────
# sock_rpc <socket> <json> <timeout_secs>
# Sends one newline-terminated JSON request, reads one response line.
# python3 used for correct half-close semantics on unix sockets.
sock_rpc() {
  local sock="$1" req="$2" timeout="${3:-30}"
  python3 -c "
import socket, sys
sock, timeout = '$sock', $timeout
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(timeout)
s.connect(sock)
s.sendall(b'$(printf '%s' "$req" | sed "s/'/'\\\\''/g")\n')
s.shutdown(socket.SHUT_WR)
resp = b''
try:
    while True:
        chunk = s.recv(4096)
        if not chunk: break
        resp += chunk
        if b'\n' in resp: break
except socket.timeout:
    pass
s.close()
line = resp.split(b'\n')[0]
print(line.decode() if line else '')
" 2>>"$LOG"
}

json_field() {
  local json="$1" field="$2"
  echo "$json" \
    | grep -oP "\"${field}\"\\s*:\\s*(\"[^\"]*\"|[^,}\\s]+)" \
    | sed -E "s/\"${field}\"\\s*:\\s*//; s/^\"//; s/\"$//" \
    || true
}

# ─── TEST 1: socket exists ────────────────────────────────────────────────────
echo "==> [TEST 1] socket exists"

if [[ -n "$SOCK" ]]; then
  pass "agent-pool.sock present at $SOCK"
else
  fail "agent-pool.sock not found (checked $PRIMARY_SOCK and $FALLBACK_SOCK)"
  echo ""
  echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP"
  echo "==> FAIL (socket absent — remaining tests cannot run)"
  exit 1
fi

# ─── TEST 2: register_config returns ok:true ─────────────────────────────────
echo ""
echo "==> [TEST 2] register_config"

REG_RESP="$(sock_rpc "$SOCK" '{"op":"register_config","config":{"provider":"claude","workspace":"/agents/e2e-pool-test","workspace_min":2,"max_parallel":3}}' 10 || true)"
echo "    response: $REG_RESP" | tee -a "$LOG"

if [[ "$(json_field "$REG_RESP" "ok")" == "true" ]]; then
  pass "register_config: ok=true"
else
  fail "register_config: expected ok=true, got: $REG_RESP"
fi

# ─── TEST 3: invalid op returns ok:false with error ──────────────────────────
echo ""
echo "==> [TEST 3] invalid op returns error"

INV_RESP="$(sock_rpc "$SOCK" '{"op":"invalid_op"}' 10 || true)"
echo "    response: $INV_RESP" | tee -a "$LOG"

INV_OK="$(json_field "$INV_RESP" "ok")"
INV_ERR="$(json_field "$INV_RESP" "error")"

if [[ "$INV_OK" == "false" ]]; then
  pass "invalid_op: ok=false"
else
  fail "invalid_op: expected ok=false, got: $INV_RESP"
fi

if [[ -n "$INV_ERR" ]]; then
  pass "invalid_op: error field non-empty ('$INV_ERR')"
else
  fail "invalid_op: error field missing (full: $INV_RESP)"
fi

# ─── TEST 4: get returns a session ────────────────────────────────────────────
# This spawns a real agent — requires the operator's CreateAgentFunc to work.
# On first call with empty queue it creates on demand (may take 30-60s).
echo ""
echo "==> [TEST 4] get returns a session (timeout=60s)"

GET_RESP="$(sock_rpc "$SOCK" '{"op":"get","provider":"claude","workspace":"/agents/e2e-pool-test"}' 60 || true)"
echo "    response: $GET_RESP" | tee -a "$LOG"

GET_OK="$(json_field "$GET_RESP" "ok")"
GET_SID="$(json_field "$GET_RESP" "session_id")"

if [[ -z "$GET_RESP" ]]; then
  skip "get: no response within 60s — provider may need credentials"
elif [[ "$GET_OK" == "true" && -n "$GET_SID" ]]; then
  pass "get: ok=true, session_id=$GET_SID"
elif [[ "$GET_OK" == "false" ]]; then
  ERR="$(json_field "$GET_RESP" "error")"
  skip "get: provider not available — $ERR"
else
  fail "get: unexpected response (ok=true but session_id empty): $GET_RESP"
fi

# ─── TEST 5: two gets return different session_ids ────────────────────────────
echo ""
echo "==> [TEST 5] two sequential gets return different session_ids"

if [[ -z "$GET_SID" ]]; then
  skip "get not available (TEST 4 skipped/failed) — skipping uniqueness check"
else
  SID1="$GET_SID"
  RESP2="$(sock_rpc "$SOCK" '{"op":"get","provider":"claude","workspace":"/agents/e2e-pool-test"}' 60 || true)"
  echo "    response2: $RESP2" | tee -a "$LOG"
  SID2="$(json_field "$RESP2" "session_id")"

  if [[ -z "$SID2" ]]; then
    skip "second get timed out — skipping uniqueness check"
  elif [[ "$SID1" != "$SID2" ]]; then
    pass "two gets returned distinct session_ids ($SID1 vs $SID2)"
  else
    fail "both gets returned the same session_id ($SID1) — pool did not dequeue unique sessions"
  fi
fi

# ─── TEST 6: replenish log appears after register_config with MinReady=1 ──────
# Proves the pool is actually pre-warming, not just accepting the config.
# SKIP if journalctl is not available (e.g. non-systemd environments).
echo ""
echo "==> [TEST 6] pool replenish log appears (MinReady=1)"

if ! command -v journalctl &>/dev/null; then
  skip "journalctl not available — cannot verify replenish log"
else
  # Register a fresh workspace with MinReady=1 to trigger a replenish cycle.
  REG6_RESP="$(sock_rpc "$SOCK" '{"op":"register_config","config":{"provider":"claude","workspace":"/agents/e2e-replenish-check","workspace_min":1,"max_parallel":1}}' 10 || true)"
  echo "    register_config response: $REG6_RESP" | tee -a "$LOG"

  if [[ "$(json_field "$REG6_RESP" "ok")" != "true" ]]; then
    skip "register_config for replenish check failed — skipping log assertion"
  else
    # Poll journalctl for up to 15s for the replenish log line.
    REPLENISH_FOUND=0
    REPLENISH_DEADLINE=$((SECONDS + 15))
    while [[ "$SECONDS" -lt "$REPLENISH_DEADLINE" ]]; do
      if journalctl -u omni --since "1 minute ago" --no-pager -q 2>/dev/null \
          | grep -q "agentpool: replenished"; then
        REPLENISH_FOUND=1
        break
      fi
      sleep 1
    done

    if [[ "$REPLENISH_FOUND" -eq 1 ]]; then
      pass "replenish log found within 15s — pool is pre-warming"
    else
      fail "replenish log not found within 15s — pool may not be pre-warming after register_config"
    fi
  fi
fi

# ─── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP  (run $RUN_ID)"
echo "==> log: $LOG"

if [[ "$FAIL" -gt 0 ]]; then
  echo "==> FAIL"
  exit 1
fi
echo "==> PASS"
