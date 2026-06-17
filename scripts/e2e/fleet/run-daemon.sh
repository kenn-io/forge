#!/usr/bin/env bash
set -euo pipefail

role="${1:?role is required}"
config="/app/scripts/e2e/fleet/${role}.toml"
data_dir="/data/${role}"
binary="${data_dir}/middleman"

mkdir -p "${data_dir}"

prepare_hub_config() {
  local host_port="${MIDDLEMAN_FLEET_HOST_PORT:-}"
  if [[ -z "${host_port}" ]]; then
    return
  fi
  config="${data_dir}/hub.toml"
  {
    printf 'allowed_hosts = ["localhost:%s", "127.0.0.1:%s"]\n' "${host_port}" "${host_port}"
    cat /app/scripts/e2e/fleet/hub.toml
  } > "${config}"
}

setup_hub_ssh_client() {
  mkdir -p /root/.ssh /fleet-ssh
  chmod 700 /root/.ssh
  if [[ ! -f /root/.ssh/id_ed25519 ]]; then
    ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519 >/dev/null
  fi
  cp /root/.ssh/id_ed25519.pub /fleet-ssh/hub.pub
  cat > /root/.ssh/config <<'EOF'
Host member-ssh
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
EOF
  chmod 600 /root/.ssh/config
}

setup_member_sshd() {
  mkdir -p /root/.ssh /fleet-ssh /run/sshd
  chmod 700 /root/.ssh
  for _ in $(seq 1 120); do
    if [[ -s /fleet-ssh/hub.pub ]]; then
      break
    fi
    sleep 1
  done
  test -s /fleet-ssh/hub.pub
  cp /fleet-ssh/hub.pub /root/.ssh/authorized_keys
  chmod 600 /root/.ssh/authorized_keys
  ssh-keygen -A >/dev/null
  /usr/sbin/sshd -D -e &
  sshd_pid=$!
}

case "${role}" in
  hub)
    prepare_hub_config
    setup_hub_ssh_client
    ;;
  member-ssh)
    setup_member_sshd
    ;;
esac

cleanup() {
  if [[ -n "${middleman_pid:-}" ]]; then
    kill "${middleman_pid}" 2>/dev/null || true
  fi
  if [[ -n "${proxy_pid:-}" ]]; then
    kill "${proxy_pid}" 2>/dev/null || true
  fi
  if [[ -n "${sshd_pid:-}" ]]; then
    kill "${sshd_pid}" 2>/dev/null || true
  fi
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

go build -o "${binary}" ./cmd/middleman

"${binary}" serve --config "${config}" &
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
