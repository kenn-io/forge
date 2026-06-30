#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel 2>/dev/null)" || exit 0
cd "$root"

if [ -f node_modules/vite-plus/bin/vp ]; then
  exit 0
fi

if command -v vp >/dev/null 2>&1; then
  exec vp install --frozen-lockfile
fi

exec bun install --frozen-lockfile
