#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: context-sync-stop.sh stop|mark <context-decision>|status

stop    Enforce that the context-sync Stop-hook gate has checked the current worktree state.
mark    Record the current worktree state after a preflight and explicit context decision.
status  Print whether the current worktree state is marked, with the recorded decision.
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
  local decision="$2"
  local file
  local fp

  file="$(state_file "$root")" || return 1
  fp="$(fingerprint "$root")" || return 1
  mkdir -p "$(dirname "$file")"
  printf '%s\n%s\n' "$fp" "$decision" >"$file"
}

current_is_marked() {
  local root="$1"
  local file
  local fp

  file="$(state_file "$root")" || return 1
  [ -f "$file" ] || return 1
  fp="$(fingerprint "$root")" || return 1
  [ "$(sed -n '1p' "$file")" = "$fp" ]
}

require_marked() {
  local root="$1"

  if current_is_marked "$root"; then
    exit 0
  fi

  cat >&2 <<'MESSAGE'
middleman context-sync Stop-hook gate is required before this turn can complete.

Run:
  scripts/context-sync --check

Then inspect the turn's diff and conversation for durable context changes:
  - update or propose context docs for new behavior, design decisions, invariants, or gotchas
  - if no update belongs in context, be ready to state the concrete reason

Finally mark with that decision, for example:
  scripts/hooks/context-sync-stop.sh mark "updated context/testing.md for the new API-test rule"
  scripts/hooks/context-sync-stop.sh mark "no context update: changed only local wording in a hook prompt"

If context-sync reports drift, address it or report the findings before marking.
Do not mark solely because the preflight passed.
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
    mark)
      shift
      if [ "$#" -eq 0 ] || [ -z "${*//[[:space:]]/}" ]; then
        printf 'context-sync-stop: mark requires a context decision reason\n' >&2
        usage
        exit 64
      fi
      mark_current "$root" "$*" ;;
    status)
      if current_is_marked "$root"; then
        echo "context-sync-stop: current worktree state is marked"
        state="$(state_file "$root")" || exit 1
        if [ "$(wc -l <"$state")" -gt 1 ]; then
          printf 'context-sync-stop: context decision: %s\n' "$(sed -n '2,$p' "$state")"
        fi
      else
        echo "context-sync-stop: current worktree state is not marked"
        exit 1
      fi
      ;;
  esac
}

main "$@"
