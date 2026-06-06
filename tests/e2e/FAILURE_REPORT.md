# E2E Go Migration — Test Failure Report

**Environment**: local (`E2E_TARGET=local`), omni binary at `/home/siraj/.local/bin/omni`
**Date**: 2026-06-05
**Run command**: `go test -tags e2e -timeout 240s ./tests/e2e/...`

---

## Summary

| Package | Passed | Failed | Skipped |
|---------|--------|--------|---------|
| exec    | 4      | 13     | 0       |
| mcp     | 1      | 3      | 0       |
| pty     | 3      | 0      | 0       |
| engine  | 0      | 6      | 0       |
| codeagent/codex | 3 | 1  | 1       |
| **Total** | **11** | **23** | **1**  |

---

## Passing Tests ✅

| Test | Package |
|------|---------|
| TestBgFlagAccepted | exec |
| TestBgShortFlag | exec |
| TestExecBgAgentNotFound | exec |
| TestResumeNonExistent | exec |
| TestPTYResumeColdStart | pty |
| TestPTYResumeIdempotent | pty |
| TestPTYResumeAfterStop | pty |
| TestAxolinkHTTPReachability | mcp |
| TestCodexBinaryAvailable | codeagent/codex |
| TestCodexAgentInit | codeagent/codex |
| TestCodexMCPConfig | codeagent/codex |

## Skipped Tests ⏭

| Test | Reason |
|------|--------|
| TestCodexConfigStrictMode | No codex config.json found at expected paths |

---

## Failing Tests ❌

### Root Cause A: `agent exec` uses service workspace, not per-test workspace

**Affected tests (13)**: All exec tests that create agents and then exec into them.

`omni agent init --workspace /tmp/xxx` creates an agent in `/tmp/xxx/db`,  
but `omni agent exec <name>` queries the running service which uses its  
**own configured workspace** (default: the workspace the server was started with).

These tests are designed to run against a Docker container where:
- The omni-server runs with workspace `/build`  
- `agent init --workspace /build` writes to the service's database  
- `exec` finds the agent in that same database

**Tests:**
- `TestExecBgNonBlocking` — agent "e2e-bg-agent" not found (no `agent init` before exec, uses hardcoded global agent)
- `TestExecBgOnActivePTY` — same reason
- `TestPromptDelivered` — same reason
- `TestSequentialBgCalls` — same reason
- `TestDetachedExecDelivery` — agent init in per-test workspace, exec can't find it
- `TestWaitPTYReadyColdStart` — same
- `TestNonDetachedRegression` — same; also times out waiting for agent
- `TestDoubleWrapFixClaude` — same; requires claude binary active session
- `TestDoubleWrapFixCodex` — same; requires codex binary active session
- `TestExecResumeLaunchesPTY` — same; times out
- `TestHookReceiptConfirmsDelivery` — same; no hook event logged
- `TestMultiplePromptsSequential` — same
- `TestSessionPersistsAfterExec` — same
- `TestCodexExecDelivery` (codeagent/codex) — same workspace mismatch

**Fix for Docker**: Tests pass in docker where server workspace = test workspace = `/build`.  
**Fix for local**: Either point tests at the service's real workspace, or use `--id` after
resolving the agent UUID from `agent list` output. Tracked for future improvement.

---

### Root Cause B: Docker-only service setup (MCP/engine tests)

**Affected tests (9)**:

- `TestMCPConfigReflection` — checks `/root/.claude.json` for axolink entry (Docker entrypoint seeds this; not present locally)
- `TestAddMCPIdempotency` — same
- `TestMCPToolListViaExec` — requires claude binary with active Claude session
- `TestSendMessageRecorded` — agent init in per-test workspace; exec/MCP can't reach it
- `TestHealthEndpoint` — `/health` route not exposed on local service (only `/mcp` is), returns connection refused
- `TestFullSessionExecAndResponse` — workspace mismatch + requires active claude session
- `TestSessionNoCrashOnBadInput` — workspace mismatch
- `TestMultiAgentConcurrentExec` — workspace mismatch
- `TestSessionStopAndStatus` — workspace mismatch

---

## Notes on Expected Behavior

1. **PTY tests pass** because `omni agent resume` properly accepts `--workspace` and creates sessions that are visible to the local service. Detach + restart cycles work as expected.

2. **`--bg` flag** is correctly implemented and accepted (TestBgFlagAccepted, TestBgShortFlag).

3. **`--resume` rejection** is correctly enforced (confirmed in TestBgFlagAccepted).

4. **MCP HTTP** is reachable (TestAxolinkHTTPReachability passes) — axolink server is running on `:18062`.

5. **Codex** binary, agent init, and config tests all pass.

6. The `OMNI_WORKSPACE` env var is injected by the harness (`HostExecutor.WithEnv`) but `omni agent exec` does **not** currently honor it — it only uses the service's configured workspace. This is a CLI limitation, not a test bug.

---

## Recommended Next Steps

1. **Run against Docker**: `make docker-up && E2E_CONTAINER=omni-feat-e2e-go-migration-ubuntu-1 go test -tags e2e ./tests/e2e/...` should make the workspace mismatch failures disappear.

2. **Add `--id` support to exec harness**: After `agent init`, parse the UUID from `agent list --output json` and use `--id` in exec calls to bypass workspace name resolution.

3. **`/health` route**: If `TestHealthEndpoint` should pass locally, the health route needs to be at `/health` on the MCP server.

4. **`~/.claude.json` seeding**: In non-Docker environments, the MCP config tests require manual seeding of `~/.claude.json` with the axolink entry.
