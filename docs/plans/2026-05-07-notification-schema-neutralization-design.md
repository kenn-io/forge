# Notification Schema Neutralization Design

**Date:** 2026-05-07
**Status:** Draft for review
**Branch:** `mentions`

## Goal

Reshape notification persistence so current GitHub inbox feature stores provider-neutral data, while keeping current GitHub behavior unchanged. This work changes database shape and internal Go naming now, but does not implement GitLab, Gitea, or Forgejo notification ingestion yet.

## Non-goals

- Implement GitLab todo sync
- Implement Gitea or Forgejo notification sync
- Redesign inbox product behavior
- Force API or UI naming changes in this pass

## Context

Current notification persistence is GitHub-shaped in both table names and column names:

- `middleman_notifications`
- `middleman_notification_sync_state`
- `platform_thread_id`
- `github_updated_at`
- `github_last_read_at`
- `github_read_*`

That shape bakes in GitHub semantics and keys. It also keys sync watermark rows only by `platform_host`, which is too narrow for future multi-provider support. Even if providers usually have different default hosts, host alone is not correct identity boundary.

Because notification migrations only exist on this branch and have not been applied in shared history, we can rewrite them directly instead of adding repair migrations.

## Chosen approach

Adopt provider-neutral database shape and provider-neutral Go internals now, while keeping current API and UI behavior stable through mapping.

This is intentionally a GitHub-superset model with generic names, not a lowest-common-denominator model. Existing GitHub read propagation, retry, and done-state behavior stay intact, but persist through provider-neutral fields.

## Alternatives considered

### 1. Minimal rename only

Add `platform` to keys and rename a few obvious GitHub columns.

Rejected because it would leave core notification persistence conceptually GitHub-specific and would force another deeper cleanup before non-GitHub support lands.

### 2. Full rename across DB, Go, API, and UI

Rename everything now.

Rejected for this pass because it creates unnecessary user-facing churn. We only need neutral persistence and internals today.

### 3. Chosen: neutral DB and Go internals, stable API/UI

Rename schema and internal Go fields now. Preserve current API/UI contracts with server mapping where needed.

This gives future providers correct persistence shape without requiring immediate frontend/API migration.

## Schema design

### Notification items table

Rename:

- `middleman_notifications`
- to `middleman_notification_items`

Reason: each row is inbox item tracked from provider source. "Notification items" is more general than GitHub thread notifications and still reads clearly in code.

#### Columns

Core identity and repo linkage:

- `id INTEGER PRIMARY KEY AUTOINCREMENT`
- `platform TEXT NOT NULL DEFAULT 'github'`
- `platform_host TEXT NOT NULL DEFAULT 'github.com'`
- `platform_notification_id TEXT NOT NULL`
- `repo_id INTEGER REFERENCES middleman_repos(id) ON DELETE SET NULL`
- `repo_owner TEXT NOT NULL`
- `repo_name TEXT NOT NULL`

Subject and item metadata:

- `subject_type TEXT NOT NULL`
- `subject_title TEXT NOT NULL`
- `subject_url TEXT NOT NULL DEFAULT ''`
- `subject_latest_comment_url TEXT NOT NULL DEFAULT ''`
- `web_url TEXT NOT NULL DEFAULT ''`
- `item_number INTEGER`
- `item_type TEXT NOT NULL DEFAULT 'other'`
- `item_author TEXT NOT NULL DEFAULT ''`

Inbox state:

- `reason TEXT NOT NULL`
- `unread INTEGER NOT NULL DEFAULT 0`
- `participating INTEGER NOT NULL DEFAULT 0`
- `source_updated_at TEXT NOT NULL`
- `source_last_acknowledged_at TEXT`
- `synced_at TEXT NOT NULL`
- `done_at TEXT`
- `done_reason TEXT NOT NULL DEFAULT ''`

Provider action propagation state:

- `source_ack_queued_at TEXT`
- `source_ack_synced_at TEXT`
- `source_ack_generation_at TEXT`
- `source_ack_error TEXT NOT NULL DEFAULT ''`
- `source_ack_attempts INTEGER NOT NULL DEFAULT 0`
- `source_ack_last_attempt_at TEXT`
- `source_ack_next_attempt_at TEXT`

#### Uniqueness

Replace:

- `UNIQUE(platform_host, platform_thread_id)`

With:

- `UNIQUE(platform, platform_host, platform_notification_id)`

Reason: provider identity must include provider kind, not only host.

#### Indexes

Keep same access patterns, but point them at renamed columns and table:

- inbox ordering index on `(done_at, unread, source_updated_at DESC)`
- repo filter index on `(platform, platform_host, repo_owner, repo_name, source_updated_at DESC)`
- reason index on `(reason, source_updated_at DESC)`
- item lookup index on `(platform, platform_host, repo_owner, repo_name, item_type, item_number)`
- action queue index on `(platform, platform_host, source_ack_queued_at, source_ack_next_attempt_at, source_ack_synced_at)`

## Sync watermark table

Rename:

- `middleman_notification_sync_state`
- to `middleman_notification_sync_watermarks`

Reason: this table stores per-provider sync progress, not item state.

### Platform identity propagation

Adding `platform` to table keys is not enough by itself. Internal query and sync boundaries must also carry `platform` explicitly so host-only identity does not remain hidden in helpers.

Required internal shape:

- `db.Notification` includes `Platform string`
- `ListNotificationsOpts` adds `Platform string`
- `NotificationRepoFilter` adds `Platform string`
- sync watermark queries key by `(platform, platform_host)`
- notification action-queue queries key by `(platform, platform_host)`
- repo joins and notification lookup helpers match on provider-aware repo identity, not host alone
- tracked repo keys include `platform` as first segment

For current behavior, empty platform values at Go boundaries must canonicalize to `github` before inserts, updates, queue lookups, or watermark queries. Tests must prove legacy-style fixtures and zero-value upserts land under `platform='github'`.

### Columns

- `platform TEXT NOT NULL DEFAULT 'github'`
- `platform_host TEXT NOT NULL`
- `last_successful_sync_at TEXT NOT NULL`
- `last_full_sync_at TEXT`
- `sync_cursor TEXT NOT NULL DEFAULT ''`
- `tracked_repos_key TEXT NOT NULL DEFAULT ''`

This explicitly renames SQL column `tracked_repos` to `tracked_repos_key`.

`sync_cursor` is persisted even though GitHub leaves it empty for now. Current watermark get/update behavior must treat it as opaque provider-owned state: GitHub reads and writes `''`, future providers may store cursor tokens there without another schema rename.

### Primary key

Use:

- `PRIMARY KEY (platform, platform_host)`

Reason: watermark ownership is provider + host. `sync_cursor` is added now so future providers can persist opaque cursors without another schema rename. GitHub can leave it empty.

## Naming decisions

### Why `platform_notification_id`

This is general enough for GitHub thread IDs and future provider-native identifiers without pretending all providers use threads.

### Why `source_*`

`source_*` marks state tied to provider-side notification state rather than middleman-local triage state. That includes both provider timestamps and middleman-managed propagation metadata for synchronizing provider acknowledgement.

### Why `ack` instead of `read`

GitHub currently propagates mark-read semantics. Other providers may later propagate a close, acknowledge, clear, or read-style action. `ack` is broad enough to cover provider-side "user has acted on this inbox item" semantics while staying shorter than `acknowledgement`.

### Why keep `done_*`

`done_at` and `done_reason` are middleman-local triage state. They are already provider-neutral and intentionally separate from provider acknowledgement state.

## Internal Go model changes

Rename internal DB structs and fields to match neutral persistence.

### `db.Notification`

Rename fields:

- `PlatformThreadID` -> `PlatformNotificationID`
- `GitHubUpdatedAt` -> `SourceUpdatedAt`
- `GitHubLastReadAt` -> `SourceLastAcknowledgedAt`
- `GitHubReadQueuedAt` -> `SourceAckQueuedAt`
- `GitHubReadSyncedAt` -> `SourceAckSyncedAt`
- `GitHubReadGenerationAt` -> `SourceAckGenerationAt`
- `GitHubReadError` -> `SourceAckError`
- `GitHubReadAttempts` -> `SourceAckAttempts`
- `GitHubReadLastAttemptAt` -> `SourceAckLastAttemptAt`
- `GitHubReadNextAttemptAt` -> `SourceAckNextAttemptAt`

Add:

- `Platform string`

### `db.NotificationSyncWatermark`

Add and rename:

- `Platform string`
- `TrackedReposKey` stays as Go name, backed by SQL column `tracked_repos_key`
- `SyncCursor string`

### Query and method layer

Rename SQL column references, scan helpers, and internal query/mutation APIs to neutral names. Keep behavior identical.

Representative method renames expected in this pass:

- `ListQueuedNotificationReads` -> `ListQueuedNotificationAcks`
- `NotificationReadPropagationCurrent` -> `NotificationAckPropagationCurrent`
- `MarkNotificationReadPropagationResult` -> `MarkNotificationAckPropagationResult`
- `MarkNotificationsRead` -> `MarkNotificationsAcknowledged`

If any external API handler keeps read-oriented naming for compatibility, that compatibility should end at the server boundary, not in DB internals.

## Server/API compatibility

Do not force API contract changes in this pass.

Server response mapping can continue emitting current JSON fields if needed, even if database and internal structs become provider-neutral. This limits generated-client and UI churn while still moving persistence onto correct shape.

If any API response fields are already directly coupled to `db.Notification` field names, add explicit mapping instead of exposing database naming decisions.

## GitHub sync behavior

GitHub remains only implemented provider.

Expected behavior stays same:

- notification rows still sync from GitHub notifications API
- provider mark-read propagation still uses existing retry logic
- done-state reopening on newer source activity still works
- host ordering and tracked repo filtering still work

Only persistence names and keys change.

## Migration strategy

Because these migrations are branch-only and user confirmed they have not been applied in shared use, rewrite existing branch migrations directly:

- rewrite `internal/db/migrations/000019_notifications.up.sql`
- rewrite `internal/db/migrations/000019_notifications.down.sql`
- rewrite `internal/db/migrations/000020_notification_sync_state.up.sql`
- rewrite `internal/db/migrations/000020_notification_sync_state.down.sql`

Do not add repair migrations for this branch-only reshaping.

## Testing strategy

### Database tests

- update schema-open tests to expect renamed tables
- update migration regression test to verify new neutral tables are created from pre-notification schema version
- update query tests to use renamed columns through Go structs
- add coverage for `(platform, platform_host)` watermark identity
- add coverage proving empty or zero-value platform canonicalizes to `github`
- add coverage proving same host can store separate watermark rows for different platforms
- add coverage proving queue, filter, and watermark queries do not cross platform boundaries

### GitHub sync tests

- ensure current sync writes neutral fields correctly
- ensure action propagation queue logic still respects generations, retries, and reopened activity
- ensure provider lookup remains GitHub-only for now

### API and integration tests

- update only tests affected by internal mapping churn
- preserve current user-visible behavior unless deliberate API changes are made

## Risks

### Risk: partial neutralization leaks old names

If server or query helpers still depend on old field names, internal rename can become inconsistent.

Mitigation: rename DB structs comprehensively and add explicit mapping at API boundaries.

### Risk: future providers need more than `ack`

Some providers may need richer action state later.

Mitigation: current schema still allows adding provider-specific fields later. This pass only prevents obvious GitHub lock-in.

### Risk: branch migration rewrite collides with already-created local DBs

Some local developer DBs might already contain older branch schema.

Mitigation: document that branch-local DBs may need recreation if created from pre-rewrite branch history. Tests should validate fresh open and migration from pre-notification version.

## Open questions resolved

- Use truly provider-neutral database shape now: yes
- Keep GitHub-superset semantics under neutral names: yes
- Neutralize DB and Go internals now, keep API/UI stable: yes
- Rewrite branch-only notification migrations directly: yes

## Implementation outline

1. Rewrite notification migration SQL to create neutral tables and columns
2. Rename DB structs and query code to match new schema
3. Update GitHub notification sync code to write neutral fields
4. Add API mapping where internal rename would otherwise leak
5. Update tests for schema, queries, sync behavior, and API compatibility
6. Regenerate artifacts if API types change indirectly

## Success criteria

- Notification persistence no longer uses GitHub-specific table or column names
- Keys for notification items and sync watermarks include `platform`
- Current GitHub notification inbox behavior remains unchanged
- No GitLab, Gitea, or Forgejo notification implementation is added yet
- Tests cover neutral schema and preserved GitHub behavior
