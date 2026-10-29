# Notifications In Activity

Use this document for changes touching GitHub notifications, their presentation in the Activity feed, notification API handlers, notification sync, or notification persistence.

## Purpose

Notifications sync the signed-in user's GitHub notification threads, filter them to monitored
repositories, and give kenn-forge local triage state separate from GitHub's read/unread flag.

- There is no standalone inbox surface, route, or header tab. Notifications are rows **inside the
  Activity feed**, labelled by reason and toggled by a "Notifications" filter.
- Activity events are immutable history; notification rows are mutable per-user state (read,
  done, queued GitHub read propagation, retry, dead-letter).
- A notification row and an event row may share a subject and coexist in the feed (a PR's
  `Opened` row and a `Review requested` notification row).
- The feed gets notifications through the same `/activity` union as every other source, so
  pagination, the time window, type filtering, and the safety cap apply uniformly
  (`internal/db/queries_activity.go::ListActivity`).

## Always Enabled

- Notifications have no enable/disable setting and no `[notifications] enabled` key; the
  `[notifications]` section only tunes sync/propagation cadence and batch size.
- A legacy `[notifications] enabled = false` loads without error, is ignored, and notifications
  stay on. Settings reports `notifications.enabled = true` as read-only status.
- Discovery defaults to hourly because notifications spend the user's quota even with App-backed
  repo reads; read acknowledgements stay prompt (`internal/config/config.go::defaultNotificationSyncInterval`).

## Repository Scope And Identity

Notifications are user-scoped at the provider but repo-scoped in kenn-forge.

- Notification item identity is `(platform, platform_host, platform_notification_id)`. `platform`
  is required; a blank value is an error, never an implicit GitHub default.
- Show notifications only for the currently monitored repositories. Apply that scope as stable
  repository IDs inside SQL before ordering and limits; mutable routes or handler-side filtering
  can hide valid rows (`internal/server/activityapi/huma_routes.go::Handlers.trackedActivityRepoIDs`).
- Historical notifications for removed repos may stay stored but must not appear in any state
  view unless an explicit `include_unmonitored` contract is added.
- A non-null `repo_id` is authoritative across renames and route reuse; only legacy null-ID rows
  fall back to matching the current route (`internal/db/queries_notifications.go::DB.LatestOpenPRNotificationActivity`).
- `repo_id` is enrichment, not visibility authority.
- Sync watermarks are per repository identity, never host-wide; one repository's failing or
  unroutable credential must not hold back watermarks for healthy repositories on the same host.
- Repo facets and filters must be host-qualified when hosts are ambiguous (`github.com/acme/widget`).

## Persistence

- Notification persistence is provider-neutral even though only GitHub sync exists.
- `done_at`/`done_reason` are kenn-forge-local triage state; `source_*` fields track provider
  activity and acknowledgement propagation.
- `source_last_acknowledged_at` is set only after successful provider propagation or a sync
  reporting read, never when merely queued.

## Triage State Model

- `done_at` is local Octobox-style completion. Marking done or read applies locally at once;
  `mark_read=true` queues GitHub read propagation and never blocks on GitHub.
- `undone` clears only local `done_at`, unless linked PR/issue closure immediately re-closes it.
- When a linked monitored PR is closed/merged or a linked issue is closed, active notifications
  are marked done with `done_reason = 'closed'`.
- A done row re-enters active/unread only when GitHub reports newer unread activity than the local
  done/read generation; read-only GitHub updates must not reopen it.

## GitHub Read Propagation

Bulk actions are local-first; GitHub read-state propagation is asynchronous.

- Provider support is declared through the `ReadNotifications`/`NotificationMutation`
  capabilities; select providers by those flags, never by platform kind, and gate registry
  accessors on the flags, not interface satisfaction, because stubs satisfy the interfaces.
- GitLab and gitealike providers return typed `unsupported_capability` errors until real support
  lands. Sync requires BOTH flags today; a future read-only provider would split listing from
  propagation.
- Notification sync and acknowledgement propagation run only on a hub provider plane; spokes
  never construct local notification workers (`cmd/kenn-forge/main.go::run`).
- Propagation must revalidate the queued generation before calling the provider, and success and
  failure updates are both guarded by it; stale queued work must not mark newer activity read.
- After successful propagation, a stale `unread=true` payload no newer than the acknowledged
  generation must preserve local read state. Newer unread activity clears propagation state and
  reactivates the row.
- Read acknowledgements address the thread by global ID, so they outlive route renames. Linked
  rows without a current route stay queued but must not starve routable acknowledgements in the
  bounded batch (`internal/db/queries_notifications.go::DB.ListQueuedNotificationAcks`).
- A rate limit belongs to the credential that hit it: defer only that identity's repositories and
  keep propagating others (`internal/github/notifications_sync.go::Syncer.ProcessQueuedNotificationReads`).
- Rate limits pause retry without burning per-row attempts. Hitting the retry cap stops automatic
  retries and preserves local done/read state.
- During spoke preparation, propagation may finish only acknowledgements at or below the frozen
  admission generation, and sealing waits until none remain unsynced, including dead-lettered or
  unroutable ones; new read/done admissions are rejected while quiescing
  (`internal/db/queries_spoke_preparation.go::DB.CountUndrainedNotificationAcks`).

## Sync Behavior

- Persist only PR/issue-anchored notifications; GitHub sends CI, discussion, and release
  notifications with no subject number or browser URL.
- Skip `reason = "author"` ("Your thread") notifications: they carry nothing displayable beyond
  activity the feed already shows. Comment, subscribed, and attention reasons are kept.
- Both filters apply at sync and in the Activity union. The notification list/summary APIs are
  not UI-surfaced and intentionally unfiltered; apply the same filter if they gain a UI.
- Process each provider host independently; one host's failure must not block others, and
  failures update notification sync status so the UI can surface them.
- Every listing that can write a repository's rows must first provider-verify its stable
  repository ID; an occupied route is not identity proof. A 304 probe writes nothing
  (`internal/github/notifications_sync.go::Syncer.prepareNotificationRepo`).
- GitHub notifications are listed host-wide once per user identity per pass, never once per
  repository; threads for untracked or other-identity repositories are ignored
  (`internal/github/notifications_sync.go::Syncer.syncNotificationsForIdentity`).
- GitHub scopes notification `Last-Modified` to the user, not the URL, and a 304 costs no rate
  limit. Full syncs stay unconditional because read-state changes may not move the validator.
- Incremental passes send the validator only from the last pass that advanced every member
  repository's watermark; a 304 then skips identity checks and listing.
- GraphQL has no notification API.
- Paginate notification lists until the provider reports no next page, never with a fixed page
  cap, which can pin the watermark forever; the shared sync budget is the guardrail
  (`internal/github/notifications_sync.go::Syncer.ensureNotificationBudget`).
- Top-level manual sync also triggers notification sync; `/notifications/sync` triggers only
  notification sync and returns `202` once accepted.
- Notification workers stop with the server lifecycle before shared services are torn down.
- Closed/merged linked-notification completion must also run after repo/detail/list paths that
  persist closed PR or issue state, not only after notification sync.
- Closed-linked sweeps must stay on the item tables' `(repo_id, number)` index; an OR across
  linked and route-matched rows makes SQLite scan every merge request per notification
  (`internal/db/queries_notifications.go::closedLinkedNotificationSubject.sweepStatements`).

## Subject Links

- PR/issue notifications route to kenn-forge detail surfaces when host, owner, repo, and number
  are available. `PullRequest` subjects may arrive with issue-style API URLs; accept both.
- Other subjects are external-link rows only when a deterministic browser URL exists. Never turn
  raw API URLs into browser links.
- Release browser URLs require tags, not release IDs; leave `web_url` empty unless a
  deterministic tag/html URL exists.
- Rows with no destination are visibly disabled or explain that the link is unavailable.

## UI Contract

Notifications render in the Activity feed's threaded and flat layouts
(`frontend/src/lib/components/ActivityFeed.svelte`).

- A notification row's reason rides in the activity union's `body_preview` column; the feed maps
  it to a label such as `Review requested` (`frontend/src/lib/components/activityRows.ts::notificationReasonLabel`).
- The "Notifications" toggle is always present and defaults on. It persists in its own `notif` URL
  param, never by membership in `types`: a legacy `types` URL listing every event but no
  `notification` must still mean "show everything".
- PR and Issue toggles are independent: disabling either hides every attached row, while
  repository-level activity remains eligible (`frontend/src/lib/components/ActivityFeed.svelte::displayItems`).
- `types` is omitted only when everything is enabled; `types=notification` means notification-only
  with both item scopes enabled (`frontend/src/lib/stores/activity.svelte.ts::buildActivityFilterTypes`).
- An explicitly empty item scope sends the nonmatching `none` sentinel, even when another event
  type stays selected, so it never reads as "all".
- Item-scope selection, "Hide closed/merged", "Hide drafts", and "Hide bots" apply in SQL before
  ordering and limits (`internal/db/queries_activity.go::ListActivity`).
- Hide closed/merged uses the linked subject's lifecycle; unknown lifecycle stays visible rather
  than being guessed from read state. Hide drafts leaves unsynced subjects visible.
- Hide bots tests the actor for events and the item author for parent summaries
  (`internal/db/activity_visibility.go::IsBotLogin`).
- Clicking a notification row opens its PR/issue in the detail pane when it resolves; otherwise
  it follows `web_url`.
- Only unread notification rows carry a "Mark seen" control; it marks the row read locally and
  queues GitHub propagation. It is the only triage action in the feed; bulk read/done/undone are
  an intentional non-goal for the feed.
- A local "Mark seen" survives activity reads that started before it; only a later read may
  replace it (`frontend/src/lib/stores/activity.svelte.ts::projectOwnedNotificationStates`).
- Feed inclusion is historical: rows appear regardless of unread/read/done within the time
  window. `done_at` hides a notification from the list API, not from the feed.

## API Contract

- Max list and bulk mutation size is 200. Bulk responses return `{ succeeded, queued, failed }`
  from rows actually mutated; unknown or unmutated IDs go in `failed`.
- The API payload stays GitHub-shaped (`platform_thread_id`, `github_*` timestamps) where clients
  depend on it; provider-neutral storage naming must not leak through the API.
- Activity notification rows take title and URLs from the linked PR/issue once it has synced, and
  fall back to stored values only for unsynced subjects; stored values are frozen at sync time.
- Collapsed Activity returns an anchored notification directly until its PR/issue parent syncs
  (`internal/db/queries_activity_projection.go::DB.ListCollapsedActivityProjection`).
- Notification rows carry the linked lifecycle in `subject_state` (`open`/`closed`/`merged`) and
  their own triage state in `item_state`. Missing and empty `subject_state` both mean unknown and
  must never be treated as `open`; non-notification rows omit it.
