#!/usr/bin/env bash
# install.sh — omni curl installer entry point
# Detects platform and delegates to the appropriate setup script.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OS="$(uname -s)"

case "$OS" in
  Linux)
    # shellcheck source=linux/setup.sh
    source "$SCRIPT_DIR/linux/setup.sh"
    ;;
  Darwin)
    # shellcheck source=macos/setup.sh
    source "$SCRIPT_DIR/macos/setup.sh"
    ;;
  *)
    echo "error: unsupported platform '$OS'" >&2
    echo "  Windows: winget install Omni.Omni" >&2
    echo "  Homebrew (macOS/Linux): brew install omni" >&2
    exit 1
    ;;
esac

# shellcheck source=common/verify.sh
source "$SCRIPT_DIR/common/verify.sh"
verify_install
