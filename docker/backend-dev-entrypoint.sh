#!/bin/sh

set -eu

socat_pid=""
air_pid=""
terminated=0

forward_term() {
  terminated=1
  if [ -n "$air_pid" ]; then
    kill -TERM "$air_pid" 2>/dev/null || true
  fi
  if [ -n "$socat_pid" ]; then
    kill -TERM "$socat_pid" 2>/dev/null || true
  fi
}

cleanup() {
  forward_term
  if [ -n "$air_pid" ]; then
    wait "$air_pid" 2>/dev/null || true
  fi
  if [ -n "$socat_pid" ]; then
    wait "$socat_pid" 2>/dev/null || true
  fi
}

trap 'forward_term' INT TERM
trap 'cleanup' EXIT

air_bin="${AIR_BIN:-/go/bin/air}"
go_bin="${GO_BIN:-go}"
config_path="${KENN_FORGE_CONFIG_PATH:-/data/config.toml}"
backend_port="$($go_bin run ./cmd/kenn-forge config read -config "$config_path" port 2>/dev/null || true)"
case "$backend_port" in
  ''|*[!0-9]*)
    backend_port=8091
    ;;
esac

socat -d0 TCP-LISTEN:18091,fork,bind=0.0.0.0,reuseaddr TCP:127.0.0.1:${backend_port} &
socat_pid=$!

"$air_bin" -c .air.toml -- -config "$config_path" &
air_pid=$!

set +e
wait "$air_pid"
status=$?
set -e

if [ -n "$socat_pid" ]; then
  kill -TERM "$socat_pid" 2>/dev/null || true
  wait "$socat_pid" 2>/dev/null || true
fi

if [ "$terminated" -ne 0 ] && [ "$status" -eq 143 ]; then
  exit 0
fi

exit "$status"
