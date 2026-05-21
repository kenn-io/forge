#!/bin/sh

set -eu

state_dir="tmp/air"
input_hash_file="$state_dir/openapi-inputs.sha256"
frontend_spec="frontend/openapi/openapi.yaml"
backend_spec="internal/apiclient/spec/openapi.json"
frontend_schema="packages/ui/src/api/generated/schema.ts"
frontend_client="packages/ui/src/api/generated/client.ts"

mkdir -p "$state_dir"

resolve_go_bin() {
  if command -v go >/dev/null 2>&1; then
    command -v go
    return
  fi

  if [ -x /usr/local/go/bin/go ]; then
    printf '%s\n' /usr/local/go/bin/go
    return
  fi

  printf '%s\n' "go toolchain not found" >&2
  exit 127
}

resolve_bun_bin() {
  if command -v bun >/dev/null 2>&1; then
    command -v bun
    return
  fi

  printf '%s\n' "bun runtime not found" >&2
  exit 127
}

GO_BIN="$(resolve_go_bin)"
BUN_BIN="$(resolve_bun_bin)"
exe_suffix=""

if [ "$("$GO_BIN" env GOOS)" = "windows" ]; then
  exe_suffix=".exe"
fi

compute_inputs_hash() {
  {
    printf '%s\n' "go.mod" "go.sum"
    find cmd/middleman-openapi internal/server -type f -name '*.go' | sort
  } | while IFS= read -r path; do
    [ -f "$path" ] || continue
    shasum -a 256 "$path"
  done | shasum -a 256 | awk '{print $1}'
}

write_if_changed() {
  destination="$1"
  source="$2"

  if [ -f "$destination" ] && cmp -s "$destination" "$source"; then
    rm -f "$source"
    return 1
  fi

  mv "$source" "$destination"
  return 0
}

generate_frontend_client() {
  tmp_client="$(mktemp "$state_dir/frontend-client.XXXXXX")"

  cat > "$tmp_client" <<'EOF'
/**
 * This file was auto-generated from frontend/openapi/openapi.yaml.
 * Do not make direct changes to the file.
 */

import createClient, { type ClientOptions } from "openapi-fetch";
import type { paths } from "./schema";

export function createAPIClient(baseUrl: string, options: Pick<ClientOptions, "fetch" | "querySerializer"> = {}) {
  return createClient<paths>({ baseUrl, ...options });
}
EOF

  write_if_changed "$frontend_client" "$tmp_client" >/dev/null 2>&1 || true
}

generate_api_artifacts() {
  tmp_frontend_spec="$(mktemp "$state_dir/frontend-openapi.XXXXXX")"
  tmp_backend_spec="$(mktemp "$state_dir/backend-openapi.XXXXXX")"
  frontend_changed=0

  mkdir -p "$(dirname "$backend_spec")"

  GOCACHE="${GOCACHE:-/tmp/middleman-gocache}" "$GO_BIN" run ./cmd/middleman-openapi -out "$tmp_frontend_spec" -format yaml
  GOCACHE="${GOCACHE:-/tmp/middleman-gocache}" "$GO_BIN" run ./cmd/middleman-openapi -out "$tmp_backend_spec" -version 3.0

  if write_if_changed "$frontend_spec" "$tmp_frontend_spec"; then
    frontend_changed=1
  fi

  write_if_changed "$backend_spec" "$tmp_backend_spec" >/dev/null 2>&1 || true

  if [ "$frontend_changed" -eq 1 ]; then
    tmp_schema="$(mktemp "$state_dir/frontend-schema.XXXXXX")"
    (
      cd frontend
      "$BUN_BIN" x openapi-typescript openapi/openapi.yaml --enum-values -o "../$tmp_schema"
    )
    write_if_changed "$frontend_schema" "$tmp_schema" >/dev/null 2>&1 || true
    generate_frontend_client
  fi

  GOCACHE="${GOCACHE:-/tmp/middleman-gocache}" "$GO_BIN" generate ./internal/apiclient/generated
}

current_inputs_hash="$(compute_inputs_hash)"
previous_inputs_hash=""

if [ -f "$input_hash_file" ]; then
  previous_inputs_hash="$(cat "$input_hash_file")"
fi

if [ "$current_inputs_hash" != "$previous_inputs_hash" ]; then
  generate_api_artifacts
  printf '%s\n' "$current_inputs_hash" > "$input_hash_file"
fi

"$GO_BIN" build -o "./tmp/middleman$exe_suffix" ./cmd/middleman
