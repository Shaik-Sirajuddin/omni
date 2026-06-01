#!/usr/bin/env bash
# common/verify.sh — post-install sanity checks (sourced by setup scripts)
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
    echo "==> install complete with warnings — see above" >&2
  fi
}
