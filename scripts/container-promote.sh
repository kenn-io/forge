#!/usr/bin/env bash
set -euo pipefail

# Pending CI jobs can be coalesced. Every job reconciles the current stable tag,
# regardless of which release triggered it, while holding the promotion lock.
current=$(gh api repos/kenn-io/forge/releases/latest --jq .tag_name)
[[ "$current" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]
docker buildx imagetools create --tag ghcr.io/kenn-io/forge:latest "ghcr.io/kenn-io/forge:$current"
