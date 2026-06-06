# Tunnel MCP Setup

## Runtime Shape

Tunnel MCP is split into two local servers plus one bridge mode:

| Component | Transport | Purpose |
|---|---|---|
| MCP server | TCP HTTP streamable MCP | MCP protocol endpoint for MCP clients |
| Pure HTTP service server | Unix socket or TCP HTTP | Non-MCP local service routes and hooks |
| Stdio bridge | stdio process | Connects a stdio MCP client to the existing MCP HTTP endpoint |

`http_unix` is not an MCP transport in the target split. Unix socket binding belongs to the pure HTTP service server via `AXO_LINK_SERVICE_HTTP_BIND=unix`.

## Environment

| Variable | Owner | Purpose |
|---|---|---|
| `AXO_LINK_MCP_TRANSPORT` | MCP runner | `streamable_http` or `stdio` |
| `AXO_LINK_MCP_ADDR` | MCP runner | MCP TCP HTTP listen address, default target `:18062` |
| `AXO_LINK_MCP_HTTP_PATH` | MCP runner and stdio bridge | MCP streamable HTTP path, default `/mcp` |
| `AXO_LINK_MCP_AUTH_TOKEN` | MCP and service auth | Bearer token |
| `AXO_LINK_MCP_DB_PATH` | local service process | SQLite database path |
| `AXO_LINK_MCP_DELIVERY_MODE` | local service process | Delivery mode, usually `async` |
| `AXO_LINK_MCP_SENDER_ID` | stdio bridge | Forwarded as `X-SENDER-ID` |
| `AXO_LINK_MCP_SENDER_TYPE` | stdio bridge | Forwarded as `X-SENDER-TYPE` |
| `AXO_LINK_MCP_AGENT_WORKSPACE` | stdio bridge | Forwarded as `X-AGENT-WORKSPACE` |
| `AXO_LINK_SERVICE_HTTP_BIND` | pure HTTP service server | `unix`, `tcp`, or `disabled` |
| `AXO_LINK_SERVICE_UNIX_SOCKET` | pure HTTP service server | Unix socket path, default target `/run/omni-${USER}/service.sock` |
| `AXO_LINK_SERVICE_ADDR` | pure HTTP service server | TCP listen address, default target `:18061` |

## Example 1: Stdio Client Only

Starts no server. It only connects to an existing local service process exposing MCP streamable HTTP.

```env
AXO_LINK_MCP_TRANSPORT=stdio
AXO_LINK_MCP_AUTH_TOKEN=axolink-dev-token
AXO_LINK_MCP_SENDER_ID=mcp-client
AXO_LINK_MCP_SENDER_TYPE=omni_agent
AXO_LINK_MCP_AGENT_WORKSPACE=/path/to/workspace
```

Optional endpoint overrides if the existing MCP HTTP service does not use defaults:

```env
AXO_LINK_MCP_ADDR=http://127.0.0.1:18062
AXO_LINK_MCP_HTTP_PATH=/mcp
```

Expected behavior:

- Does not start the MCP streamable HTTP server.
- Does not start the pure HTTP service server.
- Forwards MCP stdio frames to the configured MCP HTTP endpoint.

## Example 2: Local Service Process

Starts both local servers:

- MCP streamable HTTP over TCP.
- Pure HTTP service routes over unix socket through `server/http`.

```env
AXO_LINK_MCP_TRANSPORT=streamable_http
AXO_LINK_MCP_ADDR=:18062
AXO_LINK_MCP_HTTP_PATH=/mcp
AXO_LINK_MCP_AUTH_TOKEN=axolink-dev-token
AXO_LINK_MCP_DELIVERY_MODE=async

AXO_LINK_SERVICE_HTTP_BIND=unix
AXO_LINK_SERVICE_UNIX_SOCKET=/run/omni-${USER}/service.sock
AXO_LINK_SERVICE_ADDR=:18061
```

Expected behavior:

- MCP clients connect to `http://127.0.0.1:18062/mcp`.
- Non-MCP local clients and hooks connect to `unix:///run/omni-${USER}/service.sock`.
- MCP tools call `server/service.Service` in memory.
- Pure HTTP routes use `server/http` and call the same `server/service.Service`.

## Health Checks

Pure HTTP service health is a regular HTTP endpoint:

```sh
curl --unix-socket /run/omni-${USER}/service.sock http://unix/healthz
```

If the pure HTTP service server is bound to TCP instead:

```sh
curl -s http://127.0.0.1:18061/healthz
```

The MCP server exposes health as an MCP tool, not as a plain REST health endpoint. Use an MCP client against the streamable HTTP endpoint and call the `health` tool:

```text
http://127.0.0.1:18062/mcp
```

## Stdio Bridge Headers

The stdio bridge maps env vars to MCP HTTP headers:

| Env | Header |
|---|---|
| `AXO_LINK_MCP_AUTH_TOKEN` | `Authorization: Bearer <token>` |
| `AXO_LINK_MCP_SENDER_ID` | `X-SENDER-ID` |
| `AXO_LINK_MCP_SENDER_TYPE` | `X-SENDER-TYPE` (`omni_agent`) |
| `AXO_LINK_MCP_AGENT_WORKSPACE` | `X-AGENT-WORKSPACE` |

## Hook Route

When the pure HTTP service server owns the mux and the delivery engine supports hook registration, `/hook` is mounted on the pure HTTP server.

Default unix hook URL:

```text
unix:///run/omni-${USER}/service.sock/hook
```

The stdio bridge does not mutate hook configuration and does not start servers.
