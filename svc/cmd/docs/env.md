# Environment Variables

All services read configuration from environment variables. No flags are required — defaults are production-ready.

## ptydaemon

| Variable | Default | Description |
|----------|---------|-------------|
| `OMNI_PTY_SOCKET` | `/run/omni-<user>/omni-pty.sock` | Unix socket path |
| `PTYDAEMON_DB` | `/var/lib/omni-<user>/ptydaemon.db` | SQLite database path |

## hook-operator

| Variable | Default | Description |
|----------|---------|-------------|
| `HOOK_OPERATOR_SOCKET` | `/run/omni-<user>/hook-operator.sock` | Unix socket path |

## axolink-mcp

| Variable | Default | Description |
|----------|---------|-------------|
| `AXO_LINK_MCP_TRANSPORT` | `http_unix` | Transport: `http_unix`, `http`, `streamable_http`, `stdio` |
| `AXO_LINK_MCP_UNIX_SOCKET` | `/run/omni-<user>/mcp.sock` | Unix socket path |
| `AXO_LINK_MCP_HTTP_PATH` | `/mcp` | MCP SSE HTTP path |
| `AXO_LINK_MCP_ADDR` | `:18061` | TCP listen address (HTTP/streamable transports) |
| `AXO_LINK_MCP_REST_PORT` | — | Alternative port for TCP address |

## Env Files

| File | Purpose |
|------|---------|
| `axolink/dev/.env` | Dev overrides for axolink-mcp |
| `axolink/.env.example` | Reference template for axolink-mcp vars |
