#!/usr/bin/env bash
# E2E test: start 2 agents, send a prompt, verify delivery + hooks + reply via logs.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/../output"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
LOG="${OUTPUT_DIR}/${RUN_ID}.log"

mkdir -p "$OUTPUT_DIR"

# Step 2: tail omni service logs into output file
journalctl -fu omni@root > "$LOG" 2>&1 &
LOG_PID=$!
echo "==> log tap started (pid=$LOG_PID) → $LOG"

cleanup() {
  kill "$LOG_PID" 2>/dev/null || true
}
trap cleanup EXIT

# Step 3: resume agent 1
echo "==> resuming test-claude-1"
omni agent resume test-claude-1 &

# Step 4: resume agent 2
echo "==> resuming tests-claude-2"
omni agent resume tests-claude-2 &

sleep 5

# Step 5: send predefined prompt to agent 1
echo "==> sending prompt to test-claude-1"
omni agent exec test-claude-1 --prompt "what is the time" --bg

# Step 6: wait for propagation
echo "==> waiting for propagation..."
sleep 20

# Step 7: stop log tap
kill "$LOG_PID" 2>/dev/null || true
trap - EXIT
echo "==> log tap stopped"

echo "==> run log saved: $LOG"
echo ""

# Step 8: check for errors
echo "==> [check] no error blocks"
if grep -i "^.*error" "$LOG"; then
  echo "FAIL: error blocks found in output"
  exit 1
fi

# Step 9: verify delivery
echo "==> [check] delivery"
grep -i "send_message\|deliver" "$LOG" || { echo "FAIL: no delivery log found"; exit 1; }

# Step 10: verify hook callbacks
echo "==> [check] hook callbacks"
grep -i "PreToolUse\|PostToolUse" "$LOG" || { echo "FAIL: no hook callback log found"; exit 1; }

# Step 11: verify reply
echo "==> [check] reply from agent 2"
grep -i "reply\|response" "$LOG" || { echo "FAIL: no reply log found"; exit 1; }

echo ""
echo "==> PASS (run $RUN_ID)"
