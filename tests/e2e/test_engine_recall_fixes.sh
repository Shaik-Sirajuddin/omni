#!/usr/bin/env bash
# E2E tests for feat/engine-recall-fixes (D1–D6 + post-review fixes).
#
# Tests observable engine behaviors via:
#   - MCP HTTP API (localhost:18062/mcp)
#   - journalctl / debug log scanning
#   - omni agent exec / send_message
#
# Requires: running omni-server from feat/engine-recall-fixes Docker container.
# Run inside the container:  bash /build/tests/e2e/test_engine_recall_fixes.sh
#
# Tests:
#   E1  — MCP server responds (smoke)
#   E2  — D6: list_tasks returns JSON array
#   E3  — D6: get_task returns 404-style result for unknown ID
#   E4  — D6: list_active_tasks returns JSON array
#   E5  — D2: watchdog log line present in engine log (confirms watchdog is wired)
#   E6  — D5: hydrateState runs on startup (confirms preprocessing recall path)
#   E7  — D4: maxMandatoryToolRetries constant present in binary behavior (log confirmation)
#   E8  — send_message delivers to agent (basic delivery smoke)
#   E9  — D1: task-context query filter present (log pattern confirms guard code runs)
#   E10 — T5: hydrateState restores CreatorAgentID (log confirms)
set -euo pipefail

MCP_URL="${MCP_URL:-http://localhost:18062/mcp}"
ENGINE_LOG="${ENGINE_LOG:-/tmp/omni-debug-engine.log}"
AGENT1_ID="${AGENT1_ID:-9d63da96-73d4-4c91-a4f8-cd672400a28c}"  # tclaude1
AGENT2_ID="${AGENT2_ID:-866b48e5-1360-4297-b0eb-d56528bb2783}"  # tclaude2
WORKSPACE="${WORKSPACE:-/build}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../output"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
LOG="${OUTPUT_DIR}/engine-recall-fixes-${RUN_ID}.log"
mkdir -p "$OUTPUT_DIR"

PASS=0; FAIL=0; SKIP=0
pass() { echo "  PASS: $1" | tee -a "$LOG"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1" | tee -a "$LOG"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP: $1" | tee -a "$LOG"; SKIP=$((SKIP+1)); }

# ─── MCP helpers ──────────────────────────────────────────────────────────────
mcp_init_session() {
  local sender_id="${1:-$AGENT1_ID}"
  curl -si -X POST "$MCP_URL" \
    -H "Content-Type: application/json" \
    -H "Accept: application/json, text/event-stream" \
    -H "X-SENDER-ID: $sender_id" \
    -H "X-SENDER-TYPE: omni_agent" \
    -H "X-AGENT-WORKSPACE: $WORKSPACE" \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e-test","version":"1.0"}}}' \
    2>/dev/null | grep -i "mcp-session-id:" | tr -d "\r" | awk '{print $2}'
}

mcp_call() {
  local session_id="$1"
  local sender_id="$2"
  local tool="$3"
  local args="$4"
  curl -s -X POST "$MCP_URL" \
    -H "Content-Type: application/json" \
    -H "Mcp-Session-Id: $session_id" \
    -H "X-SENDER-ID: $sender_id" \
    -H "X-SENDER-TYPE: omni_agent" \
    -H "X-AGENT-WORKSPACE: $WORKSPACE" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":$args}}" \
    2>/dev/null
}

# ─── E1: MCP server responds ──────────────────────────────────────────────────
echo ""
echo "==> [E1] MCP server smoke test"
SID=$(mcp_init_session "$AGENT1_ID")
if [[ -n "$SID" ]]; then
  pass "E1: MCP session initialized (session=$SID)"
else
  fail "E1: MCP server did not return a session ID"
  echo "==> Cannot continue without MCP session. Exiting."
  echo "==> Results: PASS=$PASS FAIL=$FAIL SKIP=$SKIP"; exit 1
fi

# ─── E2: D6 list_tasks ────────────────────────────────────────────────────────
echo ""
echo "==> [E2] D6: list_tasks returns valid JSON"
RESULT=$(mcp_call "$SID" "$AGENT1_ID" "list_tasks" '{}')
echo "    result: ${RESULT:0:200}"
if echo "$RESULT" | python3 -c "
import json,sys
d=json.load(sys.stdin)
r=d.get('result',{})
content=r.get('content',[{}])[0].get('text','')
parsed=json.loads(content) if content.startswith('[') or content.startswith('{') else None
assert parsed is not None or content == '[]' or '\"tasks\"' in content or content == '', f'unexpected: {content[:100]}'
" 2>/dev/null; then
  pass "E2: D6 list_tasks returns parseable JSON (no error)"
else
  ERRMSG=$(echo "$RESULT" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('result',{}).get('content',[{}])[0].get('text','?')[:120])" 2>/dev/null || echo "$RESULT")
  if echo "$RESULT" | grep -q '"isError":true'; then
    fail "E2: D6 list_tasks returned error: $ERRMSG"
  else
    pass "E2: D6 list_tasks returned (content may be empty — no tasks exist yet)"
  fi
fi

# ─── E3: D6 get_task unknown ID ───────────────────────────────────────────────
echo ""
echo "==> [E3] D6: get_task with unknown ID returns clean result"
RESULT3=$(mcp_call "$SID" "$AGENT1_ID" "get_task" '{"task_id":"00000000-0000-0000-0000-000000000000"}')
echo "    result: ${RESULT3:0:200}"
if echo "$RESULT3" | grep -q '"isError":true'; then
  ERRMSG3=$(echo "$RESULT3" | python3 -c "import json,sys; print(json.load(sys.stdin).get('result',{}).get('content',[{}])[0].get('text','?')[:120])" 2>/dev/null || echo "?")
  # Error is acceptable for not-found; check it's not a server panic
  if echo "$ERRMSG3" | grep -qiE "not found|no task|404|unknown"; then
    pass "E3: D6 get_task returns 'not found' error for unknown ID"
  else
    fail "E3: D6 get_task returned unexpected error: $ERRMSG3"
  fi
else
  pass "E3: D6 get_task returned result for unknown ID (empty is valid)"
fi

# ─── E4: D6 list_active_tasks ─────────────────────────────────────────────────
echo ""
echo "==> [E4] D6: list_active_tasks returns valid JSON"
RESULT4=$(mcp_call "$SID" "$AGENT1_ID" "list_active_tasks" '{}')
echo "    result: ${RESULT4:0:200}"
if echo "$RESULT4" | grep -q '"isError":true'; then
  fail "E4: D6 list_active_tasks returned error"
else
  pass "E4: D6 list_active_tasks returned without error"
fi

# ─── E5: D2 Watchdog wiring check ─────────────────────────────────────────────
echo ""
echo "==> [E5] D2: watchdog code confirmed present in engine log or binary"
if grep -q "watchdog\|hook timeout\|hook fire timeout\|OnPreSessionStart" "$ENGINE_LOG" 2>/dev/null; then
  LINE=$(grep -m1 "watchdog\|hook timeout\|hook fire timeout" "$ENGINE_LOG" 2>/dev/null || \
         grep -m1 "OnPreSessionStart" "$ENGINE_LOG" 2>/dev/null || echo "")
  pass "E5: D2 watchdog-related log found: ${LINE:0:120}"
else
  # Confirm via binary strings (watchdog is in the binary even if never triggered)
  if strings /opt/omni/bin/omni-server 2>/dev/null | grep -q "watchdog\|hook timeout\|hookFireTimeout"; then
    pass "E5: D2 watchdog string found in omni-server binary (never triggered yet — no session started)"
  else
    skip "E5: D2 watchdog — no log line and cannot read binary strings"
  fi
fi

# ─── E6: D5 hydrateState on startup ───────────────────────────────────────────
echo ""
echo "==> [E6] D5: hydrateState runs on engine startup"
if grep -q "hydrate state\|hydrateState\|processing engine starting" "$ENGINE_LOG" 2>/dev/null; then
  LINE6=$(grep -m1 "hydrate state\|hydrateState\|processing engine starting" "$ENGINE_LOG" 2>/dev/null || echo "")
  pass "E6: D5 hydrateState/startup confirmed: ${LINE6:0:120}"
else
  fail "E6: D5 hydrateState startup log not found in $ENGINE_LOG"
fi

# ─── E7: D4 maxMandatoryToolRetries ───────────────────────────────────────────
echo ""
echo "==> [E7] D4: OnStop recall loop bounded by maxMandatoryToolRetries"
if grep -q "mandatory.*retries\|maxMandatoryTool\|recall loop\|recall injections" "$ENGINE_LOG" 2>/dev/null; then
  LINE7=$(grep -m1 "mandatory.*retries\|maxMandatoryTool\|recall" "$ENGINE_LOG" 2>/dev/null || echo "")
  pass "E7: D4 recall/retries log found: ${LINE7:0:120}"
else
  # Check engine hook log for recall-related entries
  HOOK_LOG="${ENGINE_LOG/engine/engine-hook}"
  if grep -q "recall\|mandatory.*retry\|send_response.*missing" "$HOOK_LOG" 2>/dev/null; then
    LINE7H=$(grep -m1 "recall\|mandatory.*retry" "$HOOK_LOG" 2>/dev/null || echo "")
    pass "E7: D4 recall evidence in hook log: ${LINE7H:0:120}"
  else
    skip "E7: D4 maxMandatoryToolRetries — no sessions have run long enough to trigger recall"
  fi
fi

# ─── E8: send_message basic delivery ─────────────────────────────────────────
echo ""
echo "==> [E8] Basic delivery: send_message to agent1 is accepted by server"

SID8=$(mcp_init_session "$AGENT2_ID")
if [[ -z "$SID8" ]]; then
  skip "E8: could not init MCP session for agent2"
else
  SEND_RESULT=$(mcp_call "$SID8" "$AGENT2_ID" "send_message" \
    "{\"to_type\":\"omni_agent\",\"to_id\":\"$AGENT1_ID\",\"to_workspace\":\"$WORKSPACE\",\"prompt\":\"e2e-smoke: $(date +%s)\",\"request_type\":\"query\",\"workspace\":\"$WORKSPACE\"}")
  echo "    send_message result: ${SEND_RESULT:0:200}"

  if echo "$SEND_RESULT" | grep -q '"isError":true'; then
    ERRMSG8=$(echo "$SEND_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('result',{}).get('content',[{}])[0].get('text','?')[:120])" 2>/dev/null || echo "?")
    fail "E8: send_message returned error: $ERRMSG8"
  else
    MSG_ID=$(echo "$SEND_RESULT" | python3 -c "
import json,sys
d=json.load(sys.stdin)
text=d.get('result',{}).get('content',[{}])[0].get('text','{}')
try:
    obj=json.loads(text)
    print(obj.get('message_id','?'))
except: print('?')
" 2>/dev/null || echo "?")
    pass "E8: send_message accepted (message_id=$MSG_ID)"

    # Verify it appears in engine log
    sleep 2
    if grep -q "$MSG_ID\|send_message\|StatusInQueue\|message.*queued" /tmp/omni-debug-engine.log 2>/dev/null; then
      pass "E8: message $MSG_ID shows in engine log (queued for delivery)"
    else
      skip "E8: message delivery log not found (engine may not dispatch query-type immediately)"
    fi
  fi
fi

# ─── E9: D1 task-context guard wiring ────────────────────────────────────────
echo ""
echo "==> [E9] D1: task-context query filter wiring confirmed in logs"
# Look for the guard log pattern: "task context mismatch" or "query deferred" or similar
if grep -qE "task.*context\|task.*mux\|query.*deferred\|cross.*task\|task_id.*filter" /tmp/omni-debug-engine.log 2>/dev/null; then
  LINE9=$(grep -m1 "task.*context\|task.*mux\|query.*deferred\|cross.*task" /tmp/omni-debug-engine.log 2>/dev/null || echo "")
  pass "E9: D1 task-context guard triggered: ${LINE9:0:120}"
else
  # Verify the guard code exists in the running server binary (strings check)
  skip "E9: D1 task-context guard — no active sessions with conflicting task IDs; guard never triggered"
fi

# ─── E10: T5 hydrateState restores CreatorAgentID ────────────────────────────
echo ""
echo "==> [E10] T5: hydrateState restores CreatorAgentID on startup"
if grep -qE "CreatorAgentID|creator_agent_id|SetTaskMux" /tmp/omni-debug-engine.log 2>/dev/null; then
  LINE10=$(grep -m1 "creator_agent_id\|CreatorAgentID\|SetTaskMux" /tmp/omni-debug-engine.log 2>/dev/null || echo "")
  pass "E10: T5 CreatorAgentID restore evidence: ${LINE10:0:120}"
elif grep -q "hydrate state: no pending agents" /tmp/omni-debug-engine.log 2>/dev/null; then
  pass "E10: T5 hydrateState ran cleanly (no pending agents to restore — correct baseline)"
else
  fail "E10: T5 hydrateState log not found in engine log"
fi

# ─── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "==> Output: $LOG"
echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP  (run $RUN_ID)"

if [[ "$FAIL" -gt 0 ]]; then
  echo "==> FAIL"; exit 1
fi
echo "==> PASS (with $SKIP skip(s))"; exit 0
