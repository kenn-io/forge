#!/usr/bin/env bash
# Simulates a host reboot against a real kenn-forge daemon in Docker and checks
# that launched agent sessions resume afterwards. Each scenario kills the
# daemon, tmux, and agents in a different order, then cold-boots the container
# with only the data volume surviving.
#
# Usage: scripts/reboot-sim.sh [daemon-first|agents-first|all]...
# Env:   WORKSPACES (default 4), KEEP=1 keeps containers for inspection.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
workspaces="${WORKSPACES:-4}"
image="kenn-forge-rebootsim"
scenarios=("$@")
if [ "${#scenarios[@]}" -eq 0 ] || [ "${scenarios[0]}" = "all" ]; then
  scenarios=(daemon-first agents-first)
fi

build_image() {
  local context
  context="$(mktemp -d)"
  trap 'rm -rf "$context"' RETURN
  (cd "$repo_root" &&
    CGO_ENABLED=0 GOOS=linux go build -o "$context/kenn-forge" ./cmd/kenn-forge &&
    CGO_ENABLED=0 GOOS=linux go build -o "$context/rebootsim" ./tools/rebootsim)
  install -m 0755 "$repo_root/tools/rebootsim/fake-agent.sh" "$context/fake-agent"
  docker build -q -t "$image" -f "$repo_root/tools/rebootsim/Dockerfile" "$context" >/dev/null
}

in_box() {
  docker exec "$box" "$@"
}

api() {
  local method="$1" path="$2"
  shift 2
  in_box sh -c 'url="http://127.0.0.1:8091/api/v1$1"; shift
    curl -fsS -X "$0" -H "Authorization: Bearer $(cat /data/auth_token)" \
      -H "Content-Type: application/json" "$url" "$@"' "$method" "$path" "$@"
}

boot_daemon() {
  docker exec -d -e KENN_FORGE_DEV_RESTART=1 "$box" sh -c \
    'exec kenn-forge serve --config /data/config.toml --disable-sync >>/data/daemon.log 2>&1'
  for _ in $(seq 100); do
    if api GET /version >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.2
  done
  echo "daemon did not become ready" >&2
  in_box tail -20 /data/daemon.log >&2
  return 1
}

agent_rows() {
  in_box sqlite3 "file:/data/forge.db?mode=ro" \
    "select count(*) from forge_workspace_runtime_sessions where kind='agent'"
}

report_count() {
  in_box sh -c 'ls /data/agent-activity/*.json 2>/dev/null | wc -l'
}

wait_for() {
  local description="$1" check="$2"
  for _ in $(seq 150); do
    if eval "$check"; then
      return 0
    fi
    sleep 0.2
  done
  echo "timed out waiting for $description" >&2
  return 1
}

shutdown_host() {
  case "$1" in
    daemon-first)
      # systemd order: the service stops, then the session teardown kills the
      # tmux server and agents while no daemon is listening for their hooks.
      in_box pkill -TERM -x kenn-forge
      wait_for "daemon exit" '! in_box pgrep -x kenn-forge >/dev/null'
      ;;
    agents-first)
      # Worst case: agents die while the daemon is still running, so their
      # exits and SessionEnd hooks reach it before it stops.
      # kill-server can exit non-zero after it succeeds, when its own client
      # loses the server mid-command, so check that the server is gone.
      in_box tmux -L kenn-forge kill-server || true
      if in_box tmux -L kenn-forge list-sessions >/dev/null 2>&1; then
        echo "tmux server survived kill-server" >&2
        return 1
      fi
      sleep 3
      in_box pkill -TERM -x kenn-forge || true
      ;;
    *)
      echo "unknown scenario: $1" >&2
      return 1
      ;;
  esac
  docker stop -t 2 "$box" >/dev/null
}

resumed_count() {
  in_box sh -c 'cat /data/worktrees/sim/*/.rebootsim-agent.log 2>/dev/null' | grep -c '^resume ' || true
}

# Runs on every scenario exit, including failed setup, so a broken run does
# not leave its daemon, agents, and volume behind.
remove_scenario() {
  local status=$?
  if [ "${KEEP:-}" != "1" ]; then
    docker rm -f "$box" >/dev/null 2>&1 || true
    docker volume rm "$box" >/dev/null 2>&1 || true
  fi
  return "$status"
}

run_scenario() {
  local scenario="$1"
  box="kenn-forge-rebootsim-$scenario"
  docker rm -f "$box" >/dev/null 2>&1 || true
  docker volume rm "$box" >/dev/null 2>&1 || true
  trap remove_scenario EXIT
  docker run -d --init --name "$box" -v "$box:/data" "$image" >/dev/null

  local ids
  ids="$(in_box rebootsim -data /data -n "$workspaces")"
  boot_daemon
  for id in $ids; do
    api POST "/workspaces/$id/runtime/sessions" -d '{"target_key":"claude"}' >/dev/null
  done
  wait_for "hook reports" '[ "$(report_count)" -ge "$workspaces" ]'
  local before_rows before_reports
  before_rows="$(agent_rows)"
  before_reports="$(report_count)"

  shutdown_host "$scenario"
  docker start "$box" >/dev/null
  local boot_rows boot_reports
  boot_rows="$(agent_rows)"
  boot_reports="$(report_count)"
  boot_daemon

  wait_for "resumed agents" '[ "$(resumed_count)" -ge "$workspaces" ]' || true
  local resumed
  resumed="$(resumed_count)"
  local same_conversation=0
  for id in $ids; do
    if in_box awk '$1 == "start" { s = $2 } $1 == "resume" && $2 == s { ok = 1 } END { exit !ok }' \
      "/data/worktrees/sim/$id/.rebootsim-agent.log"; then
      same_conversation=$((same_conversation + 1))
    fi
  done

  printf '%-13s rows %s->%s  reports %s->%s  resumed %s/%s  same-conversation %s/%s\n' \
    "$scenario" "$before_rows" "$boot_rows" "$before_reports" "$boot_reports" \
    "$resumed" "$workspaces" "$same_conversation" "$workspaces"
  in_box grep -E 'resumed workspace agent|could not resume|runtime session unavailable|restore' \
    /data/daemon.log | sed 's/^/    /' | head -20 || true

  [ "$same_conversation" -eq "$workspaces" ]
}

# Each scenario runs in its own process so `set -e` stops a broken setup
# instead of being suspended by the caller's `||`.
if [ "${1:-}" = "--scenario" ]; then
  run_scenario "$2"
  exit
fi
build_image
status=0
for scenario in "${scenarios[@]}"; do
  "$0" --scenario "$scenario" || status=1
done
exit "$status"
