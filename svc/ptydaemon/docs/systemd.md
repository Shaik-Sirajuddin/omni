# omni-server systemd service

## Service name
`omni-server`

## Key environment variables
| Variable            | Default                    | Purpose                          |
|---------------------|----------------------------|----------------------------------|
| PTYDAEMON_SOCKET    | /tmp/ptydaemon.sock        | Unix socket path for IPC         |
| PTYDAEMON_DB        | /tmp/ptydaemon.db          | SQLite database path             |
| PTYDAEMON_PID       | /tmp/omni-server.pid       | PID file path                    |
| DEV                 | (unset)                    | Set to any value for debug logs  |

## Install as a systemd user service
```bash
# copy and fill in the template
cp svc/ptydaemon/ptydaemon.service.template ~/.config/systemd/user/omni-server.service
systemctl --user daemon-reload
systemctl --user enable omni-server
systemctl --user start omni-server
```

## Common commands
```bash
omni server start          # start daemon (direct or via systemd)
omni server start -d       # start with debug logging (DEV=1)
omni server stop           # stop daemon
omni server status         # print active sessions and daemon uptime

systemctl --user status omni-server
journalctl --user -u omni-server -f     # follow logs
journalctl --user -u omni-server -n 50  # last 50 lines
```

## Check socket is live
```bash
curl --unix-socket /tmp/ptydaemon.sock http://localhost/status
```

## Non-systemd environments (Coder workspaces, containers, CI)

`omni server start` works without systemd. When systemd is absent it falls back to
spawning `omni-server` directly and tracking it via a PID file (`/tmp/omni-server.pid`).
The server itself creates all required directories on startup, so no pre-flight setup
is needed.

### Coder workspace startup_script

Add to your workspace Terraform template:

```hcl
resource "coder_agent" "main" {
  startup_script = <<-EOF
    omni server start
  EOF
}
```

Coder restarts the script when the workspace resumes, so the daemon comes back up
automatically. No supervisor (supervisord, s6, etc.) is required.

### Manual (any Linux without systemd)

```bash
omni server start           # starts omni-server in background, writes PID file
omni server status          # verify it is running
omni server stop            # stop it
```

To override where the socket and database are placed:

```bash
OMNI_PTY_SOCKET=/tmp/omni-pty.sock \
HOOK_OPERATOR_SOCKET=/tmp/hook-operator.sock \
PTYDAEMON_DB=/tmp/ptydaemon.db \
omni server start
```

### Why it failed before (fixed in this version)

In systemd installs, `RuntimeDirectory=` and `StateDirectory=` create the socket and
database parent directories automatically. Without systemd those directories never
existed, causing two startup errors:

- `ptydaemon: unable to open database file (14)` — `SQLITE_CANTOPEN` because
  `/var/lib/omni-<user>/` did not exist.
- `connect: no such file or directory` on resume — socket parent `/run/omni-<user>/`
  did not exist.

Both are now fixed: `omni-server` creates all required directories itself on startup.
