#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PATH="$repo_root/.vercel/tools/bin:$PATH"

cd "$repo_root"
node scripts/build-docs.mjs
