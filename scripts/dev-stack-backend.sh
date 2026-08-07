#!/usr/bin/env sh

set -eu

mkdir -p internal/web/dist tmp/logs
if [ -z "$(find internal/web/dist -mindepth 1 -print -quit 2>/dev/null)" ]; then
  printf 'ok\n' > internal/web/dist/stub.html
fi

air_config=".air.toml"
case "$(uname -s)" in
  CYGWIN* | MINGW* | MSYS*) air_config=".air.windows.toml" ;;
esac

printf 'backend debug log: %s\n' "${KENN_FORGE_LOG_FILE:-tmp/logs/backend-dev.log}"
printf 'backend console log level: %s\n' "${KENN_FORGE_LOG_STDERR_LEVEL:-info}"
printf 'tail with: tail -F %s\n' "${KENN_FORGE_LOG_FILE:-tmp/logs/backend-dev.log}"

export KENN_FORGE_LOG_LEVEL="${KENN_FORGE_LOG_LEVEL:-debug}"
export KENN_FORGE_LOG_FILE="${KENN_FORGE_LOG_FILE:-tmp/logs/backend-dev.log}"
export KENN_FORGE_LOG_STDERR_LEVEL="${KENN_FORGE_LOG_STDERR_LEVEL:-info}"

air_bin="${AIR_BIN:-}"
if [ -z "$air_bin" ]; then
  air_bin="$(command -v air 2>/dev/null || true)"
fi
if [ -z "$air_bin" ]; then
  exe_suffix=""
  if [ "$(go env GOOS)" = "windows" ]; then
    exe_suffix=".exe"
  fi
  gopath_first="$(go env GOPATH | sed -E 's/^([A-Za-z]:)?([^;:]*).*/\1\2/')"
  if [ -x "$gopath_first/bin/air$exe_suffix" ]; then
    air_bin="$gopath_first/bin/air$exe_suffix"
  fi
fi
if [ -z "$air_bin" ]; then
  printf 'air not found. Install with: make air-install\n' >&2
  exit 1
fi

if [ -n "${KENN_FORGE_CONFIG:-}" ]; then
  exec "$air_bin" -c "$air_config" -- serve -config "$KENN_FORGE_CONFIG" ${BACKEND_ARGS:-}
else
  exec "$air_bin" -c "$air_config" -- serve ${BACKEND_ARGS:-}
fi
