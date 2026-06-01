#!/usr/bin/env bash
# build_phase — builds omni and omni-server for the local native OS/arch
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OMNI_DIR="$REPO_ROOT/omni"
SVC_CMD_DIR="$REPO_ROOT/svc/cmd"
OUT_DIR="${OUT_DIR:-$REPO_ROOT/deployment/dist/local/bin}"

build() {
  local version="${VERSION:-$(git -C "$REPO_ROOT" describe --tags --abbrev=0 2>/dev/null || echo "dev")}"
  local out_dir="${OUT_DIR:-$REPO_ROOT/deployment/dist/local/bin}"
  local goos="${GOOS:-}"
  local goarch="${GOARCH:-}"

  mkdir -p "$out_dir"

  # go.work at REPO_ROOT lets Go resolve all local modules without per-module
  # replace directives. GOWORK must point at it when running from a sub-dir.
  export GOWORK="$REPO_ROOT/go.work"

  echo "==> building omni ${goos:+$goos/}${goarch:+$goarch} ($version)..."
  GOOS="$goos" GOARCH="$goarch" go build \
    -C "$OMNI_DIR" \
    -ldflags "-X main.Version=${version}" \
    -o "$out_dir/omni" \
    ./cli/cmd/omni/

  echo "==> building omni-server ${goos:+$goos/}${goarch:+$goarch} ($version)..."
  GOOS="$goos" GOARCH="$goarch" go build \
    -C "$SVC_CMD_DIR" \
    -ldflags "-X main.Version=${version}" \
    -o "$out_dir/omni-server" \
    .

  echo "==> build complete — binaries in $out_dir"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  build
fi
