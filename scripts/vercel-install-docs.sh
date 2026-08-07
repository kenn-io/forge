#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tools_root="$repo_root/.vercel/tools"
tools_bin="$tools_root/bin"
go_root="$tools_root/go"
go_version="1.26.3"
uv_version="0.12.1"

case "$(uname -m)" in
  x86_64)
    go_arch="amd64"
    go_sha256="2b2cfc7148493da5e73981bffbf3353af381d5f93e789c82c79aff64962eb556"
    uv_target="uv-x86_64-unknown-linux-gnu"
    uv_sha256="90b2f223fb69d19db49e117da601f64978593417988530aa733d456141b4bcbb"
    ;;
  aarch64 | arm64)
    go_arch="arm64"
    go_sha256="9d89a3ea57d141c2b22d70083f2c8459ba3890f2d9e818e7e933b75614936565"
    uv_target="uv-aarch64-unknown-linux-gnu"
    uv_sha256="769d373e146692c639b5fbaae33b331c297a32e03d30448772051902df52bbf4"
    ;;
  *)
    echo "unsupported Vercel build architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

install_system_packages() {
  if [ "$(id -u)" -eq 0 ]; then
    dnf install -y tmux nspr nss poppler-utils
  else
    sudo dnf install -y tmux nspr nss poppler-utils
  fi
}

mkdir -p "$tools_bin"
install_system_packages

go_archive="$tools_root/go${go_version}.linux-${go_arch}.tar.gz"
curl -fsSL "https://go.dev/dl/go${go_version}.linux-${go_arch}.tar.gz" -o "$go_archive"
printf '%s  %s\n' "$go_sha256" "$go_archive" | sha256sum --check --status
rm -rf "$go_root"
tar -C "$tools_root" -xzf "$go_archive"
rm -f "$go_archive"

uv_archive="$tools_root/${uv_target}.tar.gz"
curl -fsSL "https://github.com/astral-sh/uv/releases/download/${uv_version}/${uv_target}.tar.gz" -o "$uv_archive"
printf '%s  %s\n' "$uv_sha256" "$uv_archive" | sha256sum --check --status
tar -C "$tools_root" -xzf "$uv_archive"
install -m 0755 "$tools_root/$uv_target/uv" "$tools_bin/uv"
install -m 0755 "$tools_root/$uv_target/uvx" "$tools_bin/uvx"
rm -rf "${tools_root:?}/${uv_target:?}"
rm -f "$uv_archive"

cd "$repo_root"
bun install --frozen-lockfile
node node_modules/.bin/playwright install chromium
