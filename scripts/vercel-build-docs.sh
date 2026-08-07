#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PATH="$repo_root/.vercel/tools/go/bin:$repo_root/.vercel/tools/bin:$PATH"

cd "$repo_root"
node scripts/build-frontend.mjs
node scripts/check-asset-base-paths.mjs
rm -rf internal/web/dist
cp -r frontend/dist internal/web/dist
printf 'ok\n' > internal/web/dist/stub.html

mkdir -p .vercel/bin
GOFLAGS="${GOFLAGS:+$GOFLAGS }-buildvcs=false" \
  go build -o .vercel/bin/e2e-server ./cmd/e2e-server

KENN_FORGE_DOCS_SITE_PROJECT="${KENN_FORGE_DOCS_SITE_PROJECT:-chromium}" \
  PLAYWRIGHT_E2E_SERVER_BINARY="$repo_root/.vercel/bin/e2e-server" \
  node scripts/build-docs.mjs
