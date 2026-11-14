#!/usr/bin/env bash
# Run scripts/generate-docs-screenshots.mjs inside docs/screenshots/Dockerfile
# and write the published docs-assets set to OUTPUT_DIR/assets. The host needs
# only Bash and Docker.
set -euo pipefail

output="${1:?usage: run-in-docker.sh OUTPUT_DIR}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
image=forge-docs-screenshots

mkdir -p "$output"
output="$(cd "$output" && pwd -P)"
docker build -t "$image" "$script_dir"
# Run as the caller so every generated file stays removable without root. The
# generator only replaces directories under the temporary directory.
docker run --rm --init --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -v "$repo_root:/src" -v "$output:/tmp/out" -w /src "$image" \
    bash -c 'bun install && node scripts/generate-docs-screenshots.mjs /tmp/out/assets'
