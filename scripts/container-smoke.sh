#!/usr/bin/env bash
set -euo pipefail
image=${1:?usage: container-smoke.sh IMAGE}
work=$(mktemp -d)
name="forge-smoke-$$"
cleanup() {
 local result=$?
 if (( result != 0 )); then
  docker inspect "$name" --format '{{json .State.Health}}' >&2 || true
 fi
 docker rm -f "$name" > /dev/null 2>&1 || true
 docker volume rm "$name-home" > /dev/null 2>&1 || true
 rm -rf "$work"
}
trap cleanup EXIT
[[ $(docker run --rm --entrypoint id "$image" -u) == 1000 ]]
docker run --rm "$image" version
[[ $(docker image inspect "$image" --format '{{json .Config.Entrypoint}}') == '["kenn-forge"]' ]]
docker volume create "$name-home" > /dev/null
# Env supplies the health port; no config file, seed, host rewrite or startup script.
docker run -d --name "$name" --cpus 2 --network host \
 -v "$name-home:/home/forge" \
 -e KENN_FORGE_PORT="${FORGE_SMOKE_PORT:-18091}" \
 -e KENN_FORGE_TRUST_REVERSE_PROXY=true \
 "$image" serve --disable-sync > /dev/null
for _ in {1..120}; do
 status=$(docker inspect "$name" --format '{{.State.Health.Status}}')
 [[ $status == healthy ]] && break
 [[ $(docker inspect "$name" --format '{{.State.Running}}') == true ]]
 sleep 1
done
[[ $status == healthy ]]
url="http://127.0.0.1:${FORGE_SMOKE_PORT:-18091}"
authority="127.0.0.1:${FORGE_SMOKE_PORT:-18091}"
[[ $(curl --noproxy '*' -s -o /dev/null -w '%{http_code}' -H "X-Forwarded-Host: $authority" "$url/api/v1/snapshot") == 401 ]]
[[ $(curl --noproxy '*' -s -o /dev/null -w '%{http_code}' -H 'Host: attacker.example' "$url/healthz") == 403 ]]
token=$(docker exec "$name" cat /home/forge/.kenn/forge/auth_token)
[[ -n $token ]]
[[ $(docker exec "$name" stat -c '%a' /home/forge/.kenn/forge/auth_token) == 600 ]]
curl --noproxy '*' -fsS -H "Authorization: Bearer $token" "$url/api/v1/snapshot" > /dev/null
docker exec "$name" kenn-forge daemon status > /dev/null
curl --noproxy '*' -fsS -H "X-Forwarded-Host: $authority" "$url/" > "$work/index.html"
FORGE_SMOKE_TOKEN="$token" node scripts/container-browser-smoke.mjs "$url"
docker restart "$name" > /dev/null
for _ in {1..120}; do
 status=$(docker inspect "$name" --format '{{.State.Health.Status}}')
 [[ $status == healthy ]] && break
 sleep 1
done
[[ $status == healthy ]]
[[ $(docker exec "$name" cat /home/forge/.kenn/forge/auth_token) == "$token" ]]
printf 'container smoke passed: non-root, direct startup, auth, Host, health, discovery, SPA and persisted token\n'
