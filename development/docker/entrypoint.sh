#!/usr/bin/env bash
# entrypoint.sh — install pre-built omni, write configs, then hand off to systemd PID 1
set -euo pipefail

WORKSPACE="${WORKSPACE:-/workspace}"

# ── install + setup omni (binary already baked into image at /opt/omni/bin) ───
fix_claude_binary() {
  local native="/usr/lib/node_modules/@anthropic-ai/claude-code/node_modules/@anthropic-ai/claude-code-linux-x64/claude"
  if [[ -f "$native" ]]; then
    ln -sf "$native" /usr/bin/claude
  fi
}
fix_claude_binary

install_and_setup() {
  echo "==> install_phase"
  # shellcheck source=deployment/setup.sh
  # system-wide mode: user@0 nested systemd fails in containers due to inotify limits
  OMNI_GLOBAL_INSTALL=1 source "$WORKSPACE/deployment/setup.sh"
  # binaries are already at /opt/omni/bin from image build — skip install_binaries
  # OMNI_GLOBAL_INSTALL=1 → SYMLINK_DIR=/usr/local/bin, which is in default PATH
  # so bare "omni hook ..." in claude/codex hook commands resolves correctly
  link_binaries
  write_service

  # enable service so systemd starts it on boot
  mkdir -p /etc/systemd/system/multi-user.target.wants
  ln -sf /etc/systemd/system/omni@.service \
         /etc/systemd/system/multi-user.target.wants/omni@root.service
}

install_and_setup

# ── resolve auth: pick OAuth token OR API key per provider ───────────────────
resolve_auth() {
  # claude: OAuth token takes precedence over API key
  if [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
    export CLAUDE_CODE_OAUTH_TOKEN
    unset ANTHROPIC_API_KEY 2>/dev/null || true
  fi
  # codex: map subscription OAuth token to OPENAI_API_KEY if no API key set
  if [[ -z "${OPENAI_API_KEY:-}" && -n "${OPENAI_OAUTH_TOKEN:-}" ]]; then
    export OPENAI_API_KEY="$OPENAI_OAUTH_TOKEN"
  fi
}
resolve_auth

# ── export runtime socket paths for all child processes (hooks, CLIs) ─────────
write_runtime_env() {
  cat > /etc/profile.d/omni-sockets.sh <<'EOF'
export HOOK_OPERATOR_SOCKET=/run/omni-root/hook-operator.sock
export OMNI_PTY_SOCKET=/run/omni-root/omni-pty.sock
EOF
  grep -q HOOK_OPERATOR_SOCKET /root/.bashrc 2>/dev/null || \
    echo 'source /etc/profile.d/omni-sockets.sh' >> /root/.bashrc
}
write_runtime_env

# ── systemd drop-in: enable HTTP transport for dev ────────────────────────────
write_mcp_dropin() {
  mkdir -p /etc/systemd/system/omni@root.service.d
  local conf="/etc/systemd/system/omni@root.service.d/dev-mcp.conf"
  printf '[Service]\n' > "$conf"
  if [[ -n "${AXO_LINK_SERVICE_HTTP_BIND:-}" ]]; then
    printf 'Environment=AXO_LINK_SERVICE_HTTP_BIND=%s\n' "$AXO_LINK_SERVICE_HTTP_BIND" >> "$conf"
  fi
  if [[ -n "${AXO_LINK_SERVICE_UNIX_SOCKET:-}" ]]; then
    printf 'Environment=AXO_LINK_SERVICE_UNIX_SOCKET=%s\n' "$AXO_LINK_SERVICE_UNIX_SOCKET" >> "$conf"
  fi
  if [[ -n "${AXO_LINK_SERVICE_ADDR:-}" ]]; then
    printf 'Environment=AXO_LINK_SERVICE_ADDR=%s\n' "$AXO_LINK_SERVICE_ADDR" >> "$conf"
  fi
  if [[ -n "${AXO_LINK_MCP_ADDR:-}" ]]; then
    printf 'Environment=AXO_LINK_MCP_ADDR=%s\n' "$AXO_LINK_MCP_ADDR" >> "$conf"
  fi
  # agent auth — pass whichever vars are set so omni-server inherits them
  for var in ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN \
             OPENAI_API_KEY \
             GEMINI_API_KEY GOOGLE_API_KEY GOOGLE_CLOUD_PROJECT \
             ANTHROPIC_MODEL CODEX_MODEL GEMINI_MODEL AGY_MODEL; do
    if [[ -n "${!var:-}" ]]; then
      printf 'Environment=%s=%s\n' "$var" "${!var}" >> "$conf"
    fi
  done
}
write_mcp_dropin

# ── shared: seed a ~/.XYZ.json MCP config (claude / agy share this format) ───
# Usage: seed_mcp_json <path> <url> [api_key_suffix]
seed_mcp_json() {
  local path="$1" url="$2" api_key_suffix="${3:-}"
  [[ -f "$path" ]] && return 0
  echo "==> seeding $path"
  mkdir -p "$(dirname "$path")"
  cat > "$path" <<EOF
{
  "mcpServers": {
    "tunnel-mcp": {
      "type": "http",
      "url": "${url}",
      "headers": {
        "Authorization": "Bearer \${AXO_LINK_MCP_AUTH_TOKEN}",
        "X-Sender-ID": "\${AXO_LINK_MCP_SENDER_ID}",
        "X-Sender-Type": "\${AXO_LINK_MCP_SENDER_TYPE}",
        "X-Agent-Workspace": "\${AXO_LINK_MCP_AGENT_WORKSPACE}"
      }
    }
  },
  "customApiKeyResponses": {
    "approved": ["${api_key_suffix}"],
    "rejected": []
  },
  "hasCompletedOnboarding": true,
  "hasTrustDialogHooksAccepted": true,
  "shiftEnterKeyBindingInstalled": true,
  "theme": "dark"
}
EOF
}

# ── shared: seed a settings.json with omni hooks (claude / agy share this schema) ──
# Usage: seed_hooks_settings <path> <stale_key_check>
seed_hooks_settings() {
  local path="$1"
  local needs_seed=0
  [[ ! -f "$path" ]] && needs_seed=1
  if [[ $needs_seed -eq 0 ]] && python3 -c "
import json,sys
d=json.load(open('$path'))
stale={'PreSessionStart','PostSessionStart'}
sys.exit(0 if stale & set(d.get('hooks',{})) else 1)
" 2>/dev/null; then
    echo "==> rewriting $path (stale hook keys detected)"
    needs_seed=1
  fi
  [[ $needs_seed -eq 0 ]] && return 0
  echo "==> seeding $path"
  mkdir -p "$(dirname "$path")"
  cat > "$path" <<'EOF'
{
  "hooks": {
    "PostToolUse": [{"hooks": [{"type": "command","command": "omni hook --event PostToolUse"}]}],
    "PostToolUseFailure": [{"hooks": [{"type": "command","command": "omni hook --event PostToolUseFailure"}]}],
    "PreToolUse": [{"hooks": [{"type": "command","command": "omni hook --event PreToolUse"}]}],
    "SessionStart": [
      {"hooks": [{"type": "command","command": "omni hook --event PreSessionStart"}]},
      {"hooks": [{"type": "command","command": "omni hook --event PostSessionStart"}]}
    ],
    "Stop": [{"hooks": [{"type": "command","command": "omni hook --event Stop"}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command","command": "omni hook --event UserPromptSubmit"}]}]
  },
  "permissions": {
    "allow": ["mcp__tunnel-mcp__*"]
  },
  "theme": "dark"
}
EOF
}

# ── seed MCP configs into volumes (write-if-absent) ──────────────────────────
seed_mcp_configs() {
  local url="http://127.0.0.1:18062/mcp"

  # ── claude ──────────────────────────────────────────────────────────────────
  seed_hooks_settings /root/.claude/settings.json
  local claude_key_suffix=""
  if [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
    claude_key_suffix="${ANTHROPIC_API_KEY: -20}"
  elif [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
    claude_key_suffix="${CLAUDE_CODE_OAUTH_TOKEN: -20}"
  fi
  seed_mcp_json /root/.claude.json "$url" "$claude_key_suffix"

  # accept workspace trust + project onboarding for /build (write-once)
  if [[ -f /usr/bin/claude ]] && ! claude config get hasTrustDialogAccepted 2>/dev/null | grep -q true; then
    echo "==> accepting claude workspace trust for /build"
    cd /build && claude config set hasTrustDialogAccepted true 2>/dev/null || true
    cd /build && claude config set hasCompletedProjectOnboarding true 2>/dev/null || true
  fi

  # ── agy (Antigravity CLI) ────────────────────────────────────────────────────
  # agy MCP: server defined in .mcp.json at workspace root, approved via enabledMcpjsonServers
  # env field passes AXO_LINK_MCP_* vars so agy sessions inherit them for header interpolation
  local agy_settings="/root/.agy/settings.json"
  python3 - <<PYEOF
import json, os
path = "${agy_settings}"
cfg = {}
if os.path.exists(path):
    try: cfg = json.load(open(path))
    except Exception: cfg = {}
cfg.setdefault("enabledMcpjsonServers", ["tunnel-mcp"])
cfg.setdefault("permissions", {}).setdefault("allow", ["mcp__tunnel-mcp__*"])
env = cfg.setdefault("env", {})
for var in ["AXO_LINK_MCP_AUTH_TOKEN","AXO_LINK_MCP_SENDER_ID","AXO_LINK_MCP_SENDER_TYPE","AXO_LINK_MCP_AGENT_WORKSPACE"]:
    val = os.environ.get(var, "")
    if val:
        env[var] = val
    elif var not in env:
        env[var] = ""
os.makedirs(os.path.dirname(path), exist_ok=True)
json.dump(cfg, open(path, "w"), indent=2)
PYEOF
  seed_hooks_settings "$agy_settings"

  # ~/.gemini/config/mcp_config.json — global agy MCP config using stdio omni axo-link
  # stdio transport: env vars injected directly, no header interpolation issues
  local agy_mcp_config="/root/.gemini/config/mcp_config.json"
  if [[ ! -f "$agy_mcp_config" ]] || [[ ! -s "$agy_mcp_config" ]]; then
    echo "==> seeding $agy_mcp_config (agy global MCP via omni axo-link)"
    mkdir -p "$(dirname "$agy_mcp_config")"
    cat > "$agy_mcp_config" <<'EOF'
{
  "mcpServers": {
    "tunnel-mcp": {
      "command": "omni",
      "args": ["axo-link"],
      "env": {
        "AXO_LINK_MCP_SENDER_TYPE": "omni_agent"
      }
    }
  }
}
EOF
  fi
  # bypass color/terminal agreement/tool-permission prompts on first launch
  # always ensure keys are present — merge into existing file if it exists
  local agy_dir="/root/.gemini/antigravity-cli"
  mkdir -p "$agy_dir"
  python3 - <<'PYEOF'
import json, os, uuid
agy_dir = "/root/.gemini/antigravity-cli"

# settings: merge bypass flags
path = f"{agy_dir}/settings.json"
cfg = {}
if os.path.exists(path):
    try: cfg = json.load(open(path))
    except Exception: cfg = {}
cfg.setdefault("allowNonWorkspaceAccess", True)
cfg.setdefault("toolPermission", "always-proceed")
if "/build" not in cfg.get("trustedWorkspaces", []):
    cfg.setdefault("trustedWorkspaces", []).append("/build")
json.dump(cfg, open(path, "w"), indent=2)

# installation_id: generate once — marks installation as registered, skips online verify
id_path = f"{agy_dir}/installation_id"
if not os.path.exists(id_path):
    open(id_path, "w").write(str(uuid.uuid4()))
PYEOF

  # ── codex ────────────────────────────────────────────────────────────────────
  if [[ ! -f /root/.codex/config.toml ]]; then
    echo "==> seeding /root/.codex/config.toml"
    mkdir -p /root/.codex
    cat > /root/.codex/config.toml <<EOF
[mcp_servers.tunnel_mcp]
url = "${url}"
enabled = true
bearer_token_env_var = "AXO_LINK_MCP_AUTH_TOKEN"

[mcp_servers.tunnel_mcp.env_http_headers]
"X-Sender-ID"       = "AXO_LINK_MCP_SENDER_ID"
"X-Sender-Type"     = "AXO_LINK_MCP_SENDER_TYPE"
"X-Agent-Workspace" = "AXO_LINK_MCP_AGENT_WORKSPACE"
EOF
  fi
  # always pin the model so syncModelConfig can't wipe it to empty
  # awk -v avoids shell delimiter issues if CODEX_MODEL ever contains | & or \
  if [[ -n "${CODEX_MODEL:-}" && -f /root/.codex/config.toml ]]; then
    local tmp; tmp="$(mktemp)"
    if grep -q '^model[[:space:]]*=' /root/.codex/config.toml; then
      awk -v m="$CODEX_MODEL" '/^model[[:space:]]*=/{print "model = \"" m "\""; next} {print}' \
        /root/.codex/config.toml > "$tmp" && mv "$tmp" /root/.codex/config.toml
    else
      { printf 'model = "%s"\n' "$CODEX_MODEL"; cat /root/.codex/config.toml; } \
        > "$tmp" && mv "$tmp" /root/.codex/config.toml
    fi
  fi
}
seed_mcp_configs

# ensure DB is writable (volume may carry 644 from a previous run)
chmod -f 660 /var/lib/omni-root/ptydaemon.db 2>/dev/null || true

# ── warn about unconfigured agents ────────────────────────────────────────────
warn_missing_keys() {
  local warned=0
  if [[ -z "${ANTHROPIC_API_KEY:-}" && -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
    echo "  WARNING: neither ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN set — claude will not authenticate"; warned=1
  fi
  if [[ -z "${OPENAI_API_KEY:-}" && -z "${OPENAI_OAUTH_TOKEN:-}" ]]; then
    echo "  WARNING: neither OPENAI_API_KEY nor OPENAI_OAUTH_TOKEN set       — codex will not authenticate"; warned=1
  fi
  if [[ -z "${GEMINI_API_KEY:-}" && -z "${GOOGLE_API_KEY:-}" && -z "${GOOGLE_CLOUD_PROJECT:-}" ]]; then
    echo "  WARNING: no GEMINI_API_KEY / GOOGLE_API_KEY set                  — gemini will not authenticate (or use ~/.gemini/ OAuth)"; warned=1
  fi
  if [[ $warned -eq 1 ]]; then echo "  → set keys/tokens in development/.env.docker (see .env.docker.example)"; fi
}
warn_missing_keys

echo "==> handing off to systemd..."
exec /sbin/init
