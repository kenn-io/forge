#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
GITLAB_VERSION="${GITLAB_VERSION:-19.1.1-ce.0}"
GITLAB_ROOT_PASSWORD="${GITLAB_ROOT_PASSWORD:-V9q!T3m#R7p-L2x@N6s}"
RUNTIME_IMAGE="${GITLAB_FIXTURE_RUNTIME_IMAGE:-middleman/gitlab-ce-fixture-runtime:$GITLAB_VERSION}"
FIXTURE_IMAGE="${GITLAB_FIXTURE_IMAGE:-middleman/gitlab-ce-fixture:$GITLAB_VERSION}"
CONTAINER_NAME="middleman-gitlab-fixture-bake-${GITLAB_VERSION//[^a-zA-Z0-9_.-]/-}"
DATA_DIR="$ROOT_DIR/scripts/e2e/gitlab/fixture-data"
MANIFEST_PATH="$DATA_DIR/manifest.json"

cleanup() {
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker buildx bake -f "$ROOT_DIR/docker-bake.hcl" gitlab-fixture-runtime
rm -rf "$DATA_DIR"
mkdir -p "$DATA_DIR"
cleanup

container_id="$(
  docker run -d \
    --name "$CONTAINER_NAME" \
    -e GITLAB_ROOT_PASSWORD="$GITLAB_ROOT_PASSWORD" \
    -e GITLAB_OMNIBUS_CONFIG="external_url 'http://localhost'
gitlab_rails['initial_root_password'] = '$GITLAB_ROOT_PASSWORD'
letsencrypt['enable'] = false
prometheus_monitoring['enable'] = false
puma['worker_processes'] = 0
sidekiq['concurrency'] = 5
" \
    -p 127.0.0.1::80 \
    "$RUNTIME_IMAGE"
)"

host_port="$(docker inspect "$container_id" --format '{{(index (index .NetworkSettings.Ports "80/tcp") 0).HostPort}}')"
export GITLAB_URL="http://127.0.0.1:$host_port"
export GITLAB_ROOT_PASSWORD
export GITLAB_READY_TIMEOUT_SECONDS="${GITLAB_READY_TIMEOUT_SECONDS:-1200}"
"$ROOT_DIR/scripts/e2e/gitlab/wait.sh"
"$ROOT_DIR/scripts/e2e/gitlab/bootstrap.sh" "$MANIFEST_PATH"

docker exec "$CONTAINER_NAME" gitlab-ctl stop >/dev/null

docker run --rm --volumes-from "$CONTAINER_NAME" -v "$DATA_DIR:/out" alpine:3.22 \
  sh -c 'tar -C /etc/gitlab -czf /out/etc-gitlab.tgz . && tar -C /var/opt/gitlab -czf /out/var-opt-gitlab.tgz .'

docker buildx bake -f "$ROOT_DIR/docker-bake.hcl" gitlab-fixture
printf 'Built %s from %s\n' "$FIXTURE_IMAGE" "$RUNTIME_IMAGE"
