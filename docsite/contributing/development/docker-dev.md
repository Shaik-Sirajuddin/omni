# Docker Dev Environment

Run `omni` + `omni-server` inside an Ubuntu+systemd container, built from your local source.

## Prerequisites

- Docker + Docker Compose v2
- The project checked out locally

## First-Time Setup

```bash
make dev-preflight
```

This creates:
- `development/.env.docker` — copy of the example, fill in your API keys

Then edit `development/.env.docker` with your keys:

| Variable | Agent | Purpose |
|---|---|---|
| `ANTHROPIC_API_KEY` | claude | API key |
| `CLAUDE_CODE_OAUTH_TOKEN` | claude | OAuth token (Pro/Max subscription, takes precedence over API key) |
| `OPENAI_API_KEY` | codex | API key |
| `OPENAI_OAUTH_TOKEN` | codex | OAuth token (ChatGPT Plus, mapped to OPENAI_API_KEY if no key set) |
| `ANTHROPIC_MODEL` | claude | Default model — `claude-haiku-4-5` (default) |
| `CODEX_MODEL` | codex | Default model — `gpt-5.4-mini` (default) |
| `GEMINI_API_KEY` | gemini | API key (CI/non-interactive) |
| `GOOGLE_API_KEY` | gemini | Google Cloud API key (Vertex AI express mode) |
| `GOOGLE_CLOUD_PROJECT` | gemini | GCP project ID (Vertex AI) |
| `GEMINI_MODEL` | gemini | Default model (e.g. `gemini-2.5-flash`) |
| `AGY_MODEL` | agy | Default model for Antigravity CLI |
| `DEBUG` | omni | Set to `1` to enable slog debug logging |

## Start

```bash
make docker-up
```

On first start the container will:
1. Link pre-built binaries into `/usr/local/bin`
2. Write the system unit (`/etc/systemd/system/omni@.service`)
3. Hand off to systemd — `omni@root.service` starts automatically

After the container is up you are dropped straight into an interactive shell inside it.

To re-attach to a running container:

```bash
make docker-connect
```

## Rebuild Image

Required when Go source or Dockerfile changes:

```bash
make docker-rebuild
```

Agent configs and DB persist across rebuilds — they are bind-mounted from `development/local.example/` and named volumes.

## Health Checks

```bash
# MCP streamable HTTP (agent clients) — exec into container, no host port binding
docker compose -f development/docker-compose.yaml exec ubuntu \
  curl -s http://127.0.0.1:18062/mcp

# PTY socket
docker compose -f development/docker-compose.yaml exec ubuntu \
  test -S /run/omni-root/omni-pty.sock && echo ok

# systemd service status
docker compose -f development/docker-compose.yaml exec ubuntu \
  systemctl status omni@root --no-pager
```

## Service Logs

```bash
# inside container (make docker-connect)
journalctl -fu omni@root

# from host — stream live
docker compose -f development/docker-compose.yaml exec -T ubuntu \
  journalctl -fu omni@root --no-pager

# from host — pipe to local file (stop with: kill %1)
docker compose -f development/docker-compose.yaml exec -T ubuntu \
  journalctl -fu omni@root --no-pager \
  > development/local/logs-$(date +%s).txt &
```

## Volumes and Persistence

| Source | Container path | Purpose |
|---|---|---|
| `omni-persist` (volume) | `/var/lib/omni-root` | ptydaemon DB, state |
| `omni-auth` (volume) | `/root/.config` | agent CLI auth tokens, omni config |
| `omni-data` (volume) | `/root/.local/share/memory` | agent DB, sandbox data |
| `agent-claude` (volume) | `/root/.claude` | claude settings + history |
| `agent-codex` (volume) | `/root/.codex` | codex config + memories |
| `development/local/.codex/auth.json` | `/root/.codex/auth.json` | codex OAuth credentials (read-only) |
| `development/local/.gemini/antigravity-cli/antigravity-oauth-token` | `/root/.gemini/antigravity-cli/antigravity-oauth-token` | gemini/agy OAuth token (read-only) |
| `agent-agy` (volume) | `/root/.agy` | agy settings + history |

`development/local.example/` is committed to git and bind-mounted directly — edit files there to persist changes across rebuilds.

All data survives `make docker-down docker-up`. To fully reset:

```bash
docker compose -f development/docker-compose.yaml down -v   # removes named volumes
```

## Container Naming

The compose file uses no fixed `container_name`, so Docker derives names from the project:

| Context | Command | Container name |
|---|---|---|
| Dev | `docker compose up -d` | `development-ubuntu-1` |
| E2E tests | `docker compose -p omni-e2e up -d` | `omni-e2e-ubuntu-1` |

Each project gets its own prefixed volumes and network — fully isolated. To tear down the e2e environment including volumes:

```bash
docker compose -p omni-e2e down -v
```

## Stop

```bash
make docker-down
```
