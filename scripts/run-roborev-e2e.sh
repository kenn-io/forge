#!/usr/bin/env bash
set -euo pipefail

# Run roborev e2e tests against a real daemon in Docker.
# Works both locally and in CI.
#
# Required env:
#   ROBOREV_SRC   - path to roborev source checkout
#
# Optional env:
#   ROBOREV_REF   - git ref to build (default: main)
#   ROBOREV_PORT  - host port for daemon (default: 17373)
#   SKIP_BUILD    - set to 1 to skip frontend/server build

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ROBOREV_REF="${ROBOREV_REF:-main}"
ROBOREV_PORT="${ROBOREV_PORT:-17373}"
DB_PATH="$(mktemp /tmp/roborev-e2e-XXXXXX.db)"
ENV_FILE="$(mktemp /tmp/roborev-env-XXXXXX)"

cleanup() {
  echo "--- cleanup ---"
  cd "$REPO_ROOT/tests/integration" && \
    docker compose --env-file "$ENV_FILE" down -v 2>/dev/null || true
  rm -f "$DB_PATH" "${DB_PATH}-wal" "${DB_PATH}-shm" "$ENV_FILE"
}
trap cleanup EXIT

# 1. Build frontend + e2e server (unless SKIP_BUILD=1)
if [ "${SKIP_BUILD:-}" != "1" ]; then
  echo "--- build frontend ---"
  cd "$REPO_ROOT" && bun install --frozen-lockfile
  cd "$REPO_ROOT/frontend" && ../node_modules/.bin/vp build --logLevel warn
  rm -rf "$REPO_ROOT/internal/web/dist"
  cp -r "$REPO_ROOT/frontend/dist" "$REPO_ROOT/internal/web/dist"

  echo "--- build e2e server ---"
  cd "$REPO_ROOT"
  go build -o ./cmd/e2e-server/e2e-server ./cmd/e2e-server
fi

# 2. Seed roborev database
echo "--- seed database ---"
cd "$REPO_ROOT"
go run ./internal/testutil/cmd/seed-roborev -out "$DB_PATH"

# 3. Write env file for docker compose.
# Uses a per-run temp file — never touches tests/integration/.env.
printf 'ROBOREV_SRC=%s\nROBOREV_REF=%s\nROBOREV_DB_PATH=%s\nCOMPOSE_DIR=%s\nROBOREV_PORT=%s\n' \
  "$ROBOREV_SRC" "$ROBOREV_REF" "$DB_PATH" \
  "$REPO_ROOT/tests/integration" "$ROBOREV_PORT" \
  > "$ENV_FILE"

# 4. Start roborev daemon in Docker
echo "--- start daemon (ref=$ROBOREV_REF, port=$ROBOREV_PORT) ---"
cd "$REPO_ROOT/tests/integration" && \
  docker compose --env-file "$ENV_FILE" up -d --build --wait

# 5. Install Playwright browsers if needed
echo "--- install playwright ---"
cd "$REPO_ROOT/frontend" && bunx playwright install --with-deps chromium

# 6. Run tests — pass env file path so helpers can read it
echo "--- run tests ---"
cd "$REPO_ROOT/frontend"
ROBOREV_ENDPOINT="http://127.0.0.1:$ROBOREV_PORT" \
ROBOREV_ENV_FILE="$ENV_FILE" \
  bun run playwright test \
  --config=playwright-e2e.config.ts \
  --project=roborev
