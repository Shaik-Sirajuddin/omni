# Quickstart

## Install

Paste this into your terminal (Linux or WSL):

```bash
curl -fsSL https://raw.githubusercontent.com/Shaik-Sirajuddin/memory/main/install.sh | bash
```

The installer auto-detects your architecture (amd64 or arm64). `sudo` is required — binaries are installed to `/opt/omni/bin/` and symlinked into `/usr/local/bin/`.

## What gets installed

```
/opt/omni/
  bin/
    omni
    omni-server

/usr/local/bin/omni        -> /opt/omni/bin/omni
/usr/local/bin/omni-server -> /opt/omni/bin/omni-server

/etc/systemd/system/omni@.service
```

Binaries live under `/opt/omni/bin/` and are symlinked into `/usr/local/bin/` so they are available on `PATH` without any shell config changes. The install requires `sudo`.

`omni-server` is the supervisor — it starts the PTY daemon and hook-operator as
in-process goroutines. It manages two sockets:

| Socket | Protocol |
|--------|----------|
| `/run/omni-<user>/omni-pty.sock` | Raw PTY (attach, exec, stream) |
| `/run/omni-<user>/hook-operator.sock` | HTTP hook callbacks |

The daemon starts automatically on install and on every boot via systemd.

You can override the install prefix with `OMNI_PREFIX`:

```bash
sudo OMNI_PREFIX=/opt/myprefix bash setup.sh
```

## Upgrade

Re-run the same install command — it detects the current version and upgrades if a newer release is available.

## Agent commands

List running agent sessions:

```bash
omni agent list
```

Resume a session by ID:

```bash
omni agent resume <session-id>
```

Run a command inside a session:

```bash
omni agent exec <session-id> -- <command> [args...]
```

## OS selection guidance

- **Linux (native)** — fully supported on amd64 and arm64.
- **WSL (Windows Subsystem for Linux)** — supported; systemd must be enabled in your WSL distribution (`/etc/wsl.conf` → `[boot] systemd=true`).
- macOS and Windows native are not supported in this release.
