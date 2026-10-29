#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tools_root="$repo_root/.vercel/tools"
tools_bin="$tools_root/bin"
uv_version="0.13.4"

case "$(uname -m)" in
  x86_64)
    uv_target="uv-x86_64-unknown-linux-gnu"
    uv_sha256="2b428396f12572d4429cf210ca234dd599a33b3af0958a4d5ed569d63e3f344f"
    ;;
  aarch64 | arm64)
    uv_target="uv-aarch64-unknown-linux-gnu"
    uv_sha256="1ae72b4718729e2ccc4f1ae2c6d1e531ca8467800e0546e9130b3886023a60ab"
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
