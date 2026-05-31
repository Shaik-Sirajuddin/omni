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

  # sync go.mod/go.sum so Docker cache misses from local replace directives don't break the build
  go mod tidy -C "$OMNI_DIR"    2>/dev/null || true
  go mod tidy -C "$SVC_CMD_DIR" 2>/dev/null || true

  echo "==> building omni ${goos:+$goos/}${goarch}${goos:+} ($version)..."
  GOOS="$goos" GOARCH="$goarch" go build \
    -C "$OMNI_DIR" \
    -ldflags "-X main.Version=${version}" \
    -o "$out_dir/omni" \
    ./cli/cmd/omni/

  echo "==> building omni-server ${goos:+$goos/}${goarch}${goos:+} ($version)..."
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
