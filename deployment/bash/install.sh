#!/usr/bin/env bash
# install.sh — omni release-tarball installer entry point
# Bundled inside each release tarball alongside the bin/ directory.
# Detects platform and delegates to the appropriate setup script.
#
# Usage (from extracted tarball):
#   bash install.sh
#
# Override defaults via environment:
#   BIN_DIR=/path/to/bins bash install.sh     # use binaries from a custom path
#   OMNI_GLOBAL_INSTALL=1 bash install.sh     # system-wide install (requires root)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# BIN_DIR: directory containing the omni/omni-server binaries.
# Defaults to bin/ adjacent to this script (standard tarball layout).
export BIN_DIR="${BIN_DIR:-${SCRIPT_DIR}/bin}"

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
