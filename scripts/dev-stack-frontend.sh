#!/usr/bin/env sh

set -eu

mkdir -p tmp/logs
bun install ${BUN_INSTALL_FLAGS:-}
cd frontend
exec ../node_modules/.bin/vp dev -- ${FRONTEND_ARGS:-}
