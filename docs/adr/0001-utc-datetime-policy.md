# ADR 0001: UTC Datetime Policy

## Status

Accepted

## Context

kenn-forge moves timestamps through several layers:

- GitHub API payloads
- Go normalization and persistence
- SQLite storage
- HTTP API responses
- Svelte UI rendering

The codebase already trends toward UTC handling, but the rule has been implicit rather than explicit. Some handlers already emit UTC RFC3339, some database writes use SQLite-generated UTC timestamps, and some read paths tolerate multiple serialized layouts.

Without a documented rule, timezone logic can leak into the wrong layer and create subtle bugs in sorting, filtering, comparisons, and user-visible rendering.

## Decision

kenn-forge will use UTC as the canonical representation for datetimes across storage and API boundaries.

- Store kenn-forge-owned datetimes in UTC.
- Serialize API datetimes as UTC RFC3339.
- Convert to local timezone only in the Svelte UI presentation layer.

Backward-compatible database reads may continue accepting multiple legacy layouts, but parsed values must be normalized to UTC immediately.

## Consequences

### Positive

- One clear invariant for contributors and reviewers.
- Simpler reasoning about ordering, filtering, and comparisons.
- Fewer timezone-related regressions at storage and API boundaries.
- Clear separation between canonical data handling and user-facing display.

### Negative

- Existing tolerant parsing code remains necessary for compatibility.
- Some write and serialization paths need cleanup to align with the policy.
- Frontend display code may still be duplicated until separately consolidated.

## Rules

### Allowed

- `time.Now().UTC()` before persistence or API serialization
- `t.UTC().Format(time.RFC3339)` for API responses
- explicit SQL-generated UTC RFC3339 values when SQL owns timestamp creation
- `new Date(apiTimestamp)` in the UI, followed by locale-specific rendering

### Not allowed

- backend code that converts timestamps into local time for display
- API handlers that emit timezone-dependent or ad hoc datetime strings
- storage code that treats persisted timestamps as local wall-clock values

## Notes

This ADR defines the architectural rule. Contributor reminders belong in `CLAUDE.md`, and enforcement belongs in code and tests.
