# Notifications In Activity

Use this document for changes touching GitHub notifications, their presentation in the Activity feed, notification API handlers, notification sync, or notification persistence.

## Purpose

Notifications are a built-in signal that syncs the signed-in user's GitHub notification threads into SQLite, filters them to currently monitored repositories, and gives middleman local triage state that is separate from GitHub's read/unread flag.

There is no standalone inbox surface. Notifications are presented as rows **inside the Activity feed**, labelled by their reason (review requested, mentioned, assigned, etc.) and toggled by a dedicated "Notifications" filter. The backend still owns the mutable per-user state behind those rows:

- Activity events (PRs, issues, comments, reviews, commits) are immutable history across repos; notification rows are mutable, per-user state with unread/read, done/undone, queued GitHub read propagation, retry, and dead-letter metadata.
- A notification row and an event row may point at the same subject identity: `(platform_host, owner, repo, item_type, number)`; they coexist in the feed (e.g. a PR's `Opened` row and a `Review requested` notification row).
- The feed obtains notifications through the same `/activity` union as every other source (`db.ListActivity`, `activity_type = "notification"`), so cursor pagination, the time window, type filtering, and the safety cap apply uniformly.

## Always Enabled

Notifications are a built-in capability with no enable/disable setting. There is no `[notifications] enabled` key.

```toml
[notifications]
sync_interval = "2m"
propagation_interval = "1m"
batch_size = 25
```

Rules:

- `Config.NotificationsEnabled()` reports `c != nil`; the only "off" state is the absence of a loaded config, which callers use purely for nil-safety. There is no user-facing toggle.
- A legacy config that still carries `[notifications] enabled = false` loads without error — the key is ignored (it is not a deprecated-key error) and notifications stay on.
- The `[notifications]` section only tunes sync/propagation cadence and batch size.
- The Settings API reports `notifications.enabled = true` as a read-only status, not a configurable knob.

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
- The notification schema ships as a single migration, `000035_notifications.*`; do not split future assumptions across deleted branch-only migrations. Branch databases that already applied the abandoned notification migration at version 34 are repaired at startup by ensuring the current `000034_fleet_integration` artifacts exist before the database is accepted.

## Triage State Model

Middleman stores local workflow state separately from GitHub state. These states drive the notification list/mutation API; the Activity feed surfaces unread vs read via the row's `item_state`.

- `unread`: `done_at IS NULL AND unread = 1`.
- `active`: `done_at IS NULL`, regardless of unread.
- `read`: `done_at IS NULL AND unread = 0`.
- `done`: `done_at IS NOT NULL`.
- `all`: all monitored-repo notifications matching non-state filters.

Rules:

- `done_at` is local Octobox-style completion state.
- Marking a row done excludes it from the active/unread queries immediately.
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

- GitHub remains only implemented notification provider today. Provider support is declared through `ReadNotifications`/`NotificationMutation` in `platform.Capabilities`; the sync engine selects providers by those flags, never by hard-coded platform kind.
- GitLab and the gitealike (Forgejo/Gitea) providers ship notification stubs that return typed `unsupported_capability` errors until real support lands. Implementing a provider means replacing its stub bodies (GitLab: to-do items API; gitealike: `/notifications` endpoints on the transport) and flipping the two capability flags.
- The registry's `NotificationReader`/`NotificationMutator` accessors gate on the declared capability flags, not interface satisfaction, because stubs satisfy the interfaces.
- The sync engine intentionally requires BOTH `ReadNotifications` and `NotificationMutation` to select a provider: listing and read-ack propagation are treated as one feature today. A future read-only provider (list without upstream mark-read) would split this — select listing on `ReadNotifications` and propagation on `NotificationMutation` separately. Until such a provider exists the coupling keeps the path simple.
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

- Only PR/issue-anchored notifications are persisted. GitHub sends CheckSuite/CI, discussion, and release notifications with no subject number or browser URL, so sync skips any thread whose `item_type` is not `pr`/`issue` (or whose `item_number` is nil), and `listActivity` filters the notification union the same way for rows synced before this rule.
- Sync also skips `reason = "author"` ("Your thread") notifications: they fire for any activity on a thread the user opened and carry no displayable content beyond a `latest_comment_url` (a raw API URL) that already corresponds to a comment/review/state row in the feed. Comment, subscribed, and the attention-requesting reasons (mention, review_requested, assign, …) are kept.
- These two filters are enforced at sync (new rows) and in the Activity union (existing rows). The notification list/summary APIs (`GET /notifications`, summaries) are not UI-surfaced today and intentionally still return any rows already in `middleman_notification_items`; the Activity feed is the only filtered surface. If those APIs gain a UI, apply the same `item_type IN ('pr','issue') AND item_number IS NOT NULL AND reason != 'author'` filter there.
- Notification sync should process each configured provider host independently; one provider-host failure must not block others.
- Notification sync failures should update notification sync status so UI can surface them.
- Top-level manual sync also triggers notification sync.
- `/notifications/sync` triggers only notification sync and returns `202` once accepted.
- Sync watermark identity is `(platform, platform_host)`.
- First host sync may need GitHub `All: true`; later syncs should use persisted watermark/overlap to avoid full backlog scans.
- GitHub notification pagination must run until the provider reports no next page; do not use a fixed page cap for either the primary repo notification list or the participating-only annotation scan. A fixed cap can pin the watermark forever on large backlogs. The guardrail is the shared sync budget/rate reserve (`internal/github/notifications_sync.go::ensureNotificationPageBudget`), which should stop sync explicitly when upstream budget is exhausted (`internal/github/sync_test.go::TestSyncNotificationsReadsAllRepositoryNotificationPages`, `internal/github/sync_test.go::TestSyncNotificationsReadsAllParticipatingNotificationPages`).
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

Notifications render in the Activity feed (`packages/ui/src/components/ActivityFeed.svelte`, threaded and flat layouts) — there is no dedicated view, route, or header tab.

Rules:

- A notification row's reason rides in `body_preview` from the backend union; the feed maps it to a human label (`Review requested`, `Mentioned`, `Assigned`, …).
- The "Notifications" toggle lives in the activity filter dropdown's event-type group, is always present (notifications are always enabled), and defaults on. It is persisted by its own `notif` URL param, NOT by membership in the `types` list — a legacy `types` URL that lists every event but no `notification` must still mean "show everything", so the toggle cannot be inferred from list membership.
- `buildActivityFilterTypes` turns the dropdown state into the `/activity` `types` list (the backend filters by inclusion, so exclusion is an explicit list): the all-selected case stays the empty `[]` (backend returns everything); a partial event selection appends `notification` when the toggle is on; hiding notifications drops it from the list. Deselecting every event type while leaving Notifications on collapses to exactly `["notification"]` — a notifications-only feed where the PR/issue `Opened` anchor rows (`new_pr`/`new_issue`) do not leak in.
- Clicking a notification row opens its PR/issue in the detail pane when `(item_type, item_number)` resolve; otherwise it follows `web_url`.
- "Hide closed/merged" uses the shared `isClosedOrMergedActivity` helper (`packages/ui/src/components/activityRows.ts`) on every activity surface — flat, threaded, and the mobile activity view (`packages/ui/src/views/MobileActivityView.svelte`) — so a notification whose linked PR/issue is closed/merged is hidden on all of them, not just on desktop. The helper tests `subject_state` for notification rows (their `item_state` is unread/read, not a lifecycle state) and `item_state` for all other rows. Policy for unknown lifecycle: the filter only hides notifications whose linked state is *known*. When `subject_state` is empty or absent (the row is unanchored, or the linked PR/issue has not synced yet), the helper falls back to the notification's `item_state` (unread/read), which is never `closed`/`merged`, so the row stays visible rather than being hidden on a guess. It is hidden once its subject syncs and reports `closed`/`merged`.
- Unread notification rows (and only notification rows) carry a "Mark seen" control in both the flat and threaded layouts. It calls `POST /notifications/read` with the row's notification id (parsed from the `ntf:<id>` activity item id), which flips the row to read locally and queues the GitHub read propagation; the control then clears. Non-notification activity rows never get this control.
- The notification list/sync/triage API endpoints still exist for backend propagation; "Mark seen" is the only triage action surfaced in the feed. Bulk read/done/undone remain backend-only and are an intentional non-goal for the feed.
- Feed inclusion is historical: notification rows appear regardless of unread/read/done state within the Activity time window. `done_at` excludes a notification from the notification list API, not from the Activity feed; the feed is immutable history, not a triage queue.

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
- Activity notification rows carry the linked PR/issue lifecycle state in `subject_state` (allowed values `open` | `closed` | `merged`). `item_state` keeps carrying the notification's own `unread`/`read` triage state. `listActivity` populates `subject_state` via a `LEFT JOIN` to the merge-request/issue tables on `(repo_id, item_number)`, so the Hide closed/merged filter works in a notifications-only feed with no sibling PR/issue row present. The field is `omitempty`: when the subject is unanchored or has not synced, clients receive it absent rather than as `""`. Consumers must treat missing and empty string identically — both mean "lifecycle unknown / not applicable" — and must not treat unknown as `open`. Non-notification rows omit `subject_state` entirely; use `item_state` for them.

## Testing Expectations

Use full-stack coverage for user-visible notification behavior.

- DB tests: state filters, monitored repo scope, host-qualified identity, read generation guards, retry metadata, closed-linked auto-done.
- GitHub tests: notification normalization, PR issue-style URL parsing, participating flag, host pagination/watermarks, rate-limit behavior.
- Server tests: nil-config nil-safety (notifications served whenever a config is loaded), bulk mutation result shape, sync status, real SQLite API behavior. `internal/server/apitest/activity_notifications_test.go` is the full-stack guard: over real `/api/v1/activity` it asserts a notifications-only filter returns only notification rows, persisted unanchored ("ISSUE #0") and `author` notifications never surface, and a notification on a merged PR reports `item_state = unread` with `subject_state = merged`.
- Frontend/store tests: activity filter type construction (notification toggle on/off, notifications-only collapse to `["notification"]`, legacy URL normalization), feed rendering of notification rows.
- Playwright e2e (`frontend/tests/e2e-full/activity-notifications.spec.ts`): notifications appear as feed rows, the Notifications filter hides them, and "Mark seen" posts the read and clears the control.

Always run relevant Go tests with `-shuffle=on`. Use Bun for frontend tests and typechecks.
