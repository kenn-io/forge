#!/usr/bin/env sh

set -eu

repo_dir=".repos/effect"
repo_url="https://github.com/Effect-TS/effect"
repo_ref="f4151e1937c26de14f1d64566f8126173f1b5014"

if [ -d "$repo_dir/.git" ]; then
  current_url=$(git -C "$repo_dir" remote get-url origin 2>/dev/null || true)
  case "$current_url" in
    "$repo_url"|"$repo_url.git") ;;
    *)
      printf 'Expected %s origin at %s, found %s\n' "$repo_url" "$repo_dir" "${current_url:-none}" >&2
      exit 1
      ;;
  esac

  if ! checkout_status=$(git -C "$repo_dir" status --porcelain); then
    printf 'Could not verify Effect checkout at %s\n' "$repo_dir" >&2
    exit 1
  fi
  if [ -n "$checkout_status" ]; then
    printf 'Refusing to replace modified Effect checkout at %s\n' "$repo_dir" >&2
    exit 1
  fi
  current_ref=$(git -C "$repo_dir" rev-parse HEAD 2>/dev/null || true)
  if [ "$current_ref" = "$repo_ref" ]; then
    exit 0
  fi
  if ! git -C "$repo_dir" cat-file -e "$repo_ref^{commit}" 2>/dev/null; then
    git -C "$repo_dir" fetch origin "$repo_ref"
  fi
  git -C "$repo_dir" checkout --detach "$repo_ref"
  exit 0
fi

if [ -e "$repo_dir" ]; then
  printf 'Expected %s to be absent or a Git checkout\n' "$repo_dir" >&2
  exit 1
fi

mkdir -p ".repos"
git clone --filter=blob:none --no-checkout "$repo_url" "$repo_dir"
git -C "$repo_dir" checkout --detach "$repo_ref"
