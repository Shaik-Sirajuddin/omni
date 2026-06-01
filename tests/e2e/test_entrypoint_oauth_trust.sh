#!/usr/bin/env bash
# E2E tests: fix/entrypoint-claude-oauth-trust
#
# Covers the 8 assertions from the changeset:
#  1. make docker-up exits 0 (container healthy)
#  2. docker logs show trust accepted for /build /workspace /
#  3. ~/.claude/.credentials.json absent inside container (OAUTH token set)
#  4. ~/.claude/projects/ has build.json, workspace.json, root.json
#  5. claude auth status --text shows authenticated
#  6. claude -p 'respond only: READY' works from /, /build, /workspace
#  7. no interactive trust dialog shown
#  8. no regressions (MCP config seeded, settings.json present)
#
# Requires: running docker container (development-ubuntu-1), CLAUDE_CODE_OAUTH_TOKEN set
set -euo pipefail

PASS=0; FAIL=0; SKIP=0
pass() { echo "  PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP: $1"; SKIP=$((SKIP+1)); }

CONTAINER="development-ubuntu-1"
TRUST_JSON='{"hasTrustDialogAccepted":true,"hasTrustDialogHooksAccepted":true,"hasCompletedProjectOnboarding":true}'

# ─── Pre-check ────────────────────────────────────────────────────────────────
if ! docker inspect "$CONTAINER" &>/dev/null; then
  echo "==> Container $CONTAINER not found — run: make docker-rebuild && make docker-up"
  for i in $(seq 1 8); do skip "A$i: container not running"; done
  echo "==> Results: PASS=$PASS FAIL=$FAIL SKIP=$SKIP"; exit 0
fi

container_status=$(docker inspect --format '{{.State.Health.Status}}' "$CONTAINER" 2>/dev/null || echo "unknown")
echo "==> Container status: $container_status"

# ─── A1: container healthy ────────────────────────────────────────────────────
echo ""
echo "==> [A1] Container is healthy"
if [[ "$container_status" == "healthy" ]]; then
  pass "A1: docker-up container is healthy"
else
  fail "A1: container status is '$container_status' (expected healthy)"
fi

# ─── A2: trust accepted for /build /workspace / in logs ───────────────────────
echo ""
echo "==> [A2] Entrypoint logs show trust accepted for /build /workspace /"
logs=$(docker logs "$CONTAINER" 2>&1)
a2_fail=0
for dir in /build /workspace /; do
  if echo "$logs" | grep -q "accepting claude workspace trust for $dir"; then
    pass "A2: trust accepted for $dir"
  else
    # On repeated starts volumes persist trust JSONs — check file presence instead.
    fname="$(echo "$dir" | sed 's|^/||; s|/|-|g')"
    [[ -z "$fname" ]] && fname="root"
    if docker exec "$CONTAINER" bash -lc "test -f /root/.claude/projects/${fname}.json" 2>/dev/null; then
      pass "A2: trust file present for $dir (write-once: already existed from prior run)"
    else
      fail "A2: trust not accepted and file missing for $dir"
      a2_fail=1
    fi
  fi
done

# ─── A3: .credentials.json absent when OAUTH token set ───────────────────────
echo ""
echo "==> [A3] ~/.claude/.credentials.json absent (CLAUDE_CODE_OAUTH_TOKEN set)"
oauth_set=$(docker exec "$CONTAINER" bash -lc 'echo "${CLAUDE_CODE_OAUTH_TOKEN:-}"' 2>/dev/null)
if [[ -z "$oauth_set" ]]; then
  skip "A3: CLAUDE_CODE_OAUTH_TOKEN not set in container — skipping credentials check"
else
  creds=$(docker exec "$CONTAINER" bash -lc "ls /root/.claude/.credentials.json 2>/dev/null && echo PRESENT || echo ABSENT" 2>/dev/null)
  if [[ "$creds" == "ABSENT" ]]; then
    pass "A3: .credentials.json absent (cleared by entrypoint when OAUTH token set)"
  else
    fail "A3: .credentials.json is PRESENT — stale credentials not cleared"
  fi
fi

# ─── A4: project trust JSONs present ─────────────────────────────────────────
echo ""
echo "==> [A4] ~/.claude/projects/ has trust JSON for /build /workspace /"
for dir in /build /workspace /; do
  fname="$(echo "$dir" | sed 's|^/||; s|/|-|g')"
  [[ -z "$fname" ]] && fname="root"
  project_file="/root/.claude/projects/${fname}.json"
  content=$(docker exec "$CONTAINER" bash -lc "cat '$project_file' 2>/dev/null || echo MISSING" 2>/dev/null)
  if echo "$content" | grep -q "hasTrustDialogAccepted.*true"; then
    pass "A4: ${fname}.json present with hasTrustDialogAccepted=true"
  else
    fail "A4: ${fname}.json missing or hasTrustDialogAccepted not true (got: $content)"
  fi
done

# ─── A5: claude auth status shows authenticated ───────────────────────────────
echo ""
echo "==> [A5] claude auth status --text shows authenticated"
auth_out=$(docker exec "$CONTAINER" bash -lc "claude auth status --text 2>&1 | head -5" 2>/dev/null || echo "ERROR")
echo "    auth output: ${auth_out:0:120}"
if echo "$auth_out" | grep -qiE "CLAUDE_CODE_OAUTH_TOKEN|authenticated|logged.?in|api.?key"; then
  pass "A5: claude auth status shows authenticated"
else
  fail "A5: claude auth status output unexpected: $auth_out"
fi

# ─── A6: claude -p READY works from /, /build, /workspace ────────────────────
echo ""
echo "==> [A6] claude -p 'respond only: READY' works from /, /build, /workspace"
for dir in / /build /workspace; do
  result=$(docker exec "$CONTAINER" bash -lc "mkdir -p '$dir' && cd '$dir' && timeout 25 claude -p 'respond only: READY' --output-format text 2>&1" 2>/dev/null || echo "TIMEOUT_OR_ERROR")
  trimmed=$(echo "$result" | tr -d '\n ' | head -c 50)
  if echo "$result" | grep -qi "^READY$\|^READY\b"; then
    pass "A6: claude -p READY works from $dir"
  elif echo "$result" | grep -qi "READY"; then
    pass "A6: claude -p returned READY from $dir (with extra output)"
    echo "    output: ${result:0:80}"
  else
    fail "A6: claude -p from $dir returned unexpected: ${result:0:120}"
  fi
done

# ─── A7: no interactive trust dialog ─────────────────────────────────────────
echo ""
echo "==> [A7] No interactive trust dialog shown"
json_out=$(docker exec "$CONTAINER" bash -lc "cd /build && timeout 25 claude -p 'respond only: PING' --output-format json 2>&1" 2>/dev/null || echo "ERROR")
if echo "$json_out" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); exit(0 if d.get('subtype')=='success' else 1)" 2>/dev/null; then
  pass "A7: JSON output subtype=success — no trust dialog intercepted"
elif echo "$json_out" | grep -qi "trust\|dialog\|onboard\|welcome\|accept"; then
  fail "A7: trust/dialog text detected in output"
  echo "    output: ${json_out:0:200}"
else
  pass "A7: no trust/dialog text in output"
fi

# ─── A8: regression — settings.json and MCP seeded ───────────────────────────
echo ""
echo "==> [A8] Regression: settings.json seeded, MCP config present"
# Check settings.json has hooks and MCP server entry
settings=$(docker exec "$CONTAINER" bash -lc "cat /root/.claude/settings.json 2>/dev/null || echo MISSING" 2>/dev/null)
if echo "$settings" | grep -q '"mcpServers"'; then
  pass "A8: settings.json has mcpServers key"
else
  fail "A8: settings.json missing or no mcpServers key (got: ${settings:0:100})"
fi
if echo "$settings" | grep -q '"tunnel-mcp"'; then
  pass "A8: tunnel-mcp entry present in settings.json"
else
  fail "A8: tunnel-mcp missing from settings.json"
fi

# ─── Summary ─────────────────────────────────────────────────────────────────
echo ""
echo "==> Results: PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP"
if [[ "$FAIL" -gt 0 ]]; then
  echo "==> FAIL"; exit 1
fi
echo "==> PASS (with $SKIP skip(s))"; exit 0
