#!/usr/bin/env bash
# Materialize the complete workflow screenshot set from the reviewed commit in
# scripts/docs-assets.ref. Every source is staged and validated as one
# generation before it replaces the ignored local cache.
set -euo pipefail

script_repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="${DOCS_ASSETS_REPO_ROOT:-$script_repo_root}"
manifest="${DOCS_ASSETS_MANIFEST:-$script_repo_root/scripts/docs-assets.txt}"
destination="${DOCS_ASSETS_DESTINATION:-$repo_root/docs/assets/generated}"
remote="${DOCS_ASSETS_REMOTE:-origin}"
ref_file="${DOCS_ASSETS_REF_FILE:-$script_repo_root/scripts/docs-assets.ref}"
pinned_ref="${DOCS_ASSETS_REF:-$(tr -d '[:space:]' < "$ref_file")}"
raw_root="${DOCS_ASSETS_RAW_ROOT:-https://raw.githubusercontent.com/kenn-io/forge/$pinned_ref}"
generation_manifest="$destination/.docs-assets.synced"
generation_ref="$destination/.docs-assets.ref"
placeholder_manifest="$destination/.docs-assets.placeholder"

if [[ ! "$pinned_ref" =~ ^[0-9a-f]{40}$ ]]; then
  echo "error: docs asset ref must be a full lowercase Git commit SHA" >&2
  exit 1
fi

assets=()
while IFS= read -r asset; do
  [[ -n "$asset" ]] && assets+=("$asset")
done < "$manifest"
if [[ ${#assets[@]} -eq 0 ]]; then
  echo "error: docs asset manifest is empty" >&2
  exit 1
fi

mkdir -p "$(dirname "$destination")"
stage_root="$(mktemp -d "$(dirname "$destination")/.docs-assets-sync.XXXXXX")"
trap 'rm -rf "$stage_root"' EXIT

hash_file() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

git_ref_has_complete_set() {
  local ref="$1" asset
  for asset in "${assets[@]}"; do
    if ! git -C "$repo_root" cat-file -e "$ref:$asset" 2>/dev/null; then
      missing_asset="$asset"
      return 1
    fi
  done
}

stage_git_ref() {
  local ref="$1" target="$2" asset
  mkdir "$target"
  for asset in "${assets[@]}"; do
    git -C "$repo_root" show "$ref:$asset" > "$target/$asset" 2>/dev/null || return 1
  done
}

stage_raw_assets() {
  local target="$1" asset
  mkdir "$target"
  for asset in "${assets[@]}"; do
    curl -fsSL --max-time 30 -o "$target/$asset" "$raw_root/$asset" 2>/dev/null || return 1
  done
}

validate_staged_assets() {
  local target="$1" asset file
  for asset in "${assets[@]}"; do
    file="$target/$asset"
    if ! grep -Eiq '<svg([[:space:]>])' "$file" \
      || grep -Eiq '<([[:alnum:]_.-]+:)?(script|foreignObject)([[:space:]>])|<\?xml-stylesheet|<!DOCTYPE|[[:space:]]on[[:alpha:]]+[[:space:]]*=|(href|xlink:href)[[:space:]]*=[[:space:]]*["'\'']((https?:)?//|javascript:)' "$file" \
      || { grep -Eio "(href|xlink:href)[[:space:]]*=[[:space:]]*[\"']data:[^\"']*" "$file" \
        | grep -Eiv "^(href|xlink:href)[[:space:]]*=[[:space:]]*[\"']data:image/png;base64,[A-Za-z0-9+/=]+$" >/dev/null; }; then
      echo "error: unsafe SVG in pinned docs asset $asset" >&2
      return 1
    fi
  done
}

cached_generation_is_synced() {
  local expected asset actual
  [[ -f "$generation_manifest" ]] || return 1
  [[ -f "$generation_ref" ]] || return 1
  [[ "$(tr -d '[:space:]' < "$generation_ref")" == "$pinned_ref" ]] || return 1
  [[ ! -f "$placeholder_manifest" ]] || return 1
  while read -r expected asset; do
    [[ -n "$expected" && -n "$asset" && -f "$destination/$asset" ]] || return 1
    actual="$(hash_file "$destination/$asset")"
    [[ "$actual" == "$expected" ]] || return 1
  done < "$generation_manifest"
  for asset in "${assets[@]}"; do
    grep -q "  $asset$" "$generation_manifest" || return 1
  done
}

stage_cached_assets() {
  local target="$1" asset
  cached_generation_is_synced || return 1
  mkdir "$target"
  for asset in "${assets[@]}"; do
    cp "$destination/$asset" "$target/$asset"
  done
}

stage_placeholders() {
  local target="$1" asset
  mkdir "$target"
  for asset in "${assets[@]}"; do
    printf '%s\n' \
      '<svg xmlns="http://www.w3.org/2000/svg" width="1600" height="966" viewBox="0 0 1600 966">' \
      '  <rect width="1600" height="966" fill="#0d1420"/>' \
      "  <text x=\"800\" y=\"483\" fill=\"#5f6870\" font-size=\"28\" font-family=\"monospace\" text-anchor=\"middle\">$asset placeholder</text>" \
      '</svg>' > "$target/$asset"
  done
}

publish_assets() {
  local source="$1" label="$2" placeholders="${3:-}" asset digest
  local old_destination="$stage_root/previous"
  if [[ -z "$placeholders" ]]; then
    for asset in "${assets[@]}"; do
      digest="$(hash_file "$source/$asset")"
      printf '%s  %s\n' "$digest" "$asset" >> "$source/.docs-assets.synced"
    done
  else
    touch "$source/.docs-assets.placeholder"
  fi
  printf '%s\n' "$pinned_ref" > "$source/.docs-assets.ref"

  if [[ -e "$destination" ]]; then
    mv "$destination" "$old_destination"
  fi
  if ! mv "$source" "$destination"; then
    [[ -e "$old_destination" ]] && mv "$old_destination" "$destination"
    echo "error: could not publish docs asset generation" >&2
    exit 1
  fi
  rm -rf "$old_destination"
  echo "synced ${#assets[@]} docs assets from $label"
}

fetched_ref=""
if git -C "$repo_root" fetch --depth=1 "$remote" "$pinned_ref" >/dev/null 2>&1; then
  fetched_ref="FETCH_HEAD"
fi

if [[ -n "$fetched_ref" ]]; then
  missing_asset=""
  if ! git_ref_has_complete_set "$fetched_ref"; then
    echo "error: fetched docs asset commit is incomplete; missing $missing_asset" >&2
    exit 1
  fi
  fetched_stage="$stage_root/fetched"
  if ! stage_git_ref "$fetched_ref" "$fetched_stage"; then
    echo "error: could not stage the complete fetched docs asset commit" >&2
    exit 1
  fi
  validate_staged_assets "$fetched_stage" || exit 1
  publish_assets "$fetched_stage" "$remote@$pinned_ref"
  exit 0
fi

missing_asset=""
local_stage="$stage_root/local"
if git -C "$repo_root" cat-file -e "$pinned_ref^{commit}" 2>/dev/null; then
  if ! git_ref_has_complete_set "$pinned_ref"; then
    echo "error: pinned docs asset commit is incomplete; missing $missing_asset" >&2
    exit 1
  fi
  if ! stage_git_ref "$pinned_ref" "$local_stage"; then
    echo "error: could not stage the complete pinned docs asset commit" >&2
    exit 1
  fi
  validate_staged_assets "$local_stage" || exit 1
  publish_assets "$local_stage" "local commit $pinned_ref"
  exit 0
fi

raw_stage="$stage_root/raw"
if stage_raw_assets "$raw_stage"; then
  validate_staged_assets "$raw_stage" || exit 1
  publish_assets "$raw_stage" "raw.githubusercontent.com@$pinned_ref"
  exit 0
fi

cached_stage="$stage_root/cached"
if stage_cached_assets "$cached_stage"; then
  validate_staged_assets "$cached_stage" || exit 1
  echo "warning: could not reach pinned docs asset commit; keeping the verified asset set" >&2
  publish_assets "$cached_stage" "verified local cache"
  exit 0
fi

if [[ -n "${DOCS_ASSETS_ALLOW_PLACEHOLDER:-}" ]]; then
  placeholder_stage="$stage_root/placeholders"
  stage_placeholders "$placeholder_stage"
  echo "warning: pinned docs asset commit is unavailable; generating placeholder assets" >&2
  publish_assets "$placeholder_stage" "generated placeholders" placeholder
  exit 0
fi

echo "error: could not sync the complete pinned docs asset set at $pinned_ref" >&2
echo "       and the local cache is missing or stale." >&2
echo "       Publish the complete asset set before building production docs." >&2
exit 1
