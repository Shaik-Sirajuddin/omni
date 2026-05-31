#!/usr/bin/env bash
# install.sh — curl-pipe entrypoint
# Usage: curl -fsSL https://raw.githubusercontent.com/Shaik-Sirajuddin/memory/main/install.sh | bash
set -euo pipefail

REPO="Shaik-Sirajuddin/memory"
INSTALL_BIN="${INSTALL_BIN:-/usr/local/bin}"

# ── OS / arch detection (linux + wsl only) ────────────────────────────────────
detect_platform() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"

  case "$os" in
    linux) ;;
    *) echo "error: unsupported OS '$os' — only Linux/WSL is supported" >&2; exit 1 ;;
  esac

  case "$arch" in
    x86_64)  arch="amd64" ;;
    aarch64) arch="arm64" ;;
    *) echo "error: unsupported architecture '$arch'" >&2; exit 1 ;;
  esac

  echo "linux/${arch}"
}

# ── Resolve latest release tag ────────────────────────────────────────────────
latest_version() {
  if command -v gh &>/dev/null; then
    gh release view --repo "$REPO" --json tagName -q .tagName 2>/dev/null && return
  fi
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/'
}

# ── Download + extract ────────────────────────────────────────────────────────
download_and_install() {
  local tag="$1" os_arch="$2"
  local os arch
  IFS='/' read -r os arch <<< "$os_arch"

  # goreleaser uses {{ .Version }} (no leading v) in filenames, but {{ .Tag }} in the URL path
  local ver="${tag#v}"
  local tarball="omni-${ver}-${os}-${arch}.tar.gz"
  local url="https://github.com/${REPO}/releases/download/${tag}/${tarball}"
  local tmp_dir
  tmp_dir="$(mktemp -d)"
  # double-quotes expand $tmp_dir now so it isn't unbound when EXIT trap fires
  trap "rm -rf '$tmp_dir'" EXIT

  echo "==> downloading $tarball"
  curl -fsSL "$url" -o "$tmp_dir/$tarball"

  echo "==> extracting"
  tar -xzf "$tmp_dir/$tarball" -C "$tmp_dir"

  # goreleaser archives are flat (no wrapping subdir); setup.sh is at deployment/setup.sh
  echo "==> running setup"

  local can_global=0
  if [[ "$EUID" -eq 0 ]] || sudo -n true 2>/dev/null || sudo -n -l 2>/dev/null | grep -q "NOPASSWD"; then
    can_global=1
  fi

  local do_global="${OMNI_GLOBAL_INSTALL:-}"
  if [[ -z "$do_global" && "$can_global" == "1" ]]; then
    # root/sudo detected — ask user which mode they want
    echo ""
    echo "  Root/sudo access detected. Choose install mode:"
    echo "    [1] User-local  — ~/.local/opt/omni  (default, no system changes)"
    echo "    [2] System-wide — /opt/omni           (installs for all users, requires root)"
    echo ""
    local choice
    read -r -p "  Enter choice [1]: " choice </dev/tty || choice="1"
    [[ "$choice" == "2" ]] && do_global="1" || do_global="0"
  fi

  if [[ "${do_global:-0}" == "1" ]]; then
    if [[ "$EUID" -eq 0 ]]; then
      BIN_DIR="$tmp_dir" OMNI_GLOBAL_INSTALL=1 bash "$tmp_dir/deployment/setup.sh"
    else
      sudo BIN_DIR="$tmp_dir" OMNI_GLOBAL_INSTALL=1 bash "$tmp_dir/deployment/setup.sh"
    fi
  else
    BIN_DIR="$tmp_dir" bash "$tmp_dir/deployment/setup.sh"
  fi
}

# ── Upgrade detection ─────────────────────────────────────────────────────────
current_version() {
  omni --version 2>/dev/null | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true
}

main() {
  local platform version current

  platform="$(detect_platform)"
  version="$(latest_version)"

  if [[ -z "$version" ]]; then
    echo "error: could not resolve latest release version" >&2
    exit 1
  fi

  current="$(current_version)"
  local version_bare="${version#v}"
  local current_bare="${current#v}"
  if [[ -n "$current" && "$current_bare" == "$version_bare" ]]; then
    echo "==> omni $version is already installed and up to date"
    exit 0
  elif [[ -n "$current" ]]; then
    echo "==> upgrading omni $current -> $version"
  else
    echo "==> installing omni $version"
  fi

  download_and_install "$version" "$platform"
  echo "==> done — run 'omni agent list' to get started"
}

main "$@"
