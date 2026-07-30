# Retries, Backoff, and Single-Flight Dedup

middleman has three distinct wait patterns. Keep them separate:

- **Transient retry** — short bounded retries for flaky upstream failures.
- **Rate-limit gate** — wait until provider quota resets.
- **Scheduling cadence** — steady periodic loops.

Issue #299 consolidates only transient retry under one shared package.
Rate-limit gates and cadence loops are intentionally separate.

## Transient retry: `internal/retry`

[`internal/retry/retry.go`](../internal/retry/retry.go). Canonical home for
`cenkalti/backoff/v5` orchestration. It wraps idempotent operations in a
bounded retry loop, retries only when the caller's classifier marks an error
transient, wraps all other errors with `backoff.Permanent`, and logs each retry
attempt at `DEBUG`.

Production schedule: 500ms → ~4s, jitter ±30%, 3 attempts.

**Transient retry sites:**

- [`internal/retry/retry.go`](../internal/retry/retry.go) — shared helper and
  production schedule.
- [`internal/gitclone/retry.go`](../internal/gitclone/retry.go) — git-specific
  transient matcher plus thin adapter into `internal/retry`.
- [`internal/gitclone/clone.go`](../internal/gitclone/clone.go) — retries
  `git clone --bare`, `git fetch`, and `git remote set-head`.

Use transient retry for idempotent work that hits flaky upstreams. Do not stack
it on top of paths that already retry (for example `internal/github` REST
client behavior). Test the shared helper against `DoWithBackOff` with a fast
injected backoff, and test package-specific callers for their own classifier and
budget choices.

Scope upstream timeout child contexts to the upstream call; subsequent local
database and filesystem work must retain the request context
(`internal/server/kataapi/workspace.go::getKataProjectMappings`).

To extend git transient matching, add a substring to the slice in
`internal/gitclone/retry.go` and a row to `internal/gitclone/retry_test.go`.
Keep the matcher conservative — false positives turn permanent failures into
multi-second hangs.

## Rate-limit gates

These paths are **not** transient retry. They represent provider quota state and
wait until the reset window.

- [`internal/ratelimit/rate.go`](../internal/ratelimit/rate.go) —
  `RateTracker.ShouldBackoff()` returns `(bool, time.Duration)` for exhausted
  quota windows.
- [`internal/github/graphql.go`](../internal/github/graphql.go) —
  `GraphQLFetcher.ShouldBackoff()` passthrough for GraphQL quota gating.
- [`internal/github/sync.go`](../internal/github/sync.go) — worker and GraphQL
  call sites that gate work on `ShouldBackoff()` before proceeding.

Do not wrap these paths in `backoff.Retry`, `RetryAfterError`, or any new retry
abstraction unless a separate design explicitly changes rate-limit policy.

## Scheduling cadence (out of scope)

Steady background cadence is scheduling policy, not transient retry. Do not
migrate ticker-driven sync or refresh loops into `backoff/v5`
(`internal/github/sync.go::Syncer.Start`).

Repository-disabled issue and merge-request scopes use a 24-hour in-memory
background probe gate. Expiry admits one reserved background probe across all
lanes; any pre-provider exit abandons only the reservation, while completed provider
work either renews or releases the observed generation. (`internal/github/feature_cooldown.go::repositoryFeatureProbe.abandon`)
Explicit sync bypasses only cooldown generations present when the run begins; any
non-disabled attempt clears that observed generation even when retry bits remain, while
newer disabled results gate follow-on lanes. (`internal/github/feature_cooldown.go::repositoryFeatureProbe.release`)
Cooldown keys use provider-canonical repository identity; GitHub host, owner/name case,
and derived-path aliases must converge. (`internal/github/feature_cooldown.go::repositoryFeatureKey`)
Completion clears only the observed generation, so a concurrent disabled renewal wins.
(`internal/github/feature_cooldown.go::repositoryFeatureProbe.clear`)
Follow-on lanes after index completion must acquire a fresh probe; clearing the index
generation does not authorize an unchanged-list comment refresh. (`internal/github/sync.go::refreshRepoPRComments`)
Classify repository-disabled reads before generic fallback or detail handling. GitHub
uses its definitive disabled 410; other providers confirm candidate 403/404/410 failures
against repository metadata and preserve unconfirmed errors. (`internal/platform/gitealike/feature_disabled.go::Provider.repositoryFeatureError`)
Custom GitHub GraphQL timeline transports must preserve structured non-2xx errors before
closing the body so disabled-feature classification can inspect 410 responses.
(`internal/github/client.go::ListPullRequestTimelineEvents`)
Recording a disabled result must preserve any earlier same-scope item failure so retry
and ETag invalidation state survives the cooldown. (`internal/github/sync.go::indexSyncRepo`)
Apply the gate before shared budget exhaustion so suppressed work cannot starve
unaffected scopes. (`internal/github/sync.go::drainDetailQueue`)
Archive inventory, maintenance, and hydration share this in-memory gate; pre-provider
deferral skips only that repo/type so unaffected streams and later same-host repositories
remain eligible. (`internal/archive/scheduler.go::runProviderHostWork`)
Feature deferral preserves scan cursors and pending lookup state and is never persisted as
provider-budget or item-retry state. (`internal/github/sync.go::Admit`)
Hydration excludes the cooled repository feature only while selecting work in the current
pass, so unaffected due items remain eligible and restart or manual recovery can resume
immediately. (`internal/archive/scheduler.go::runNextHydrationWork`)

## Single-flight: `Manager.EnsureClone`

[`clone.go`](../internal/gitclone/clone.go). `EnsureClone` opens a
`singleflight` slot keyed on `(host, owner, name)` so concurrent
callers (periodic syncer, per-PR detail syncs, workspace setup) share
one underlying clone/fetch instead of stampeding GitHub.

Invariants to preserve:

- **Pre-check `ctx.Err()`**. A caller whose ctx is already canceled
  must not enter the slot, or the runner does work for nobody.
- **Key shape** `host \x00 owner \x00 name`. The null separator
  prevents `foo/barbaz` colliding with `foobar/baz`.
- **Detached, bounded runner ctx**. The slot runs with
  `context.WithTimeout(context.WithoutCancel(ctx), ensureCloneTimeout)`.
  Detached so one canceled waiter cannot abort work for others;
  bounded so a stuck git subprocess cannot hold the slot forever.
- **`DoChan`, not `Do`**, so each caller still observes its own
  `ctx.Done()` via the outer `select`.

Reach for a singleflight slot whenever multiple in-process call sites
hit the same upstream resource. Prefer dedup over retry — it removes
the cause, retry just absorbs the effect.

## Tests

Test the policy decisions, not the library. For retry that means the
matcher, the `backoff.Permanent` wrap, and the budget constant. For
dedup that means the key shape and the integration paths that
exercise the real cloneBare/fetch. Skip tests that assert "the library
loops" or "DoChan delivers to every waiter" — those are upstream's
contract.
