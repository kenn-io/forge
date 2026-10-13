#!/bin/sh
# Stands in for Claude Code in the reboot simulation: reports hook events
# through the real `kenn-forge agent-hook run` relay, records whether it was
# started fresh or resumed, and sends SessionEnd with reason "other" when
# signaled, as Claude does. Payloads carry the fields the hook decoder
# requires: SessionStart a source, SessionEnd a reason.
set -eu

session_id=""
previous=""
for arg in "$@"; do
  if [ "$previous" = "--resume" ]; then
    session_id="$arg"
  fi
  previous="$arg"
done
mode="resume"
source="resume"
if [ -z "$session_id" ]; then
  source="startup"
  mode="start"
  session_id="sim-$(cat /proc/sys/kernel/random/uuid)"
fi
printf '%s %s %s\n' "$mode" "$session_id" "${KENN_FORGE_RUNTIME_SESSION_KEY:-}" >>.rebootsim-agent.log

# hook EVENT [EXTRA_JSON_FIELDS]
hook() {
  printf '{"session_id":"%s","cwd":"%s","hook_event_name":"%s"%s}' "$session_id" "$PWD" "$1" "${2:-}" |
    kenn-forge agent-hook run --agent claude --config /data/config.toml \
      --source kenn-forge-agent-activity >/dev/null 2>&1 || true
}

hook SessionStart ",\"source\":\"$source\""
hook Stop
trap 'hook SessionEnd ",\"reason\":\"other\""; exit 0' HUP TERM INT
while :; do
  sleep 1
done
