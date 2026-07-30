#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${KENN_FORGE_GITLAB_COMPOSE_FILE:-$SCRIPT_DIR/docker-compose.yml}"
GITLAB_URL="${GITLAB_URL:-http://127.0.0.1:${GITLAB_HTTP_PORT:-18080}}"
TIMEOUT_SECONDS="${GITLAB_READY_TIMEOUT_SECONDS:-1200}"
DEADLINE=$((SECONDS + TIMEOUT_SECONDS))

while [ "$SECONDS" -lt "$DEADLINE" ]; do
  if curl -fsSI "$GITLAB_URL/users/sign_in" 2>/dev/null | grep -qi '^X-Gitlab-Meta:'; then
    printf 'GitLab ready at %s\n' "$GITLAB_URL"
    exit 0
  fi
  sleep 5
done

printf 'Timed out waiting for GitLab at %s after %ss\n' "$GITLAB_URL" "$TIMEOUT_SECONDS" >&2
docker compose -f "$COMPOSE_FILE" logs --tail=120 gitlab >&2 || true
exit 1
