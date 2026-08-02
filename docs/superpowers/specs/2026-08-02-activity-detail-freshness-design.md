# Activity Detail Freshness Design

**Date:** 2026-08-02
**Status:** Approved for implementation

## Goal

Make the existing active-PR freshness policy agree with the Activity view: an open pull request whose visible Activity signal is less than 30 minutes old should have its persisted detail refreshed on the configured fast cadence, two minutes by default. Selecting a PR detail should also preserve a newer refresh intent when an older refresh for that PR is already running.

## Context

Kenn Forge already has a budgeted watched-PR lane. Open PRs whose `last_activity_at` is within the 30-minute hot window are eligible at `active_pr_refresh_interval`; the default is two minutes. Older open PRs within `active_pr_window`, four hours by default, use the five-minute warm cadence. GitHub watched refreshes use the persisted PR ETag, provider rate-limit checks, and the configured local sync budget.

The Activity feed has another freshness signal. A linked notification row is ordered and displayed using `forge_notification_items.source_updated_at`, but the watched-PR selector considers only `forge_merge_requests.last_activity_at`. A notification can therefore make Activity show recent PR activity without admitting that PR to the hot detail-refresh lane. This is a signal-joining bug, not a reason to add an independent poller.

Activity detail selection already requests an asynchronous item sync after loading the SQLite detail. The server deduplicates a request that collides with an in-flight sync for the same item. Because ordinary selection uses the non-rerunning form of that deduplication, a newer selection intent can be lost when the running sync observed older provider state. The next periodic sync then becomes the only recovery path.

## Design

### Unified hot-PR signal

Select recently active open PRs using an effective activity timestamp derived from:

- the PR's authoritative `last_activity_at`; and
- the newest linked PR notification's `source_updated_at`, matched through full repository identity and PR number.

Use the later timestamp only for scheduling eligibility and cadence. Do not write notification timestamps into `forge_merge_requests.last_activity_at`: that field remains derived from authoritative PR and timeline data, while a notification timestamp is only a signal that detail may be stale.

The existing hot and warm rules remain unchanged:

- effective activity no older than 30 minutes uses `active_pr_refresh_interval`;
- older effective activity still inside `active_pr_window` uses the five-minute warm interval; and
- missing `detail_fetched_at` remains due immediately.

Closed and merged PRs remain outside the active lane. Notification rows without a resolved tracked repository, PR number, or matching open PR do not create candidates. Provider, platform host, repository identity, and PR number all participate in the match.

### Selection refresh intent

Treat opening or reselecting a PR detail as a request that must run after an older in-flight detail sync when necessary. The asynchronous PR-detail endpoint should use the existing single-rerun mechanism rather than dropping a colliding request.

The request remains asynchronous:

1. The client loads the currently persisted SQLite detail immediately.
2. The server starts the item detail sync if none is running.
3. If the same item is already syncing, the server retains one pending rerun with the newest requested work.
4. Completion broadcasts the existing `data_changed` event, and the selected Activity detail reloads the newly persisted timeline.

Coalesce repeated clicks and tabs to at most one in-flight job plus one pending rerun per provider-aware PR identity. Do not create an unbounded queue.

### Cost and admission

Reuse the watched-PR lane and its existing controls:

- configured two-minute hot cadence and five-minute warm cadence;
- per-provider and per-host repository admission;
- credential-scoped rate-limit backoff;
- local hourly sync budget; and
- GitHub conditional PR requests using persisted ETags.

Do not add a second timer, a last-viewed database table, or a last-10 viewed polling set in this change. Repairing the existing freshness signal comes first; a viewed-history policy can be considered later with measured request data if rotation still feels stale.

An explicit detail selection remains higher-intent than periodic background work, but it does not bypass provider errors or create parallel same-item syncs. The single pending rerun is the bound.

## Error and concurrency behavior

- Failure to query linked notification activity logs the existing fast-sync selection warning and leaves the prior watched set behavior available; it must not stop the sync loop.
- Rate-limited, disabled-feature, untracked, or authentication-blocked repositories continue through their existing skip and backoff paths.
- A failed in-flight detail sync still yields to one pending selection rerun. Each job reports its own failure through the existing background logging path.
- A newer notification cannot regress an authoritative PR activity timestamp because notification activity is never persisted into the PR row.
- Duplicate notification rows for one PR collapse to their maximum `source_updated_at` for scheduling.
- Full provider identity prevents notifications on one host or repository from heating a same-number PR elsewhere.

## Testing

Add tests at the boundaries that establish the contract:

- Database/query tests prove effective PR activity uses the later of authoritative PR activity and linked notification activity, ignores unrelated identities and non-PR notifications, and returns each PR once.
- Watched-PR scheduler tests prove a notification-hot open PR receives the two-minute cadence even when its authoritative `last_activity_at` is older, while closed PRs and expired signals remain excluded.
- Server detail-sync tests prove a selection request colliding with an in-flight PR sync schedules exactly one rerun and that repeated collisions remain bounded.
- Server/API tests prove `POST .../pulls/.../sync/async` uses the rerun behavior without changing its `202 Accepted` contract.
- A focused full-stack Activity test creates a newer linked notification while PR detail is stale, verifies that the fast lane persists the missing detail, and verifies that the open or subsequently selected detail refreshes from the resulting `data_changed` event.

Run targeted shuffled Go tests for the database, GitHub syncer, and server packages. Run the existing Activity frontend and full-stack tests because the observable contract crosses the notification feed, persisted detail, SSE invalidation, and selected drawer.

## Non-goals

- Polling the last 10 viewed PRs independently of the watched-PR lane.
- Persisting user view history.
- Treating notification timestamps as authoritative PR timeline timestamps.
- Expanding the active lane to closed or merged PRs.
- Changing the configured hot/warm intervals or the hourly sync budget defaults.
- Adding a new progress indicator or stale-data banner.
- Extending the PR-only watched lane to issues in this change.
