#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tools_root="$repo_root/.vercel/tools"
tools_bin="$tools_root/bin"
go_root="$tools_root/go"
go_version="1.26.3"

case "$(uname -m)" in
  x86_64)
    go_arch="amd64"
    ;;
  aarch64 | arm64)
    go_arch="arm64"
    ;;
  *)
    echo "unsupported Vercel build architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

install_system_packages() {
  if [ "$(id -u)" -eq 0 ]; then
    dnf install -y tmux nspr nss
  else
    sudo dnf install -y tmux nspr nss
  fi
}

mkdir -p "$tools_bin"
install_system_packages

go_archive="$tools_root/go${go_version}.linux-${go_arch}.tar.gz"
curl -fsSL "https://go.dev/dl/go${go_version}.linux-${go_arch}.tar.gz" -o "$go_archive"
rm -rf "$go_root"
tar -C "$tools_root" -xzf "$go_archive"
rm -f "$go_archive"

curl -LsSf https://astral.sh/uv/install.sh \
  | env UV_INSTALL_DIR="$tools_bin" UV_NO_MODIFY_PATH=1 sh

cd "$repo_root"
bun install --frozen-lockfile
node node_modules/.bin/playwright install chromium
