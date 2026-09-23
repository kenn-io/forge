#!/bin/sh
# Run a code generator only when its inputs or outputs changed.
#
# Usage: cached-generate.sh NAME PATH... -- COMMAND [ARG...]
#
# PATHs are files or directories covering both the generator's inputs and
# its generated output. The command is skipped when their combined content
# hash matches the hash recorded after the last successful run, so an edited
# spec, generator, lockfile, or hand-edited/deleted output always regenerates.
# The record lives in this worktree's git directory and is never committed.
set -eu

if [ "$#" -lt 3 ]; then
  echo "usage: $0 NAME PATH... -- COMMAND [ARG...]" >&2
  exit 2
fi

name=$1
shift
paths=""
while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do
  paths="$paths
$1"
  shift
done
if [ "$#" -eq 0 ]; then
  echo "$0: missing -- before the generator command" >&2
  exit 2
fi
shift
if [ "$#" -eq 0 ]; then
  echo "$0: missing generator command" >&2
  exit 2
fi

fingerprint() {
  printf '%s\n' "$paths" | while IFS= read -r path; do
    [ -n "$path" ] || continue
    if [ -e "$path" ]; then
      find "$path" -type f -print
    else
      printf 'missing:%s\n' "$path"
    fi
  done | LC_ALL=C sort | while IFS= read -r file; do
    case "$file" in
      missing:*) printf '%s\n' "$file" ;;
      *) printf '%s %s\n' "$(git hash-object --no-filters -- "$file")" "$file" ;;
    esac
  done | git hash-object --stdin
}

stamp_dir=$(git rev-parse --git-path kenn-forge-generate)
stamp="$stamp_dir/$name"

current=$(fingerprint)
if [ -f "$stamp" ] && [ "$(cat "$stamp")" = "$current" ]; then
  echo "$name: inputs and outputs unchanged; skipping generation"
  exit 0
fi

"$@"

mkdir -p "$stamp_dir"
fingerprint >"$stamp"
