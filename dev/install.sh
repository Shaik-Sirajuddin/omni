#!/usr/bin/env bash
# install_phase + setup_phase for local (build-from-source) installs
# build_phase is run first unless SKIP_BUILD=1
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SETUP_SH="$REPO_ROOT/deployment/setup.sh"
BUILD_SH="$REPO_ROOT/dev/build.sh"

# ── build_phase ───────────────────────────────────────────────────────────────
if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
  echo "==> build_phase"
  # shellcheck source=dev/build.sh
  source "$BUILD_SH"
  build
fi

# ── install_phase + setup_phase ───────────────────────────────────────────────
echo "==> install_phase"

# Point setup.sh functions at the locally built binaries
export BIN_DIR="$REPO_ROOT/deployment/dist/local/bin"

# shellcheck source=deployment/setup.sh
source "$SETUP_SH"

need_root
install_binaries
link_binaries

echo "==> setup_phase"
write_service
check_agent_binaries
reload_and_enable

echo "==> install complete — 'omni' is ready"
