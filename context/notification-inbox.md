# Notification Inbox

Use this document for changes touching GitHub notifications, the Inbox route, notification API handlers, notification sync, or notification persistence.

## Purpose

The notification inbox is a draft, feature-flagged maintainer work queue. It syncs the signed-in user's GitHub notification threads into SQLite, filters them to currently monitored repositories, and gives middleman local triage state that is separate from GitHub's read/unread flag.

This surface is intentionally not the Activity feed:

- Activity is immutable history across repos.
- Notifications are mutable, per-user inbox state with unread/read, done/undone, queued GitHub read propagation, retry, and dead-letter metadata.
- Both surfaces may point at the same subject identity: `(platform_host, owner, repo, item_type, number)`.

## Feature Flag

Notifications are disabled by default.

```toml
[notifications]
enabled = false
sync_interval = "2m"
propagation_interval = "1m"
batch_size = 25
```

Rules:

- `Config.NotificationsEnabled()` returns true only when `enabled = true` is explicit.
- The Settings UI does not expose this flag yet.
- When disabled, the app must not load notification data, render the Inbox UI, run notification sync loops, or expose notification list/mutation APIs.
- Direct `/inbox` navigation should show disabled copy rather than the draft Inbox.
- Notification list, sync, and bulk mutation handlers should return `403` when disabled.
- E2E servers may opt in so Inbox tests keep covering the feature.

## Repository Scope And Identity

Notifications are user-scoped at provider level but repo-scoped in middleman.

Rules:

- Persist notification item identity as `(platform, platform_host, platform_notification_id)`.
- Treat repository identity as `(platform, platform_host, repo_owner, repo_name)` everywhere notifications are filtered, joined, or summarized.
- `platform` is required. Blank provider/platform values are errors, not implicit GitHub defaults.
- Show notifications only for current monitored repo set from config/syncer repo refs.
- Historical notifications for removed repos may stay in SQLite but must not appear in `unread`, `active`, `read`, `done`, or `all` unless future explicit `include_unmonitored` contract exists.
- `repo_id` is enrichment/optimization, not visibility authority.
- Tracked repo keys and sync watermarks must include provider identity, not host alone.
- Repo facets and filters must be host-qualified when host ambiguity is possible, e.g. `github.com/acme/widget`.

## Persistence Shape

Notification persistence is provider-neutral even though only GitHub sync exists today.

Current tables:

- `middleman_notification_items`
- `middleman_notification_sync_watermarks`

Current provider-owned fields:

- `platform`
- `platform_host`
- `platform_notification_id`
- `source_updated_at`
- `source_last_acknowledged_at`
- `source_ack_*`
- `sync_cursor`
- `tracked_repos_key`

Rules:

- `done_at` and `done_reason` remain middleman-local triage state.
- `source_*` fields track provider-side activity and acknowledgement propagation state.
- `sync_cursor` is opaque provider-owned watermark state. GitHub currently leaves it empty.
- Current notification schema ships as single DB upgrade in `000019_notifications.*`; do not split future assumptions across deleted branch-only migrations.

## Inbox State Model

Middleman stores local workflow state separately from GitHub state.

- `unread`: `done_at IS NULL AND unread = 1`. This is the default landing state.
- `active`: `done_at IS NULL`, regardless of unread.
- `read`: `done_at IS NULL AND unread = 0`.
- `done`: `done_at IS NOT NULL`.
- `all`: all monitored-repo notifications matching non-state filters.

Rules:

- `done_at` is local Octobox-style completion state.
- Marking a row done hides it from default Inbox immediately.
- Marking a row read clears local unread immediately without setting `done_at`.
- Marking done with `mark_read=true` queues GitHub read propagation; it does not block on GitHub.
- `undone` clears only local `done_at` unless linked PR/issue closure rules immediately re-close it.
- If a linked monitored PR is closed/merged or linked issue is closed, active notifications are marked done with `done_reason = 'closed'`.
- A locally done row re-enters active/unread only when GitHub reports newer unread activity than the local done/read generation.
- Read-only GitHub updates must not reopen locally done rows.

## GitHub Read Propagation

Bulk actions are local-first. GitHub read-state propagation is asynchronous.

Provider-neutral storage fields:

- `source_ack_queued_at`: local read/done queued for provider propagation.
- `source_ack_synced_at`: provider mark-read succeeded or provider later reported acknowledged/read.
- `source_ack_generation_at`: source activity timestamp covered by successful propagation.
- `source_last_acknowledged_at`: only set after successful provider propagation or source sync reporting acknowledged/read, never when merely queued.
- `source_ack_error`, `source_ack_attempts`, `source_ack_last_attempt_at`, `source_ack_next_attempt_at`: retry/dead-letter state.

Rules:

- GitHub remains only implemented notification provider today.
- Propagation workers must revalidate queued generation before calling provider.
- Stale queued work must not mark newer provider activity read.
- After successful propagation, stale GitHub sync payloads with `unread=true` and `source_updated_at <= source_ack_generation_at` must preserve local read state.
- Newer unread GitHub activity clears queued/synced/error propagation fields and reactivates row.
- Failure updates must be guarded by queued generation just like success updates.
- Rate-limit/secondary-limit errors should pause retry without burning normal per-row attempts across batch.
- Retry cap failures should stop automatic retries, clear `source_ack_next_attempt_at`, and preserve local done/read state.

## Sync Behavior

Notification sync has its own status and cadence.

Rules:

- Notification sync should process each configured provider host independently; one provider-host failure must not block others.
- Notification sync failures should update notification sync status so UI can surface them.
- Top-level manual sync may trigger notification sync only when notifications are enabled.
- `/notifications/sync` triggers only notification sync and returns `202` once accepted.
- Sync watermark identity is `(platform, platform_host)`.
- First host sync may need GitHub `All: true`; later syncs should use persisted watermark/overlap to avoid full backlog scans.
- `tracked_repos_key` must include provider-qualified tracked repo identity so watermark reuse does not cross providers sharing same host.
- Notification sync and read propagation should stop with server lifecycle before shared services are torn down.
- Closed/merged linked notification completion must run after repo/detail/list paths that persist closed PR or issue state, not only after notification sync.

## Subject Links

Notification subjects may be PRs, issues, releases, commits, discussions, or other GitHub objects.

Rules:

- PR/issue notifications should route to existing middleman detail surfaces when `(platform_host, owner, repo, number)` is available.
- PR subjects may arrive with issue-style API URLs; parse both `/pulls/{number}` and `/issues/{number}` when GitHub subject type is `PullRequest`.
- Non-PR/issue subjects are external-link rows when a deterministic browser URL is available.
- Never turn raw API URLs into browser links.
- Release browser URLs require tags, not release IDs; leave `web_url` empty unless a deterministic tag/html URL exists.
- Rows with no destination should be visibly disabled or explain that the link is unavailable.

## UI Contract

The Inbox UI lives in `packages/ui/src/views/InboxView.svelte` and is mounted by `frontend/src/App.svelte`.

Rules:

- Show `Draft UI` above the Inbox content while the feature remains early/flagged.
- Hide the header Inbox tab/select option when notifications are disabled.
- Direct `/inbox` access must still respect the feature flag.
- State, reason, type, repo, search, and sort filters belong in the route query so reload/share/back-forward preserve triage context.
- Bulk actions operate only on explicit selected visible rows; unbounded "mark all filtered" is out of scope.
- Sync status, queued propagation, retry failures, and terminal failures should be visible without blocking local triage.

## API Contract

Primary endpoints:

- `GET /api/v1/notifications`
- `POST /api/v1/notifications/sync`
- `POST /api/v1/notifications/read`
- `POST /api/v1/notifications/done`
- `POST /api/v1/notifications/undone`

Rules:

- All timestamps are UTC RFC3339 at API boundaries.
- Default list limit is bounded; max list and bulk mutation size are 200.
- Bulk responses return `{ succeeded, queued, failed }` based on rows actually mutated.
- Unknown or unmutated IDs belong in `failed`.
- API payload remains GitHub-shaped for now where existing clients depend on fields like `platform_thread_id` and `github_*` timestamps.
- Provider-neutral storage and DB naming must not leak through API accidentally; translate at server boundary.
- Generated OpenAPI clients must be regenerated after API shape changes.

## Testing Expectations

Use full-stack coverage for user-visible notification behavior.

- DB tests: state filters, monitored repo scope, host-qualified identity, read generation guards, retry metadata, closed-linked auto-done.
- GitHub tests: notification normalization, PR issue-style URL parsing, participating flag, host pagination/watermarks, rate-limit behavior.
- Server tests: feature-flag gating, bulk mutation result shape, sync status, disabled access, real SQLite API behavior.
- Frontend/store tests: route filters, disabled event refresh, sync polling, unavailable destination rows.
- Playwright e2e: Inbox listing/filtering/sync, disabled direct `/inbox`, bulk read/done, tight-height internal scroll layout.

Always run relevant Go tests with `-shuffle=on`. Use Bun for frontend tests and typechecks.
