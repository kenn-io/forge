# Provider-Capped Archive Pacing Design

## Goal

Prevent historical archive catch-up from exhausting a GitHub credential's
provider quota when `sync_budget_per_hour` is configured as a high process-local
emergency ceiling.

## Problem

GitHub archive admission checks the credential's REST and GraphQL pools before
each item and preserves a 200-request reserve. Its longer-term pacing envelope,
however, is calculated from `SyncBudget.limit`. That value is a process-local
guard, not provider capacity.

When an operator raises the local guard above GitHub's 5,000-request hourly
quota, the quadratic archive schedule scales to the larger local number.
Archive hydration can then run continuously until the provider reserve is
reached. Concurrent live and periodic work can consume the reserve, leaving the
credential rate limited even though the local guard still reports substantial
capacity.

## Decisions

### Provider-aware pacing window

For GitHub, archive pacing uses the configured local ceiling and the live quota
registry together:

- every required provider resource must have a current known pool;
- the provider pacing limit is the smallest limit among the required REST and
  GraphQL pools;
- the provider pacing reset is the latest reset among those pools, so the
  aggregate schedule cannot run ahead of a slower resource window;
- the effective archive envelope is the smaller of the local ceiling and the
  provider pacing limit;
- the 200-request provider reserve is excluded from the provider archive
  envelope.

The existing per-item reserve check remains authoritative for current
remaining capacity. The new envelope limits cumulative archive spend over the
window; it does not replace live quota observations.

### Local emergency guard

`sync_budget_per_hour` remains a process-wide hard ceiling for all background
wire attempts. It continues to protect providers without usable quota windows
and protects against runaway local traffic. A high value no longer increases
GitHub archive throughput beyond the credential's reported capacity.

No new archive-specific configuration option is introduced. Operators may
still choose a lower local ceiling when they want an absolute cap across
archive, periodic, notification, and active-detail background work.

### Unknown and mixed quota state

GitHub archive work remains deferred whenever one of its required quota pools
is unknown, expired, or at its reserve. Non-GitHub providers retain the existing
local-only pacing behavior when no provider quota window is available.

### Conditional requests

The existing conditional-GET behavior is unchanged. Authorized 304 responses
continue to be refunded from the local budget because they do not consume
GitHub's primary quota. Archive maintenance treatment of 304 responses is
separate from this incident.

## Components

`QuotaRegistry` exposes a conservative pacing window for a credential and a set
of resources. `Syncer.Admit` supplies that provider limit and reset to
`SyncBudget`. `SyncBudget` calculates the quadratic archive schedule against
the effective provider/local envelope while retaining its independent local
hard-stop accounting.

No API, database, or frontend contract changes are required.

## Failure Handling

- Missing or expired GitHub quota state defers archive work until headers or
  bounded reconciliation establish a current window.
- A provider pool at its reserve continues to defer work until that pool
  resets.
- Local ceiling exhaustion continues to reject background requests before
  upstream I/O.
- Foreground work remains outside the local background ceiling and may consume
  provider capacity intentionally.

## Testing

Unit coverage proves that:

- a 100,000-request local ceiling paired with a 5,000-request provider pool
  paces archive work from the provider limit, not the local guard;
- a local ceiling below the provider limit remains the effective hard cap;
- mixed REST and GraphQL limits use the smaller limit and later reset;
- unknown or expired resource pools do not produce a pacing window;
- non-GitHub local-only pacing retains its current behavior.

Sync admission coverage proves that a GitHub archive request receives the
provider-capped allowance while preserving the existing reserve and local
ceiling checks.

## Operational Rollout

Before deploying the code fix, affected instances should use a conservative
explicit `sync_budget_per_hour` value and restart so the new ceiling takes
effect. After deployment, the same explicit value remains a useful
belt-and-suspenders cap while provider-aware pacing prevents future high local
settings from exhausting GitHub archive quota.

Rollback is the previous Middleman binary plus the conservative explicit local
ceiling. Verification compares the status endpoint's local spend and GitHub
remaining quota across a fresh provider window and confirms archive progress
does not drive the provider pool to zero.
