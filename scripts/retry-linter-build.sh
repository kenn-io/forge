#!/bin/sh
set -eu

# GOPROXY's source fallback cannot recover checksum-service stream failures.
log=$(mktemp)
trap 'rm -f "$log"' 0

for attempt in 1 2 3; do
    if "$@" >"$log" 2>&1; then
        cat "$log"
        exit 0
    else
        status=$?
    fi
    cat "$log" >&2
    if [ "$attempt" -eq 3 ] || ! grep -Eq 'reading https://sum\.golang\.org/.*: stream error: .*INTERNAL_ERROR' "$log"; then
        exit "$status"
    fi
    echo "Checksum service stream failed; retrying linter build in $((attempt * 5)) seconds." >&2
    sleep "$((attempt * 5))"
done
