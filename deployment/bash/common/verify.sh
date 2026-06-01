#!/usr/bin/env bash
# common/verify.sh — post-install sanity checks (sourced by setup scripts)
#
# verify_install() always exits 0 (degraded-ok): missing binaries print
# actionable warnings but do not abort, since the user may need to open a
# new shell or add ~/.local/bin to PATH before the check passes.
set -euo pipefail

verify_install() {
  local ok=1

  echo "==> verifying install"

  for bin in omni omni-server; do
    if command -v "$bin" &>/dev/null; then
      echo "    [ok] $bin found at $(command -v "$bin")"
    else
      echo "    [warn] $bin not found in PATH — add ~/.local/bin to your PATH:" >&2
      echo "      export PATH=\"\$HOME/.local/bin:\$PATH\"" >&2
      ok=0
    fi
  done

  for agent in claude codex agy; do
    if command -v "$agent" &>/dev/null; then
      echo "    [ok] $agent found"
    else
      echo "    [warn] $agent not found — agent features will be unavailable" >&2
    fi
  done

  if [[ "$ok" -eq 1 ]]; then
    echo "==> install verified"
  else
    # Degraded-ok: binaries are installed, PATH may just need updating.
    echo "==> install complete with warnings — open a new shell or update PATH" >&2
  fi
}
