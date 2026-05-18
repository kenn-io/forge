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

printf 'backend debug log: %s\n' "${MIDDLEMAN_LOG_FILE:-tmp/logs/backend-dev.log}"
printf 'backend console log level: %s\n' "${MIDDLEMAN_LOG_STDERR_LEVEL:-info}"
printf 'tail with: tail -F %s\n' "${MIDDLEMAN_LOG_FILE:-tmp/logs/backend-dev.log}"

export MIDDLEMAN_LOG_LEVEL="${MIDDLEMAN_LOG_LEVEL:-debug}"
export MIDDLEMAN_LOG_FILE="${MIDDLEMAN_LOG_FILE:-tmp/logs/backend-dev.log}"
export MIDDLEMAN_LOG_STDERR_LEVEL="${MIDDLEMAN_LOG_STDERR_LEVEL:-info}"

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

if [ -n "${MIDDLEMAN_CONFIG:-}" ]; then
  exec "$air_bin" -c "$air_config" -- -config "$MIDDLEMAN_CONFIG" ${BACKEND_ARGS:-}
else
  exec "$air_bin" -c "$air_config" -- ${BACKEND_ARGS:-}
fi
