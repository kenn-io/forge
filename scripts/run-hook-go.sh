#!/bin/sh
set -eu

# Go hooks use every CPU by default. Set KENN_FORGE_HOOK_GO_CONCURRENCY to cap
# GOMAXPROCS and GO_TEST_P on a shared host; explicit per-tool values win.
if [ -n "${KENN_FORGE_HOOK_GO_CONCURRENCY:-}" ]; then
  case "$KENN_FORGE_HOOK_GO_CONCURRENCY" in
    *[!0-9]*|0)
      echo "KENN_FORGE_HOOK_GO_CONCURRENCY must be a positive integer" >&2
      exit 2
      ;;
  esac
  export GOMAXPROCS=${GOMAXPROCS:-$KENN_FORGE_HOOK_GO_CONCURRENCY}
  export GO_TEST_P=${GO_TEST_P-$KENN_FORGE_HOOK_GO_CONCURRENCY}
fi

exec "$@"
