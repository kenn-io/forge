# ADR 0004: Quota meter semantics

Date: 2026-08-09

## Status

Accepted

## Context

The quota popover presents two different quantities. Provider REST and GitHub
GraphQL numerators are capacity left, sourced from provider rate-limit
responses. The local process-guard numerator is requests spent against the
configured hourly ceiling. Giving those opposite quantities the same visual
direction makes the numbers ambiguous.

The local sync budget reserves `floor(limit / 10)` requests for essential
repository discovery. Optional background work therefore stops at 2,700 spent
requests for a 3,000-request ceiling, while essential discovery may use the
final 300. The existing API does not expose that boundary, so the frontend
cannot explain why background work stops before the hard ceiling.

The provider resource values and host-level `sync_paused` flag can also come
from different authorities: credential quota-registry snapshots override the
REST and GraphQL values, while `sync_paused` remains tracker-derived. Using the
flag for popover copy can contradict the provider values shown beside it.

The status bar also imposes `white-space: nowrap`; without resetting that at
the popover boundary, explanatory copy forces horizontal scrolling.

## Decision

Keep the existing compact popover, visual tokens, and provider-only status-bar
trigger while giving the quota systems distinct geometry:

- Provider capacity fills are anchored to the right and contract toward that
  edge as capacity is consumed.
- Local spend fills are anchored to the left and grow toward the hard ceiling.
- The local meter marks the backend-provided `background_limit`; the segment
  beyond it is visibly reserved for essential discovery.
- Reaching that boundary is described as `background refresh paused`, not
  `sync paused`.
- Provider pressure is derived per REST or GraphQL resource from the displayed
  values and `reserve_buffer`, not the host-level `sync_paused` flag.
- `sync_throttle_factor` remains separate cadence information.

Expose `background_limit` on `LocalSyncCeilingStatus` rather than duplicating
the reserve calculation in Svelte. Keep the existing ceiling and provider
fields intact.

The compact visual UI does not add `remaining`, `used`, or `rem`. Popover
tracks use meter semantics with explicit accessible names, raw values, limits,
units, and policy descriptions for screen-reader users. Outlined tracks,
opposing fill direction, alignment, and the threshold marker communicate
meaning without relying on color alone.

Reset inherited no-wrap behavior at the popover boundary. Include padding in
its fixed width, let grid content shrink, and wrap identities and explanatory
copy inside the content box. Horizontal clipping is only a final safeguard,
not a substitute for correct layout.

## Consequences

Provider capacity and local spend remain separate across the backend, wire,
and UI. A future reserve-policy change flows from the backend without a matching
frontend constant. Tests cover the enforced API boundary, accessible meter
values, opposing fill anchors, scoped pause copy, and real-browser overflow.
