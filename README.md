# omni

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)

Omni is a supervisor for AI coding agents (Claude, Codex, Gemini). It manages agent sessions, hooks, and inter-agent messaging over a local PTY daemon and MCP transport.

![omni demo](readme.gif)

**Multi-agent orchestration in your terminal** — run Claude Code and OpenAI Codex side-by-side, let them message each other, and coordinate across tasks automatically.

### Features

- **Inter-agent messaging** — agents send and receive messages across sessions via the built-in axolink MCP transport
- **Auto retries** — hook operator automatically retries failed tool calls and session events with configurable back-off
- **Claude support** — first-class Claude Code integration (Claude API, Haiku, Sonnet, Opus)
- **Codex support** — OpenAI Codex CLI managed as a full omni agent session
- **Agy support** — Gemini/Agy agent sessions with the same session and hook lifecycle

## Install

### Linux (amd64 / arm64)

```bash
curl -fsSL https://raw.githubusercontent.com/Shaik-Sirajuddin/omni/main/install.sh | bash
```

Requires `sudo`. Tested on Ubuntu 22.04+, Debian 12+, and compatible distros.

### Windows (WSL)

Open a WSL terminal (Ubuntu recommended) and run the same command:

```bash
curl -fsSL https://raw.githubusercontent.com/Shaik-Sirajuddin/omni/main/install.sh | bash
```

Requires WSL 2 with systemd enabled. To enable systemd in WSL, add to `/etc/wsl.conf`:

```ini
[boot]
systemd=true
```

Then restart WSL: `wsl --shutdown` and reopen the terminal.

→ See [docs/quickstart.md](docs/quickstart.md) for what gets installed, upgrade instructions, and agent commands.

## Quick reference

```bash
omni agent list                          # list running sessions
omni agent resume <session-id>           # attach to a session
omni agent exec <session-id> -- <cmd>    # run a command inside a session
```

## Development

```bash
make docker-up       # start the dev container (requires .env.docker)
make docker-rebuild  # rebuild image after code changes
make docker-connect  # open a shell in the running container
```

→ See [development/](development/) for docker setup and `.env.docker.example`.

## License

[GNU Affero General Public License v3.0](LICENSE) — see [AGPL-3.0](https://www.gnu.org/licenses/agpl-3.0) for details.
