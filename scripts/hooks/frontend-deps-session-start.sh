#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel 2>/dev/null)" || exit 0
cd "$root"

exec make frontend-deps
