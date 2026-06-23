#!/usr/bin/env bash
# E2E-5: resume with no --arg/--env is a no-op (run_config unchanged in DB)
#
# Verifies:
#   - init stores a RunConfig
#   - plain resume (no override flags) does not modify the stored run_config
#
# Usage:
#   OMNI=/path/to/omni OMNI_DB=/path/to/omni.db bash test_run_config_noop_resume.sh

set -euo pipefail

OMNI="${OMNI:-omni}"
OMNI_DB="${OMNI_DB:-${HOME}/.omni/omni.db}"
PROVIDER="${PROVIDER:-claude}"
WORKSPACE="${WORKSPACE:-$(mktemp -d)}"
AGENT_NAME="e2e-rc-noop-$$"

PASS=0
FAIL=0

tap_ok()   { echo "ok - $1"; PASS=$((PASS+1)); }
tap_fail() { echo "not ok - $1"; FAIL=$((FAIL+1)); }
tap_skip() { echo "ok - $1 # SKIP $2"; }
tap_done() {
  echo "1..$((PASS+FAIL))"
  if [ "$FAIL" -gt 0 ]; then exit 1; fi
}

if ! command -v sqlite3 &>/dev/null; then
  tap_skip "sqlite3 not available" "sqlite3 binary required"; tap_done; exit 0
fi
if ! "$OMNI" --version &>/dev/null 2>&1; then
  tap_skip "omni binary not available" "set OMNI="; tap_done; exit 0
fi
if [ ! -f "$OMNI_DB" ]; then
  tap_skip "omni DB not found at $OMNI_DB" "set OMNI_DB="; tap_done; exit 0
fi

cleanup() { "$OMNI" agent delete "$AGENT_NAME" --workspace "$WORKSPACE" &>/dev/null || true; }
trap cleanup EXIT

# ── T1: init with args + envs ─────────────────────────────────────────────────
"$OMNI" agent init "$AGENT_NAME" \
  --workspace "$WORKSPACE" \
  --provider "$PROVIDER" \
  --interactive=false \
  --arg=--stable-flag \
  --env=STABLE=1

AGENT_ID=$(sqlite3 "$OMNI_DB" \
  "SELECT id FROM agents WHERE name='$AGENT_NAME' AND workspace_dir='$WORKSPACE';" 2>/dev/null)

BEFORE=$(sqlite3 "$OMNI_DB" \
  "SELECT run_config FROM agent_settings WHERE agent_id='$AGENT_ID';" 2>/dev/null)

if echo "$BEFORE" | grep -q '"--stable-flag"'; then
  tap_ok "T1: initial run_config stored"
else
  tap_fail "T1: initial run_config stored (got: $BEFORE)"; tap_done; exit 1
fi

# ── T2: plain resume — no overrides ───────────────────────────────────────────
"$OMNI" agent resume "$AGENT_NAME" \
  --workspace "$WORKSPACE" \
  --detach &>/dev/null || true

AFTER=$(sqlite3 "$OMNI_DB" \
  "SELECT run_config FROM agent_settings WHERE agent_id='$AGENT_ID';" 2>/dev/null)

if [ "$BEFORE" = "$AFTER" ]; then
  tap_ok "T2: run_config unchanged after no-override resume"
else
  tap_fail "T2: run_config unchanged after no-override resume (before=$BEFORE after=$AFTER)"
fi

tap_done
