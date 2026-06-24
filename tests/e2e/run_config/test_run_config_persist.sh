#!/usr/bin/env bash
# E2E-1: RunConfig persists across init → resume (DB assertions only, no live provider)
#
# Verifies:
#   - init with --arg / --env writes run_config JSON to agent_settings table
#   - resume with --env KEY=new upserts the key and persists the merged result
#
# Usage:
#   OMNI=/path/to/omni OMNI_DB=/path/to/omni.db bash test_run_config_persist.sh
#
# Env overrides:
#   OMNI        path to omni binary             (default: omni on $PATH)
#   OMNI_DB     path to SQLite database file    (default: ~/.omni/omni.db)
#   WORKSPACE   workspace directory             (default: temp dir)
#   PROVIDER    provider to use for init         (default: claude)

set -euo pipefail

OMNI="${OMNI:-omni}"
OMNI_DB="${OMNI_DB:-${HOME}/.omni/omni.db}"
PROVIDER="${PROVIDER:-claude}"
WORKSPACE="${WORKSPACE:-$(mktemp -d)}"
AGENT_NAME="e2e-rc-persist-$$"

PASS=0
FAIL=0

tap_ok()   { echo "ok - $1"; PASS=$((PASS+1)); }
tap_fail() { echo "not ok - $1"; FAIL=$((FAIL+1)); }
tap_skip() { echo "ok - $1 # SKIP $2"; }
tap_done() {
  echo "1..$((PASS+FAIL))"
  if [ "$FAIL" -gt 0 ]; then exit 1; fi
}

# ── pre-flight ────────────────────────────────────────────────────────────────
if ! command -v sqlite3 &>/dev/null; then
  tap_skip "sqlite3 not available" "sqlite3 binary required"; tap_done; exit 0
fi
if ! command -v "$OMNI" &>/dev/null 2>&1 && ! "$OMNI" --version &>/dev/null 2>&1; then
  tap_skip "omni binary not available" "set OMNI="; tap_done; exit 0
fi
if [ ! -f "$OMNI_DB" ]; then
  tap_skip "omni DB not found at $OMNI_DB" "set OMNI_DB="; tap_done; exit 0
fi

cleanup() { "$OMNI" agent delete "$AGENT_NAME" --workspace "$WORKSPACE" &>/dev/null || true; }
trap cleanup EXIT

# ── T1: init persists run_config ──────────────────────────────────────────────
"$OMNI" agent init "$AGENT_NAME" \
  --workspace "$WORKSPACE" \
  --provider "$PROVIDER" \
  --interactive=false \
  --arg=--output-format=json \
  --arg=--max-turns=1 \
  --env=MY_VAR=hello

AGENT_ID=$(sqlite3 "$OMNI_DB" \
  "SELECT id FROM agents WHERE name='$AGENT_NAME' AND workspace_dir='$WORKSPACE';" 2>/dev/null)

if [ -z "$AGENT_ID" ]; then
  tap_fail "T1: agent row found in DB"; tap_done; exit 1
fi
tap_ok "T1: agent row found in DB ($AGENT_ID)"

RUN_CFG=$(sqlite3 "$OMNI_DB" \
  "SELECT run_config FROM agent_settings WHERE agent_id='$AGENT_ID';" 2>/dev/null)

if echo "$RUN_CFG" | grep -q '"--output-format=json"'; then
  tap_ok "T1: extra arg --output-format=json stored"
else
  tap_fail "T1: extra arg --output-format=json stored (got: $RUN_CFG)"
fi

if echo "$RUN_CFG" | grep -q '"MY_VAR"'; then
  tap_ok "T1: env key MY_VAR stored"
else
  tap_fail "T1: env key MY_VAR stored (got: $RUN_CFG)"
fi

if echo "$RUN_CFG" | grep -q '"hello"'; then
  tap_ok "T1: env value hello stored"
else
  tap_fail "T1: env value hello stored (got: $RUN_CFG)"
fi

# ── T2: resume upserts env, persists merged result ────────────────────────────
"$OMNI" agent resume "$AGENT_NAME" \
  --workspace "$WORKSPACE" \
  --env=MY_VAR=world \
  --detach &>/dev/null || true   # detached; may error if provider not authed — that's OK

RUN_CFG_AFTER=$(sqlite3 "$OMNI_DB" \
  "SELECT run_config FROM agent_settings WHERE agent_id='$AGENT_ID';" 2>/dev/null)

if echo "$RUN_CFG_AFTER" | grep -q '"world"'; then
  tap_ok "T2: env value updated to world after resume"
else
  tap_fail "T2: env value updated to world after resume (got: $RUN_CFG_AFTER)"
fi

# args should still be present (not wiped)
if echo "$RUN_CFG_AFTER" | grep -q '"--output-format=json"'; then
  tap_ok "T2: extra args preserved after env-only resume"
else
  tap_fail "T2: extra args preserved after env-only resume (got: $RUN_CFG_AFTER)"
fi

tap_done
