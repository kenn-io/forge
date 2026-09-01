#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tools_root="$repo_root/.vercel/tools"
tools_bin="$tools_root/bin"
uv_version="0.12.1"

case "$(uname -m)" in
  x86_64)
    uv_target="uv-x86_64-unknown-linux-gnu"
    uv_sha256="90b2f223fb69d19db49e117da601f64978593417988530aa733d456141b4bcbb"
    ;;
  aarch64 | arm64)
    uv_target="uv-aarch64-unknown-linux-gnu"
    uv_sha256="769d373e146692c639b5fbaae33b331c297a32e03d30448772051902df52bbf4"
    ;;
  *)
    echo "unsupported Vercel build architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

mkdir -p "$tools_bin"

uv_archive="$tools_root/${uv_target}.tar.gz"
curl -fsSL "https://github.com/astral-sh/uv/releases/download/${uv_version}/${uv_target}.tar.gz" -o "$uv_archive"
printf '%s  %s\n' "$uv_sha256" "$uv_archive" | sha256sum --check --status
tar -C "$tools_root" -xzf "$uv_archive"
install -m 0755 "$tools_root/$uv_target/uv" "$tools_bin/uv"
install -m 0755 "$tools_root/$uv_target/uvx" "$tools_bin/uvx"
rm -rf "${tools_root:?}/${uv_target:?}"
rm -f "$uv_archive"
