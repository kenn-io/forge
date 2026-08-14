#!/bin/sh
set -eu

hook_concurrency=${KENN_FORGE_HOOK_GO_CONCURRENCY:-4}
case "$hook_concurrency" in
  ''|*[!0-9]*|0)
    echo "KENN_FORGE_HOOK_GO_CONCURRENCY must be a positive integer" >&2
    exit 2
    ;;
esac

export GOMAXPROCS=${GOMAXPROCS:-$hook_concurrency}
export GO_TEST_P=${GO_TEST_P-$hook_concurrency}

exec "$@"
