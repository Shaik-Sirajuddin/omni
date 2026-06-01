#!/usr/bin/env bash
# macOS setup — sourced by install.sh
set -euo pipefail

: "${BIN_DIR:?BIN_DIR must be set by caller}"
: "${OMNI_PREFIX:=${HOME}/.local/opt/omni}"

OMNI_BIN="$OMNI_PREFIX/bin"
SYMLINK_DIR="${HOME}/.local/bin"
BINARIES=(omni omni-server)
PLIST_LABEL="com.omni.daemon"
PLIST_DIR="${HOME}/Library/LaunchAgents"
PLIST_FILE="${PLIST_DIR}/${PLIST_LABEL}.plist"
PLIST_TMPL="$(dirname "${BASH_SOURCE[0]}")/com.omni.daemon.plist.tmpl"

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

_write_launchd_plist() {
  mkdir -p "$PLIST_DIR"
  sed \
    -e "s|{{OMNI_SERVER}}|${OMNI_BIN}/omni-server|g" \
    -e "s|{{HOME}}|${HOME}|g" \
    -e "s|{{LOCAL_BIN}}|${HOME}/.local/bin|g" \
    "$PLIST_TMPL" > "$PLIST_FILE"
  echo "==> wrote $PLIST_FILE"
}

_reload_launchd() {
  if launchctl list "$PLIST_LABEL" &>/dev/null 2>&1; then
    launchctl unload "$PLIST_FILE" 2>/dev/null || true
  fi
  launchctl load -w "$PLIST_FILE"
  echo "==> $PLIST_LABEL loaded via launchd"
  echo "    to keep running after logout: launchctl enable gui/$(id -u)/$PLIST_LABEL"
}

_write_launchd_plist
_reload_launchd
