#!/usr/bin/env bash
set -euo pipefail

role="${1:?role is required}"
config="/app/scripts/e2e/fleet/${role}.toml"
data_dir="/data/${role}"

mkdir -p "${data_dir}"

cleanup() {
  if [[ -n "${middleman_pid:-}" ]]; then
    kill "${middleman_pid}" 2>/dev/null || true
  fi
  if [[ -n "${proxy_pid:-}" ]]; then
    kill "${proxy_pid}" 2>/dev/null || true
  fi
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

go run ./cmd/middleman serve --config "${config}" &
middleman_pid=$!

for _ in $(seq 1 120); do
  if curl -fsS http://127.0.0.1:8091/healthz >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "${middleman_pid}" 2>/dev/null; then
    wait "${middleman_pid}"
    exit $?
  fi
  sleep 1
done

curl -fsS http://127.0.0.1:8091/healthz >/dev/null
socat TCP-LISTEN:18091,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:8091 &
proxy_pid=$!

wait -n "${middleman_pid}" "${proxy_pid}"
