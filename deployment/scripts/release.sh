#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OMNI_DIR="$REPO_ROOT/omni"
PTY_DIR="$REPO_ROOT/svc/ptydaemon"
DIST_DIR="$REPO_ROOT/deployment/dist"

# ── Args ──────────────────────────────────────────────────────────────────────
AUTO_CHECKOUT_MAIN=false
POSITIONAL_ARGS=()
for arg in "$@"; do
  case "$arg" in
    --branch=main) AUTO_CHECKOUT_MAIN=true ;;
    *) POSITIONAL_ARGS+=("$arg") ;;
  esac
done
set -- "${POSITIONAL_ARGS[@]+"${POSITIONAL_ARGS[@]}"}"

# ── Branch guard ──────────────────────────────────────────────────────────────
CURRENT_BRANCH=$(git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD)
if [[ "$CURRENT_BRANCH" != "main" ]]; then
  if [[ "$AUTO_CHECKOUT_MAIN" == true ]]; then
    echo "==> switching to main (was on $CURRENT_BRANCH)"
    git -C "$REPO_ROOT" checkout main
  else
    echo "error: must be on main to release (currently on '$CURRENT_BRANCH')" >&2
    echo "       re-run with --branch=main to auto-checkout" >&2
    exit 1
  fi
fi

# ── Version ───────────────────────────────────────────────────────────────────
if [[ -n "${1:-}" ]]; then
  VERSION="$1"
else
  LATEST=$(git -C "$REPO_ROOT" tag --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1)
  if [[ -z "$LATEST" ]]; then
    VERSION="v0.1.0"
  else
    IFS='.' read -r major minor patch <<< "${LATEST#v}"
    VERSION="v${major}.${minor}.$((patch + 1))"
  fi
fi

echo "==> releasing $VERSION"

# shellcheck source=dev/build.sh
source "$REPO_ROOT/dev/build.sh"

# ── Targets ───────────────────────────────────────────────────────────────────
LINUX_TARGETS=("linux/amd64" "linux/arm64")

# darwin and windows targets commented out for now
# DARWIN_TARGETS=("darwin/amd64" "darwin/arm64")
# WINDOWS_TARGETS=("windows/amd64")

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

build_target() {
  local os="$1" arch="$2"
  local pkg_dir="$DIST_DIR/omni-${VERSION}-${os}-${arch}"
  local bin_dir="$pkg_dir/bin"
  mkdir -p "$bin_dir"

  echo "  building omni ($os/$arch)..."
  OUT_DIR="$bin_dir" GOOS="$os" GOARCH="$arch" build

  cp "$REPO_ROOT/deployment/setup.sh" "$pkg_dir/setup.sh"
  chmod +x "$pkg_dir/setup.sh"

  local tarball="$DIST_DIR/omni-${VERSION}-${os}-${arch}.tar.gz"
  tar -czf "$tarball" -C "$DIST_DIR" "$(basename "$pkg_dir")"
  echo "  -> $tarball"
}

for target in "${LINUX_TARGETS[@]}"; do
  IFS='/' read -r os arch <<< "$target"
  build_target "$os" "$arch"
done

# ── Tag + GitHub release ──────────────────────────────────────────────────────
if git -C "$REPO_ROOT" rev-parse "$VERSION" &>/dev/null; then
  echo "==> tag $VERSION already exists, skipping tag creation"
else
  git -C "$REPO_ROOT" tag -a "$VERSION" -m "Release $VERSION"
  git -C "$REPO_ROOT" push origin "$VERSION"
fi

if ! command -v gh &>/dev/null; then
  echo "==> gh CLI not found — tarballs are in $DIST_DIR, publish manually"
  exit 0
fi

TARBALLS=("$DIST_DIR"/omni-"${VERSION}"-*.tar.gz)
echo "==> creating GitHub release $VERSION"
gh release create "$VERSION" \
  --title "omni $VERSION" \
  --notes "omni $VERSION" \
  "${TARBALLS[@]}"

echo "==> done"
