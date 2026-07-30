#!/usr/bin/env bash
set -euo pipefail

cmd="${1:?kenn-forge subcommand is required}"
shift

export PATH="/usr/local/go/bin:${PATH}"

cd /app
exec /data/member-ssh/kenn-forge "${cmd}" --config /app/scripts/e2e/fleet/member-ssh.toml "$@"
