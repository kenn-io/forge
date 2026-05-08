# Notification Schema Neutralization Implementation Plan

> **For agentic workers:** REQUIRED: Use `/skill:orchestrator-implements` (in-session, orchestrator implements), `/skill:subagent-driven-development` (in-session, subagents implement), or `/skill:executing-plans` (parallel session) to implement this plan. Steps use checkbox syntax for tracking.

**Goal:** Reshape notification persistence and Go internals so current GitHub inbox behavior runs on provider-neutral tables, keys, and field names without implementing non-GitHub notification providers yet.

**Architecture:** Rewrite the branch-only notification migrations so SQLite creates neutral notification item and sync watermark tables keyed by `(platform, platform_host)`. Rename DB structs, query helpers, and GitHub sync internals to provider-neutral names, then preserve current API payloads by translating neutral DB fields back to existing GitHub-shaped response fields at the server boundary.

**Tech Stack:** Go, SQLite, Huma HTTP server, testify, existing e2e/integration tests, branch-local SQL migrations.

---

## Files and responsibilities

- Modify `internal/db/migrations/000019_notifications.up.sql`
  - Replace GitHub-shaped notification table with neutral `middleman_notification_items` schema, indexes, and unique keys.
- Modify `internal/db/migrations/000019_notifications.down.sql`
  - Drop neutral notification items table and associated indexes.
- Modify `internal/db/migrations/000020_notification_sync_state.up.sql`
  - Replace host-only sync state table with neutral `middleman_notification_sync_watermarks` keyed by `(platform, platform_host)` and including `sync_cursor`.
- Modify `internal/db/migrations/000020_notification_sync_state.down.sql`
  - Drop neutral watermark table.
- Modify `internal/db/db_test.go`
  - Update schema-open and migration regression tests to expect neutral table names.
- Modify `internal/db/types.go`
  - Rename notification structs and fields to provider-neutral names and add `Platform` / `SyncCursor` fields.
- Modify `internal/db/queries_notifications.go`
  - Rename SQL columns, canonicalization helpers, queue/watermark methods, and filter structs to provider-neutral terminology.
- Modify `internal/db/queries_notifications_test.go`
  - Cover provider defaulting, platform-aware watermark identity, and platform-isolated ack queues.
- Modify `internal/github/notifications_sync.go`
  - Thread `platform` through notification sync identity, tracked repo keys, watermark calls, and ack propagation.
- Modify `internal/github/sync_test.go`
  - Update and extend sync tests for platform-scoped watermark and ack behavior.
- Modify `internal/server/notifications.go`
  - Map neutral DB fields back into current API response field names and include `Platform` in repo scoping filters.
- Modify `internal/server/api_types.go`
  - Keep response JSON stable while internal field names change.
- Modify `internal/server/notifications_test.go`
  - Prove stable API payloads still expose `github_*` response names from neutral DB fields.
- No planned changes under `frontend/`, `packages/ui/`, `frontend/openapi/`, or `internal/apiclient/generated/`
  - Verification must confirm those trees stay unchanged.

---

### Task 1: Rewrite branch-only notification migrations and schema regression tests

**TDD scenario:** Modifying tested code — run existing tests first, then update tests for the new schema shape.

**Files:**
- Modify: `internal/db/db_test.go`
- Modify: `internal/db/migrations/000019_notifications.up.sql`
- Modify: `internal/db/migrations/000019_notifications.down.sql`
- Modify: `internal/db/migrations/000020_notification_sync_state.up.sql`
- Modify: `internal/db/migrations/000020_notification_sync_state.down.sql`

- [ ] **Step 1: Update schema tests to expect the neutral table names**

Replace the notification table expectations in `internal/db/db_test.go`.

```go
func TestOpenAndSchema(t *testing.T) {
	d := openTestDB(t)
	tables := []string{
		"middleman_repos",
		"middleman_merge_requests",
		"middleman_mr_events",
		"middleman_kanban_state",
		"middleman_labels",
		"middleman_merge_request_labels",
		"middleman_issue_labels",
		"middleman_repo_overviews",
		"middleman_notification_items",
		"middleman_notification_sync_watermarks",
	}
	for _, tbl := range tables {
		var name string
		err := d.ReadDB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl,
		).Scan(&name)
		require.NoErrorf(t, err, "table %s should exist", tbl)
	}
}

func TestOpenMigratesNotificationSchemaFromVersion18(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "notifications-v18.db")

	d, err := Open(path)
	require.NoError(err)
	require.NoError(d.Close())

	raw, err := sql.Open("sqlite", path)
	require.NoError(err)
	_, err = raw.Exec(`
		DROP TABLE IF EXISTS middleman_notification_sync_watermarks;
		DROP TABLE IF EXISTS middleman_notification_items;
		UPDATE schema_migrations SET version = 18, dirty = FALSE`)
	require.NoError(err)
	require.NoError(raw.Close())

	reopened, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(reopened.Close()) })

	require.True(tableExistsForTest(t, reopened.ReadDB(), "middleman_notification_items"))
	require.True(tableExistsForTest(t, reopened.ReadDB(), "middleman_notification_sync_watermarks"))
	require.True(hasIndex(reopened.ReadDB(), "idx_middleman_notification_items_inbox"))
	require.True(hasIndex(reopened.ReadDB(), "idx_middleman_notification_items_ack_queue"))
}
```

- [ ] **Step 2: Run the schema tests to verify they fail against the old migration shape**

Run:

```bash
go test ./internal/db -run 'TestOpenAndSchema|TestOpenMigratesNotificationSchemaFromVersion18' -shuffle=on
```

Expected: FAIL with missing `middleman_notification_items` / `middleman_notification_sync_watermarks` tables or indexes.

- [ ] **Step 3: Rewrite the branch-only migrations to create the neutral tables**

Replace `internal/db/migrations/000019_notifications.up.sql` with:

```sql
CREATE TABLE IF NOT EXISTS middleman_notification_items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    platform TEXT NOT NULL DEFAULT 'github',
    platform_host TEXT NOT NULL DEFAULT 'github.com',
    platform_notification_id TEXT NOT NULL,
    repo_id INTEGER REFERENCES middleman_repos(id) ON DELETE SET NULL,
    repo_owner TEXT NOT NULL,
    repo_name TEXT NOT NULL,
    subject_type TEXT NOT NULL,
    subject_title TEXT NOT NULL,
    subject_url TEXT NOT NULL DEFAULT '',
    subject_latest_comment_url TEXT NOT NULL DEFAULT '',
    web_url TEXT NOT NULL DEFAULT '',
    item_number INTEGER,
    item_type TEXT NOT NULL DEFAULT 'other',
    item_author TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL,
    unread INTEGER NOT NULL DEFAULT 0,
    participating INTEGER NOT NULL DEFAULT 0,
    source_updated_at TEXT NOT NULL,
    source_last_acknowledged_at TEXT,
    synced_at TEXT NOT NULL,
    done_at TEXT,
    done_reason TEXT NOT NULL DEFAULT '',
    source_ack_queued_at TEXT,
    source_ack_synced_at TEXT,
    source_ack_generation_at TEXT,
    source_ack_error TEXT NOT NULL DEFAULT '',
    source_ack_attempts INTEGER NOT NULL DEFAULT 0,
    source_ack_last_attempt_at TEXT,
    source_ack_next_attempt_at TEXT,
    UNIQUE(platform, platform_host, platform_notification_id)
);

CREATE INDEX IF NOT EXISTS idx_middleman_notification_items_inbox
    ON middleman_notification_items(done_at, unread, source_updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_middleman_notification_items_repo
    ON middleman_notification_items(platform, platform_host, repo_owner, repo_name, source_updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_middleman_notification_items_reason
    ON middleman_notification_items(reason, source_updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_middleman_notification_items_item
    ON middleman_notification_items(platform, platform_host, repo_owner, repo_name, item_type, item_number);
CREATE INDEX IF NOT EXISTS idx_middleman_notification_items_ack_queue
    ON middleman_notification_items(platform, platform_host, source_ack_queued_at, source_ack_next_attempt_at, source_ack_synced_at);
```

Replace `internal/db/migrations/000019_notifications.down.sql` with:

```sql
DROP INDEX IF EXISTS idx_middleman_notification_items_ack_queue;
DROP INDEX IF EXISTS idx_middleman_notification_items_item;
DROP INDEX IF EXISTS idx_middleman_notification_items_reason;
DROP INDEX IF EXISTS idx_middleman_notification_items_repo;
DROP INDEX IF EXISTS idx_middleman_notification_items_inbox;
DROP TABLE IF EXISTS middleman_notification_items;
```

Replace `internal/db/migrations/000020_notification_sync_state.up.sql` with:

```sql
CREATE TABLE IF NOT EXISTS middleman_notification_sync_watermarks (
    platform TEXT NOT NULL DEFAULT 'github',
    platform_host TEXT NOT NULL,
    last_successful_sync_at TEXT NOT NULL,
    last_full_sync_at TEXT,
    sync_cursor TEXT NOT NULL DEFAULT '',
    tracked_repos_key TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (platform, platform_host)
);
```

Replace `internal/db/migrations/000020_notification_sync_state.down.sql` with:

```sql
DROP TABLE IF EXISTS middleman_notification_sync_watermarks;
```

- [ ] **Step 4: Run the schema tests again to verify they pass**

Run:

```bash
go test ./internal/db -run 'TestOpenAndSchema|TestOpenMigratesNotificationSchemaFromVersion18' -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Commit the migration rewrite**

```bash
git add internal/db/migrations/000019_notifications.up.sql \
  internal/db/migrations/000019_notifications.down.sql \
  internal/db/migrations/000020_notification_sync_state.up.sql \
  internal/db/migrations/000020_notification_sync_state.down.sql \
  internal/db/db_test.go
git commit -m "refactor: neutralize notification persistence schema"
```

---

### Task 2: Rename DB notification types and queries to provider-neutral names

**TDD scenario:** New feature coverage for renamed behavior plus modified existing code.

**Files:**
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries_notifications.go`
- Modify: `internal/db/queries_notifications_test.go`

- [ ] **Step 1: Add failing DB tests for platform defaulting and platform-aware watermarks**

Append these tests to `internal/db/queries_notifications_test.go` near the existing notification tests.

```go
func TestUpsertNotificationsDefaultsPlatformToGitHub(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	itemNumber := 42

	require.NoError(d.UpsertNotifications(t.Context(), []Notification{{
		PlatformNotificationID: "thread-42",
		RepoID:                 &repoID,
		RepoOwner:              "acme",
		RepoName:               "widget",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Review requested",
		WebURL:                 "https://github.com/acme/widget/pull/42",
		ItemNumber:             &itemNumber,
		ItemType:               "pr",
		ItemAuthor:             "octocat",
		Reason:                 "review_requested",
		Unread:                 true,
		Participating:          true,
		SourceUpdatedAt:        now,
		SyncedAt:               now,
	}}))

	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "all"})
	require.NoError(err)
	require.Len(items, 1)
	require.Equal("github", items[0].Platform)
	require.Equal("github.com", items[0].PlatformHost)
}

func TestNotificationSyncWatermarksAreScopedByPlatformAndHost(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	full := now.Add(-time.Hour)

	require.NoError(d.UpdateNotificationSyncWatermark(t.Context(), "github", "code.example.com", now, &full, "", "github/code.example.com/acme/widget"))
	require.NoError(d.UpdateNotificationSyncWatermark(t.Context(), "gitlab", "code.example.com", now.Add(time.Minute), nil, "cursor-2", "gitlab/code.example.com/acme/widget"))

	githubWatermark, err := d.GetNotificationSyncWatermark(t.Context(), "github", "code.example.com", "github/code.example.com/acme/widget")
	require.NoError(err)
	require.NotNil(githubWatermark)
	require.Equal("github", githubWatermark.Platform)
	require.Equal("", githubWatermark.SyncCursor)

	gitlabWatermark, err := d.GetNotificationSyncWatermark(t.Context(), "gitlab", "code.example.com", "gitlab/code.example.com/acme/widget")
	require.NoError(err)
	require.NotNil(gitlabWatermark)
	require.Equal("gitlab", gitlabWatermark.Platform)
	require.Equal("cursor-2", gitlabWatermark.SyncCursor)
}

func TestQueuedNotificationAcksStayWithinPlatformAndHost(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	githubItem := notificationFixture("shared-thread", "mention", now)
	githubItem.Platform = "github"
	githubItem.PlatformNotificationID = "shared-thread"
	githubItem.RepoID = &repoID

	gitlabItem := notificationFixture("shared-thread", "mention", now)
	gitlabItem.Platform = "gitlab"
	gitlabItem.PlatformHost = "code.example.com"
	gitlabItem.PlatformNotificationID = "shared-thread"
	gitlabItem.RepoID = &repoID

	require.NoError(d.UpsertNotifications(t.Context(), []Notification{githubItem, gitlabItem}))
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "all"})
	require.NoError(err)
	require.Len(items, 2)

	queuedAt := now.Add(time.Minute)
	queuedIDs, err := d.QueueNotificationIDsRead(t.Context(), []int64{items[0].ID}, queuedAt)
	require.NoError(err)
	require.Len(queuedIDs, 1)

	queued, err := d.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, queuedAt)
	require.NoError(err)
	require.Len(queued, 1)
	require.Equal("github", queued[0].Platform)
	other, err := d.ListQueuedNotificationAcks(t.Context(), "gitlab", "code.example.com", 10, queuedAt)
	require.NoError(err)
	require.Empty(other)
}
```

- [ ] **Step 2: Run the DB notification tests to verify they fail before implementation**

Run:

```bash
go test ./internal/db -run 'TestUpsertNotificationsDefaultsPlatformToGitHub|TestNotificationSyncWatermarksAreScopedByPlatformAndHost|TestQueuedNotificationAcksStayWithinPlatformAndHost' -shuffle=on
```

Expected: FAIL or compile errors because the neutral fields, methods, and table names do not exist yet.

- [ ] **Step 3: Rename notification structs, canonicalization, SQL columns, and method signatures**

Update `internal/db/types.go` so the notification structs become neutral:

```go
type Notification struct {
	ID                       int64
	Platform                 string
	PlatformHost             string
	PlatformNotificationID   string
	RepoID                   *int64
	RepoOwner                string
	RepoName                 string
	SubjectType              string
	SubjectTitle             string
	SubjectURL               string
	SubjectLatestCommentURL  string
	WebURL                   string
	ItemNumber               *int
	ItemType                 string
	ItemAuthor               string
	Reason                   string
	Unread                   bool
	Participating            bool
	SourceUpdatedAt          time.Time
	SourceLastAcknowledgedAt *time.Time
	SyncedAt                 time.Time
	DoneAt                   *time.Time
	DoneReason               string
	SourceAckQueuedAt        *time.Time
	SourceAckSyncedAt        *time.Time
	SourceAckGenerationAt    *time.Time
	SourceAckError           string
	SourceAckAttempts        int
	SourceAckLastAttemptAt   *time.Time
	SourceAckNextAttemptAt   *time.Time
}

type ListNotificationsOpts struct {
	Platform     string
	PlatformHost string
	RepoOwner    string
	RepoName     string
	Repos        []NotificationRepoFilter
	State        string
	Reasons      []string
	ItemTypes    []string
	Search       string
	Sort         string
	Limit        int
	Offset       int
}

type NotificationRepoFilter struct {
	Platform     string
	PlatformHost string
	RepoOwner    string
	RepoName     string
}

type NotificationSyncWatermark struct {
	Platform             string
	LastSuccessfulSyncAt time.Time
	LastFullSyncAt       *time.Time
	SyncCursor           string
	TrackedReposKey      string
}
```

Update `internal/db/queries_notifications.go` so canonicalization and query boundaries default and carry `platform`:

```go
func canonicalizeNotification(n *Notification) {
	if n == nil {
		return
	}
	if n.Platform == "" {
		n.Platform = "github"
	}
	if n.PlatformHost == "" {
		n.PlatformHost = "github.com"
	}
	n.PlatformHost, n.RepoOwner, n.RepoName = canonicalRepoIdentifier(n.PlatformHost, n.RepoOwner, n.RepoName)
	n.SourceUpdatedAt = canonicalUTCTime(n.SourceUpdatedAt)
	n.SourceLastAcknowledgedAt = canonicalUTCTimePtr(n.SourceLastAcknowledgedAt)
	n.SourceAckQueuedAt = canonicalUTCTimePtr(n.SourceAckQueuedAt)
	n.SourceAckSyncedAt = canonicalUTCTimePtr(n.SourceAckSyncedAt)
	n.SourceAckGenerationAt = canonicalUTCTimePtr(n.SourceAckGenerationAt)
	n.SourceAckLastAttemptAt = canonicalUTCTimePtr(n.SourceAckLastAttemptAt)
	n.SourceAckNextAttemptAt = canonicalUTCTimePtr(n.SourceAckNextAttemptAt)
	if !n.Unread && n.SourceAckGenerationAt == nil && !n.SourceUpdatedAt.IsZero() {
		generation := n.SourceUpdatedAt
		n.SourceAckGenerationAt = &generation
	}
}

const notificationSelectColumns = `n.id, n.platform, n.platform_host, n.platform_notification_id, n.repo_id, n.repo_owner, n.repo_name,
	n.subject_type, n.subject_title, n.subject_url, n.subject_latest_comment_url, n.web_url,
	n.item_number, n.item_type, n.item_author, n.reason, n.unread, n.participating,
	n.source_updated_at, n.source_last_acknowledged_at, n.synced_at, n.done_at, n.done_reason,
	n.source_ack_queued_at, n.source_ack_synced_at, n.source_ack_generation_at, n.source_ack_error, n.source_ack_attempts,
	n.source_ack_last_attempt_at, n.source_ack_next_attempt_at`

func (d *DB) GetNotificationSyncWatermark(ctx context.Context, platform, host, trackedReposKey string) (*NotificationSyncWatermark, error)
func (d *DB) UpdateNotificationSyncWatermark(ctx context.Context, platform, host string, syncedAt time.Time, lastFullSyncedAt *time.Time, syncCursor string, trackedReposKey string) error
func (d *DB) MarkNotificationsAcknowledged(ctx context.Context, platform, host string, notificationIDs []string, acknowledgedAt time.Time) error
func (d *DB) ListQueuedNotificationAcks(ctx context.Context, platform, host string, limit int, now time.Time) ([]Notification, error)
func (d *DB) NotificationAckPropagationCurrent(ctx context.Context, id int64, queuedAt *time.Time, sourceUpdatedAt time.Time) (bool, error)
func (d *DB) MarkNotificationAckPropagationResult(ctx context.Context, id int64, queuedAt *time.Time, sourceUpdatedAt time.Time, syncedAt *time.Time, errText string, nextAttemptAt *time.Time) error
```

Use `middleman_notification_items` and `middleman_notification_sync_watermarks` consistently in all SQL. All repo filters and queue lookups must match on both `platform` and `platform_host`.

- [ ] **Step 4: Run the full DB package to verify the renamed persistence layer stays green**

Run:

```bash
go test ./internal/db -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Commit the DB neutralization**

```bash
git add internal/db/types.go \
  internal/db/queries_notifications.go \
  internal/db/queries_notifications_test.go
git commit -m "refactor: neutralize notification db internals"
```

---

### Task 3: Update GitHub notification sync to use neutral identity and ack fields

**TDD scenario:** Modifying code with existing tests — update targeted sync tests first, then adapt the implementation.

**Files:**
- Modify: `internal/github/notifications_sync.go`
- Modify: `internal/github/sync_test.go`

- [ ] **Step 1: Update and extend sync tests for platform-aware watermark and ack calls**

In `internal/github/sync_test.go`, update the existing watermark tests to use the new DB signatures and add a focused key-format test.

```go
func TestNotificationTrackedReposKeyIncludesPlatform(t *testing.T) {
	require := require.New(t)
	tracked := map[string]RepoRef{}
	tracked[notificationRepoKey("github", "github.com", "acme", "widget")] = RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
	}
	tracked[notificationRepoKey("gitlab", "code.example.com", "acme", "widget")] = RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}

	require.Equal("github/github.com/acme/widget", notificationTrackedReposKey("github", "github.com", tracked))
	require.Equal("gitlab/code.example.com/acme/widget", notificationTrackedReposKey("gitlab", "code.example.com", tracked))
}

func TestSyncNotificationsUsesPersistedSinceWatermark(t *testing.T) {
	// existing setup stays the same, but use platform-aware watermark calls
	watermarkKey := notificationRepoKey("github", "github.com", "acme", "widget")
	require.NoError(d.UpdateNotificationSyncWatermark(t.Context(), "github", "github.com", watermark, &lastFullSyncAt, "", watermarkKey))
	...
}
```

Update the queued-ack tests in the same file to call the renamed DB methods and neutral fields:

```go
queued, err := d.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, queuedAt)
require.NoError(err)
require.NoError(d.MarkNotificationAckPropagationResult(t.Context(), queued[0].ID, queued[0].SourceAckQueuedAt, queued[0].SourceUpdatedAt, &syncedAt, "", nil))
```

- [ ] **Step 2: Run the targeted GitHub sync tests to verify they fail before the code changes**

Run:

```bash
go test ./internal/github -run 'TestNotificationTrackedReposKeyIncludesPlatform|TestSyncNotificationsUsesPersistedSinceWatermark|TestSyncNotificationsClearsSinceWhenTrackedReposChange|TestProcessQueuedNotificationReadsPausesOnRateLimitWithoutConsumingAttempts' -shuffle=on
```

Expected: FAIL or compile errors because the sync code still uses host-only keys and old DB method names.

- [ ] **Step 3: Thread `platform` through the GitHub notification sync internals**

Update `internal/github/notifications_sync.go` to carry provider identity end-to-end.

```go
type notificationHostClient struct {
	platform platform.Kind
	host     string
	client   Client
}

func (s *Syncer) notificationClients() []notificationHostClient {
	providers := s.clients.Providers()
	clients := make([]notificationHostClient, 0, len(providers))
	for _, provider := range providers {
		if provider.Platform() != platform.KindGitHub {
			continue
		}
		legacy, ok := provider.(interface{ GitHubClient() Client })
		if !ok || legacy.GitHubClient() == nil {
			continue
		}
		clients = append(clients, notificationHostClient{
			platform: provider.Platform(),
			host:     normalizedPlatformHost(provider.Host()),
			client:   legacy.GitHubClient(),
		})
	}
	sort.Slice(clients, func(i, j int) bool {
		if clients[i].platform != clients[j].platform {
			return clients[i].platform < clients[j].platform
		}
		return clients[i].host < clients[j].host
	})
	return clients
}

func notificationRepoKey(platform, host, owner, name string) string {
	return platform + "/" + normalizedPlatformHost(host) + "/" + strings.ToLower(owner) + "/" + strings.ToLower(name)
}

func notificationTrackedReposKey(platform, host string, tracked map[string]RepoRef) string {
	prefix := platform + "/" + normalizedPlatformHost(host) + "/"
	...
}
```

Use the neutral DB methods everywhere in this file:

```go
watermark, err := s.db.GetNotificationSyncWatermark(ctx, string(entry.platform), entry.host, trackedReposKey)
...
if err := s.db.UpdateNotificationSyncWatermark(ctx, string(entry.platform), entry.host, startedAt, lastFullSyncAt, "", trackedReposKey); err != nil {
	return fmt.Errorf("store notification sync watermark for %s/%s: %w", entry.platform, entry.host, err)
}
...
queued, err := s.db.ListQueuedNotificationAcks(ctx, "github", host, batchSize, time.Now().UTC())
...
current, err := s.db.NotificationAckPropagationCurrent(ctx, notification.ID, notification.SourceAckQueuedAt, notification.SourceUpdatedAt)
...
if err := s.db.MarkNotificationAckPropagationResult(ctx, notification.ID, notification.SourceAckQueuedAt, notification.SourceUpdatedAt, &syncedAt, "", nil); err != nil {
	return err
}
```

Keep GitHub behavior unchanged: GitHub remains the only provider returned by `notificationClients()`.

- [ ] **Step 4: Run the full GitHub package tests**

Run:

```bash
go test ./internal/github -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Commit the sync-layer neutralization**

```bash
git add internal/github/notifications_sync.go internal/github/sync_test.go
git commit -m "refactor: key notification sync by provider and host"
```

---

### Task 4: Preserve current notification API payloads over neutral DB fields

**TDD scenario:** Modifying tested code — add a focused compatibility test first, then update server mapping.

**Files:**
- Modify: `internal/server/notifications.go`
- Modify: `internal/server/api_types.go`
- Modify: `internal/server/notifications_test.go`

- [ ] **Step 1: Add a failing server test that proves GitHub-shaped JSON remains stable**

Append this test to `internal/server/notifications_test.go` near the other list/read tests.

```go
func TestNotificationsAPIMapsNeutralFieldsToExistingGitHubJSON(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), "github.com", "acme", "widget")
	require.NoError(err)

	number := 42
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	lastRead := now.Add(-time.Minute)
	queued := now.Add(time.Minute)
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{{
		Platform:                 "github",
		PlatformHost:             "github.com",
		PlatformNotificationID:   "thread-42",
		RepoID:                   &repoID,
		RepoOwner:                "acme",
		RepoName:                 "widget",
		SubjectType:              "PullRequest",
		SubjectTitle:             "Review requested",
		WebURL:                   "https://github.com/acme/widget/pull/42",
		ItemNumber:               &number,
		ItemType:                 "pr",
		ItemAuthor:               "octocat",
		Reason:                   "review_requested",
		Unread:                   true,
		Participating:            true,
		SourceUpdatedAt:          now,
		SourceLastAcknowledgedAt: &lastRead,
		SourceAckQueuedAt:        &queued,
		SourceAckError:           "rate limited",
		SourceAckAttempts:        2,
		SyncedAt:                 now,
	}}))

	s := New(database, nil, nil, "/", notificationsEnabledConfig(), ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	listed := getNotificationsForTest(t, ts.URL, "all")
	require.Len(listed.Items, 1)
	item := listed.Items[0]
	assert.Equal("thread-42", item.PlatformThreadID)
	assert.Equal(now.Format(time.RFC3339), item.GitHubUpdatedAt)
	assert.Equal(lastRead.Format(time.RFC3339), item.GitHubLastReadAt)
	assert.Equal(queued.Format(time.RFC3339), item.GitHubReadQueuedAt)
	assert.Equal("rate limited", item.GitHubReadError)
	assert.Equal(2, item.GitHubReadAttempts)
}
```

- [ ] **Step 2: Run the notification server tests to verify the compatibility test fails first**

Run:

```bash
go test ./internal/server -run 'TestNotificationsAPIListsAndQueuesReadWithoutDone|TestNotificationsAPIMapsNeutralFieldsToExistingGitHubJSON' -shuffle=on
```

Expected: FAIL or compile errors because the server still reads old DB field names.

- [ ] **Step 3: Update the server boundary to map neutral DB fields back to the existing API shape**

Keep the JSON contract stable in `internal/server/api_types.go`, but update `internal/server/notifications.go` to read the renamed DB fields and propagate `Platform` in repo filters.

```go
func notificationRepoFilters(repos []ghclient.RepoRef) []db.NotificationRepoFilter {
	if len(repos) == 0 {
		return []db.NotificationRepoFilter{{}}
	}
	filters := make([]db.NotificationRepoFilter, 0, len(repos))
	for _, repo := range repos {
		filters = append(filters, db.NotificationRepoFilter{
			Platform:     string(repo.Platform),
			PlatformHost: repo.PlatformHost,
			RepoOwner:    repo.Owner,
			RepoName:     repo.Name,
		})
	}
	return filters
}

func toNotificationResponse(n db.Notification) notificationResponse {
	resp := notificationResponse{
		ID:                      n.ID,
		PlatformHost:            n.PlatformHost,
		Provider:                n.Platform,
		RepoPath:                strings.Trim(n.RepoOwner+"/"+n.RepoName, "/"),
		PlatformThreadID:        n.PlatformNotificationID,
		RepoOwner:               n.RepoOwner,
		RepoName:                n.RepoName,
		SubjectType:             n.SubjectType,
		SubjectTitle:            n.SubjectTitle,
		SubjectURL:              n.SubjectURL,
		SubjectLatestCommentURL: n.SubjectLatestCommentURL,
		WebURL:                  n.WebURL,
		ItemNumber:              n.ItemNumber,
		ItemType:                n.ItemType,
		ItemAuthor:              n.ItemAuthor,
		Reason:                  n.Reason,
		Unread:                  n.Unread,
		Participating:           n.Participating,
		GitHubUpdatedAt:         formatUTCRFC3339(n.SourceUpdatedAt),
		DoneReason:              n.DoneReason,
		GitHubReadError:         n.SourceAckError,
		GitHubReadAttempts:      n.SourceAckAttempts,
	}
	assignTime := func(value *time.Time) string {
		if value == nil {
			return ""
		}
		return formatUTCRFC3339(*value)
	}
	resp.GitHubLastReadAt = assignTime(n.SourceLastAcknowledgedAt)
	resp.DoneAt = assignTime(n.DoneAt)
	resp.GitHubReadQueuedAt = assignTime(n.SourceAckQueuedAt)
	resp.GitHubReadSyncedAt = assignTime(n.SourceAckSyncedAt)
	resp.GitHubReadLastAttemptAt = assignTime(n.SourceAckLastAttemptAt)
	resp.GitHubReadNextAttemptAt = assignTime(n.SourceAckNextAttemptAt)
	if resp.Provider == "" {
		resp.Provider = "github"
	}
	return resp
}
```

Do not rename JSON fields in `notificationResponse` during this task.

- [ ] **Step 4: Run server tests, full short suite, and artifact guardrail verification**

Run:

```bash
go test ./internal/server -shuffle=on
make test-short
git diff --exit-code -- frontend/openapi internal/apiclient/generated packages/ui/src/api/generated
```

Expected:
- `go test ./internal/server -shuffle=on`: PASS
- `make test-short`: PASS
- `git diff --exit-code -- ...`: no output and exit 0

- [ ] **Step 5: Commit the compatibility layer**

```bash
git add internal/server/notifications.go internal/server/api_types.go internal/server/notifications_test.go
git commit -m "fix: preserve notification API payloads over neutral schema"
```

---

## Self-review against spec

- Provider-neutral schema: covered in Task 1.
- Provider-neutral Go internals and platform-aware boundaries: covered in Tasks 2 and 3.
- Stable current API shape: covered in Task 4.
- No non-GitHub implementation: enforced in Task 3 by keeping `notificationClients()` GitHub-only.
- Testing for defaulting, cross-platform isolation, and watermark identity: covered in Tasks 2 and 3.
- No frontend or generated-client churn expected: explicitly verified in Task 4.
