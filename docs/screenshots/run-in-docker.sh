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
# Run as the caller so every generated file stays removable without root.
# Install and build in a copy inside the container, so the checkout's own
# node_modules and build output stay untouched.
docker run --rm --init --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -v "$repo_root:/src:ro" -v "$output:/tmp/out" "$image" \
    bash -c 'mkdir /tmp/work &&
        tar -C /src --exclude=node_modules -cf - . | tar -C /tmp/work -xf - &&
        cd /tmp/work && bun install &&
        node scripts/generate-docs-screenshots.mjs /tmp/out/assets'
