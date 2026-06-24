#!/usr/bin/env bash
# E2E-3: --clear-args replaces stored ExtraArgs list (DB assertions only)
#
# Verifies:
#   - init stores ["--old-flag"]
#   - resume with --clear-args --arg=--new-flag results in ["--new-flag"] only
#
# Usage:
#   OMNI=/path/to/omni OMNI_DB=/path/to/omni.db bash test_run_config_clear_args.sh

set -euo pipefail

OMNI="${OMNI:-omni}"
OMNI_DB="${OMNI_DB:-${HOME}/.omni/omni.db}"
PROVIDER="${PROVIDER:-claude}"
WORKSPACE="${WORKSPACE:-$(mktemp -d)}"
AGENT_NAME="e2e-rc-clrargs-$$"

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

# ── T1: init with --old-flag ──────────────────────────────────────────────────
"$OMNI" agent init "$AGENT_NAME" \
  --workspace "$WORKSPACE" \
  --provider "$PROVIDER" \
  --interactive=false \
  --arg=--old-flag

AGENT_ID=$(sqlite3 "$OMNI_DB" \
  "SELECT id FROM agents WHERE name='$AGENT_NAME' AND workspace_dir='$WORKSPACE';" 2>/dev/null)

RUN_CFG=$(sqlite3 "$OMNI_DB" \
  "SELECT run_config FROM agent_settings WHERE agent_id='$AGENT_ID';" 2>/dev/null)

if echo "$RUN_CFG" | grep -q '"--old-flag"'; then
  tap_ok "T1: --old-flag stored after init"
else
  tap_fail "T1: --old-flag stored after init (got: $RUN_CFG)"
fi

# ── T2: resume with --clear-args --arg=--new-flag ─────────────────────────────
"$OMNI" agent resume "$AGENT_NAME" \
  --workspace "$WORKSPACE" \
  --clear-args \
  --arg=--new-flag \
  --detach &>/dev/null || true

RUN_CFG_AFTER=$(sqlite3 "$OMNI_DB" \
  "SELECT run_config FROM agent_settings WHERE agent_id='$AGENT_ID';" 2>/dev/null)

if echo "$RUN_CFG_AFTER" | grep -q '"--new-flag"'; then
  tap_ok "T2: --new-flag present after clear-args resume"
else
  tap_fail "T2: --new-flag present after clear-args resume (got: $RUN_CFG_AFTER)"
fi

if echo "$RUN_CFG_AFTER" | grep -q '"--old-flag"'; then
  tap_fail "T2: --old-flag absent after clear-args (still present: $RUN_CFG_AFTER)"
else
  tap_ok "T2: --old-flag absent after clear-args"
fi

tap_done
