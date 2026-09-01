#!/usr/bin/env bash
set -euo pipefail

role="${1:?role is required}"
data_dir="/data/${role}"
config="${data_dir}/${role}.toml"
binary="${data_dir}/kenn-forge"

mkdir -p "${data_dir}"

prepare_config() {
  if [[ -f "${config}" ]]; then
    return
  fi
  if [[ "${role}" != "hub" ]]; then
    cp "/app/scripts/e2e/fleet/${role}.toml" "${config}"
    return
  fi
  local host_port="${KENN_FORGE_FLEET_HOST_PORT:-}"
  if [[ -z "${host_port}" ]]; then
    echo "KENN_FORGE_FLEET_HOST_PORT is required for the hub" >&2
    exit 1
  fi
  {
    printf 'allowed_hosts = ["hub:18091", "localhost:%s", "127.0.0.1:%s"]\n' "${host_port}" "${host_port}"
    cat /app/scripts/e2e/fleet/hub.toml
  } > "${config}"
}

prepare_certificate() {
  if [[ -s /pki/cert.pem && -s /pki/key.pem ]]; then
    return
  fi
  if mkdir /pki/generate.lock 2>/dev/null; then
    openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
      -subj "/CN=kenn-forge-fleet-e2e" \
      -addext "subjectAltName=DNS:hub,DNS:member,IP:127.0.0.1" \
      -keyout /pki/key.pem -out /pki/cert.pem >/dev/null 2>&1
    chmod 0600 /pki/key.pem
    chmod 0644 /pki/cert.pem
    rmdir /pki/generate.lock
    return
  fi
  for _ in $(seq 1 120); do
    if [[ -s /pki/cert.pem && -s /pki/key.pem ]]; then
      return
    fi
    sleep 1
  done
  echo "timed out waiting for the fleet test certificate" >&2
  exit 1
}

prepare_config
prepare_certificate

cleanup() {
  if [[ -n "${forge_pid:-}" ]]; then
    kill "${forge_pid}" 2>/dev/null || true
  fi
  if [[ -n "${proxy_pid:-}" ]]; then
    kill "${proxy_pid}" 2>/dev/null || true
  fi
  if [[ -n "${published_proxy_pid:-}" ]]; then
    kill "${published_proxy_pid}" 2>/dev/null || true
  fi
  if [[ -n "${git_daemon_pid:-}" ]]; then
    kill "${git_daemon_pid}" 2>/dev/null || true
  fi
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

go_build_args=(-o "${binary}")
if [[ -n "${KENN_FORGE_GO_BUILD_TAGS:-}" ]]; then
  go_build_args=(-tags "${KENN_FORGE_GO_BUILD_TAGS}" "${go_build_args[@]}")
fi
go build "${go_build_args[@]}" ./cmd/kenn-forge

"${binary}" serve --config "${config}" &
forge_pid=$!

for _ in $(seq 1 120); do
  if curl -fsS http://127.0.0.1:8091/healthz >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "${forge_pid}" 2>/dev/null; then
    wait "${forge_pid}"
    exit $?
  fi
  sleep 1
done

curl -fsS http://127.0.0.1:8091/healthz >/dev/null
socat OPENSSL-LISTEN:18091,fork,reuseaddr,bind=0.0.0.0,cert=/pki/cert.pem,key=/pki/key.pem,verify=0 TCP:127.0.0.1:8091 &
proxy_pid=$!

if [[ "${role}" == "member" ]]; then
  mkdir -p /data/member/worktrees
  git daemon --reuseaddr --export-all --base-path=/data/member/worktrees \
    --listen=0.0.0.0 --port=9418 /data/member/worktrees &
  git_daemon_pid=$!
fi

if [[ "${role}" == "hub" ]]; then
  socat TCP-LISTEN:18092,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:8091 &
  published_proxy_pid=$!
fi

if [[ -n "${published_proxy_pid:-}" ]]; then
  wait -n "${forge_pid}" "${proxy_pid}" "${published_proxy_pid}"
elif [[ -n "${git_daemon_pid:-}" ]]; then
  wait -n "${forge_pid}" "${proxy_pid}" "${git_daemon_pid}"
else
  wait -n "${forge_pid}" "${proxy_pid}"
fi
