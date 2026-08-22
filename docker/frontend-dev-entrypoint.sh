#!/bin/sh

set -eu

cd /app/frontend

if [ -n "${BUN_INSTALL_FLAGS:-}" ]; then
  # Intentional word splitting for CLI flags.
  bun install ${BUN_INSTALL_FLAGS}
else
  bun install
fi

if [ -n "${FRONTEND_DEV_ARGS:-}" ]; then
  # Intentional word splitting for CLI args.
  exec node ../node_modules/vite-plus/bin/vp dev ${FRONTEND_DEV_ARGS}
fi

exec node ../node_modules/vite-plus/bin/vp dev
