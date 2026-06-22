#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: context-sync-stop.sh stop|mark|status

stop    Enforce that context-sync has checked the current worktree state.
mark    Record the current worktree state after scripts/context-sync --check or sync.
status  Print whether the current worktree state is marked.
USAGE
}

find_root() {
  git rev-parse --show-toplevel 2>/dev/null || return 1
}

state_file() {
  local root="$1"
  local git_dir

  git_dir="$(git -C "$root" rev-parse --git-dir 2>/dev/null)" || return 1
  case "$git_dir" in
    /*) printf '%s\n' "$git_dir/middleman-context-sync-stop" ;;
    *) printf '%s\n' "$root/$git_dir/middleman-context-sync-stop" ;;
  esac
}

fingerprint() {
  local root="$1"

  {
    git -C "$root" rev-parse HEAD
    git -C "$root" status --porcelain=v1 -z
    git -C "$root" diff --binary --no-ext-diff HEAD --
    git -C "$root" ls-files --others --exclude-standard -z |
      while IFS= read -r -d '' path; do
        printf 'untracked %s\0' "$path"
        if [ -f "$root/$path" ]; then
          shasum -a 256 "$root/$path"
        fi
      done
  } | shasum -a 256 | awk '{print $1}'
}

mark_current() {
  local root="$1"
  local file
  local fp

  file="$(state_file "$root")" || return 1
  fp="$(fingerprint "$root")" || return 1
  mkdir -p "$(dirname "$file")"
  printf '%s\n' "$fp" >"$file"
}

current_is_marked() {
  local root="$1"
  local file
  local fp

  file="$(state_file "$root")" || return 1
  [ -f "$file" ] || return 1
  fp="$(fingerprint "$root")" || return 1
  [ "$(cat "$file")" = "$fp" ]
}

require_marked() {
  local root="$1"

  if current_is_marked "$root"; then
    exit 0
  fi

  cat >&2 <<'MESSAGE'
middleman context-sync is required before this turn can complete.

Run:
  scripts/context-sync --check
  scripts/hooks/context-sync-stop.sh mark

If context-sync reports drift, address it or report the findings before marking.
MESSAGE
  exit 2
}

main() {
  local command="${1:-}"
  local root

  case "$command" in
    stop|mark|status) ;;
    *) usage; exit 64 ;;
  esac

  root="$(find_root)" || exit 0
  [ -f "$root/skills/context-sync/SKILL.md" ] || exit 0

  case "$command" in
    stop) require_marked "$root" ;;
    mark) mark_current "$root" ;;
    status)
      if current_is_marked "$root"; then
        echo "context-sync-stop: current worktree state is marked"
      else
        echo "context-sync-stop: current worktree state is not marked"
        exit 1
      fi
      ;;
  esac
}

main "$@"
