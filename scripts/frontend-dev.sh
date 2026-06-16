#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
log_dir="$repo_root/tmp/logs"
log_file="$log_dir/frontend-dev.log"

mkdir -p "$log_dir"

if [ -d "$HOME/.local/share/mise/installs/bun/latest/bin" ]; then
  PATH="$HOME/.local/share/mise/installs/bun/latest/bin:$PATH"
fi
if [ -d "$HOME/.local/share/mise/installs/node/24.16.0/bin" ]; then
  PATH="$HOME/.local/share/mise/installs/node/24.16.0/bin:$PATH"
fi
export PATH

if command -v bun-latest >/dev/null 2>&1; then
  bun_latest="bun-latest"
elif [ -x "$HOME/.local/share/mise/installs/bun/latest/bin/bun-latest" ]; then
  bun_latest="$HOME/.local/share/mise/installs/bun/latest/bin/bun-latest"
else
  echo "bun-latest not found; install it with: bunx bun-pr main" >&2
  exit 1
fi

if ! command -v node >/dev/null 2>&1; then
  echo "node not found; Vite+ still requires node on PATH while running under bun-latest" >&2
  exit 1
fi

cd "$repo_root"
"$bun_latest" install --no-save ${BUN_INSTALL_FLAGS:-}
cd frontend
"$bun_latest" ../node_modules/vite-plus/bin/vp dev -- ${@:+"$@"} 2>&1 | tee "$log_file"
