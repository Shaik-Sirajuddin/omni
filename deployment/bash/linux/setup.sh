#!/usr/bin/env bash
# Linux setup — sourced by install.sh
set -euo pipefail

: "${BIN_DIR:?BIN_DIR must be set by caller}"
: "${OMNI_PREFIX:=${HOME}/.local/opt/omni}"

OMNI_BIN="$OMNI_PREFIX/bin"
SYMLINK_DIR="${HOME}/.local/bin"
BINARIES=(omni omni-server)

if [[ "${OMNI_GLOBAL_INSTALL:-0}" == "1" ]]; then
  [[ "$EUID" -eq 0 ]] || { echo "error: OMNI_GLOBAL_INSTALL=1 requires root" >&2; exit 1; }
  OMNI_PREFIX="${OMNI_PREFIX:-/opt/omni}"
  OMNI_BIN="$OMNI_PREFIX/bin"
  SYMLINK_DIR="/usr/local/bin"
fi

echo "==> installing omni to $OMNI_BIN"
mkdir -p "$OMNI_BIN"
for bin in "${BINARIES[@]}"; do
  install -m 755 "$BIN_DIR/$bin" "$OMNI_BIN/$bin"
done

echo "==> symlinking into $SYMLINK_DIR"
mkdir -p "$SYMLINK_DIR"
for bin in "${BINARIES[@]}"; do
  ln -sf "$OMNI_BIN/$bin" "$SYMLINK_DIR/$bin"
done

_write_systemd_user_unit() {
  local svc_file="${XDG_CONFIG_HOME:-${HOME}/.config}/systemd/user/omni.service"
  local debug_env=""
  [[ "${DEBUG:-0}" == "1" ]] && debug_env=$'\nEnvironment=DEV=1'

  mkdir -p "$(dirname "$svc_file")"
  cat > "$svc_file" <<EOF
[Unit]
Description=Omni daemon
After=network.target

[Service]
Type=simple
ExecStart=${OMNI_BIN}/omni-server
Restart=on-failure
RestartSec=3s
RuntimeDirectory=omni
RuntimeDirectoryMode=0700
StateDirectory=omni
StateDirectoryMode=0700
Environment=OMNI_PTY_SOCKET=%t/omni/omni-pty.sock
Environment=PTYDAEMON_DB=%S/omni/ptydaemon.db
Environment=HOOK_OPERATOR_SOCKET=%t/omni/hook-operator.sock
Environment=PATH=/usr/local/bin:/usr/bin:/bin:%h/.local/bin${debug_env}
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
EOF
  echo "==> wrote $svc_file"
}

_write_systemd_system_unit() {
  local svc_file="/etc/systemd/system/omni@.service"
  local debug_env=""
  [[ "${DEBUG:-0}" == "1" ]] && debug_env=$'\nEnvironment=DEV=1'

  mkdir -p "$(dirname "$svc_file")"
  cat > "$svc_file" <<EOF
[Unit]
Description=Omni PTY daemon for %i
After=network.target

[Service]
Type=simple
User=%i
ExecStart=/opt/omni/bin/omni-server
Restart=on-failure
RestartSec=3s
RuntimeDirectory=omni-%i
RuntimeDirectoryMode=0700
StateDirectory=omni-%i
StateDirectoryMode=0700
Environment=OMNI_PTY_SOCKET=/run/omni-%i/omni-pty.sock
Environment=PTYDAEMON_DB=/var/lib/omni-%i/ptydaemon.db
Environment=HOOK_OPERATOR_SOCKET=/run/omni-%i/hook-operator.sock
Environment=PATH=/usr/local/bin:/usr/bin:/bin:/home/%i/.local/bin${debug_env}
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
  echo "==> wrote $svc_file"
}

_reload_systemd() {
  local systemctl="systemctl --user"
  local svc_name="omni"
  if [[ "${OMNI_GLOBAL_INSTALL:-0}" == "1" ]]; then
    systemctl="systemctl"
    svc_name="omni@${SUDO_USER:-$(id -un)}"
  fi

  if [[ -z "${XDG_RUNTIME_DIR:-}" ]] && [[ "${OMNI_GLOBAL_INSTALL:-0}" != "1" ]]; then
    echo "error: user-mode systemd requires an active D-Bus session (XDG_RUNTIME_DIR not set)." >&2
    echo "  Run: loginctl enable-linger $(id -un)" >&2
    echo "       export XDG_RUNTIME_DIR=/run/user/$(id -u)" >&2
    exit 1
  fi

  $systemctl daemon-reload
  if $systemctl is-active --quiet "$svc_name" 2>/dev/null; then
    $systemctl restart "$svc_name"
  else
    $systemctl enable --now "$svc_name"
  fi
  echo "==> $svc_name started"
}

if [[ "${OMNI_GLOBAL_INSTALL:-0}" == "1" ]]; then
  _write_systemd_system_unit
else
  _write_systemd_user_unit
fi
_reload_systemd
