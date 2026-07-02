# GitLab Discussion ID Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add GitLab discussion_id support to enable automated threaded replies and discussion resolution.

**Architecture:** Extend the event tables with discussion metadata columns, switch GitLab sync from Notes API to Discussions API, and add new endpoints for replying to and resolving discussions. The schema is provider-agnostic so other providers can add discussion support later.

**Tech Stack:** Go, SQLite migrations, Huma/OpenAPI, GitLab API v4 via `gitlab.com/gitlab-org/api/client-go`, testify for tests.

---

## File Structure

| File | Purpose |
|------|---------|
| `internal/db/migrations/000025_gitlab_discussion_ids.up.sql` | Add discussion columns to event tables |
| `internal/db/migrations/000025_gitlab_discussion_ids.down.sql` | Rollback migration |
| `internal/db/types.go` | Extend MREvent and IssueEvent structs |
| `internal/db/queries.go` | Update upsert/select queries for new columns |
| `internal/platform/types.go` | Extend platform event types and capabilities |
| `internal/platform/client.go` | Add DiscussionReplier and DiscussionResolver interfaces |
| `internal/platform/persist.go` | Map new platform fields to DB fields |
| `internal/platform/gitlab/normalize.go` | New discussion normalization functions |
| `internal/platform/gitlab/client.go` | Switch to Discussions API, update capabilities |
| `internal/platform/gitlab/mutation.go` | New file for reply/resolve mutations |
| `internal/server/huma_routes.go` | New discussion reply/resolve endpoints |
| `internal/server/api_types.go` | Update capability response type |

---

## Task 1: Database Migration

**Files:**
- Create: `internal/db/migrations/000025_gitlab_discussion_ids.up.sql`
- Create: `internal/db/migrations/000025_gitlab_discussion_ids.down.sql`

- [ ] **Step 1: Create up migration**

```sql
-- internal/db/migrations/000025_gitlab_discussion_ids.up.sql
ALTER TABLE middleman_mr_events ADD COLUMN discussion_id TEXT;
ALTER TABLE middleman_mr_events ADD COLUMN position_json TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_mr_events ADD COLUMN resolvable INTEGER NOT NULL DEFAULT 0;
ALTER TABLE middleman_mr_events ADD COLUMN resolved INTEGER NOT NULL DEFAULT 0;

ALTER TABLE middleman_issue_events ADD COLUMN discussion_id TEXT;

CREATE INDEX idx_mr_events_discussion
    ON middleman_mr_events(discussion_id) WHERE discussion_id IS NOT NULL;
```

- [ ] **Step 2: Create down migration**

```sql
-- internal/db/migrations/000025_gitlab_discussion_ids.down.sql
DROP INDEX IF EXISTS idx_mr_events_discussion;

ALTER TABLE middleman_mr_events DROP COLUMN discussion_id;
ALTER TABLE middleman_mr_events DROP COLUMN position_json;
ALTER TABLE middleman_mr_events DROP COLUMN resolvable;
ALTER TABLE middleman_mr_events DROP COLUMN resolved;

ALTER TABLE middleman_issue_events DROP COLUMN discussion_id;
```

- [ ] **Step 3: Run migration test**

Run: `go test ./internal/db -run TestMigrations -shuffle=on`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/db/migrations/000025_gitlab_discussion_ids.up.sql internal/db/migrations/000025_gitlab_discussion_ids.down.sql
git commit -m "feat(db): add discussion_id columns to event tables"
```

---

## Task 2: DB Types Extension

**Files:**
- Modify: `internal/db/types.go`

- [ ] **Step 1: Extend MREvent struct**

Add after `DedupeKey string` field (around line 238):

```go
type MREvent struct {
	ID                 int64
	MergeRequestID     int64
	PlatformID         *int64
	PlatformExternalID string
	EventType          string
	Author             string
	Summary            string
	Body               string
	MetadataJSON       string
	CreatedAt          time.Time
	DedupeKey          string
	DiscussionID       *string
	PositionJSON       string
	Resolvable         bool
	Resolved           bool
}
```

- [ ] **Step 2: Extend IssueEvent struct**

Add after `DedupeKey string` field (around line 301):

```go
type IssueEvent struct {
	ID                 int64
	IssueID            int64
	PlatformID         *int64
	PlatformExternalID string
	EventType          string
	Author             string
	Summary            string
	Body               string
	MetadataJSON       string
	CreatedAt          time.Time
	DedupeKey          string
	DiscussionID       *string
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: Build succeeds

- [ ] **Step 4: Commit**

```bash
git add internal/db/types.go
git commit -m "feat(db): extend MREvent and IssueEvent with discussion fields"
```

---

## Task 3: Platform Types Extension

**Files:**
- Modify: `internal/platform/types.go`

- [ ] **Step 1: Extend MergeRequestEvent struct**

Add after `DedupeKey string` field (around line 140):

```go
type MergeRequestEvent struct {
	Repo               RepoRef
	PlatformID         int64
	PlatformExternalID string
	MergeRequestNumber int
	EventType          string
	Author             string
	Summary            string
	Body               string
	MetadataJSON       string
	CreatedAt          time.Time
	DedupeKey          string
	DiscussionID       string
	PositionJSON       string
	Resolvable         bool
	Resolved           bool
}
```

- [ ] **Step 2: Extend IssueEvent struct**

Add after `DedupeKey string` field (around line 154):

```go
type IssueEvent struct {
	Repo               RepoRef
	PlatformID         int64
	PlatformExternalID string
	IssueNumber        int
	EventType          string
	Author             string
	Summary            string
	Body               string
	MetadataJSON       string
	CreatedAt          time.Time
	DedupeKey          string
	DiscussionID       string
}
```

- [ ] **Step 3: Extend Capabilities struct**

Add after `LabelMutation bool` field (around line 213):

```go
type Capabilities struct {
	ReadRepositories  bool
	ReadMergeRequests bool
	ReadIssues        bool
	ReadComments      bool
	ReadReleases      bool
	ReadCI            bool
	ReadLabels        bool
	CommentMutation   bool
	StateMutation     bool
	MergeMutation     bool
	ReviewMutation    bool
	WorkflowApproval  bool
	ReadyForReview    bool
	IssueMutation     bool
	LabelMutation     bool
	DiscussionReply   bool
	DiscussionResolve bool
}
```

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: Build succeeds

- [ ] **Step 5: Commit**

```bash
git add internal/platform/types.go
git commit -m "feat(platform): extend event types and capabilities for discussions"
```

---

## Task 4: Platform Interfaces

**Files:**
- Modify: `internal/platform/client.go`

- [ ] **Step 1: Add DiscussionReplier interface**

Add after the `ReviewMutator` interface (around line 110):

```go
type DiscussionReplier interface {
	ReplyToDiscussion(
		ctx context.Context,
		ref RepoRef,
		number int,
		discussionID string,
		body string,
	) (MergeRequestEvent, error)
}
```

- [ ] **Step 2: Add DiscussionResolver interface**

Add after `DiscussionReplier`:

```go
type DiscussionResolver interface {
	ResolveDiscussion(
		ctx context.Context,
		ref RepoRef,
		number int,
		discussionID string,
		resolved bool,
	) error
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: Build succeeds

- [ ] **Step 4: Commit**

```bash
git add internal/platform/client.go
git commit -m "feat(platform): add DiscussionReplier and DiscussionResolver interfaces"
```

---

## Task 5: Persistence Layer Update

**Files:**
- Modify: `internal/platform/persist.go`

- [ ] **Step 1: Update DBMREvent function**

Update the function to map new fields:

```go
func DBMREvent(mrID int64, event MergeRequestEvent) db.MREvent {
	out := db.MREvent{
		MergeRequestID:     mrID,
		PlatformExternalID: event.PlatformExternalID,
		EventType:          event.EventType,
		Author:             event.Author,
		Summary:            event.Summary,
		Body:               event.Body,
		MetadataJSON:       event.MetadataJSON,
		CreatedAt:          event.CreatedAt,
		DedupeKey:          event.DedupeKey,
		PositionJSON:       event.PositionJSON,
		Resolvable:         event.Resolvable,
		Resolved:           event.Resolved,
	}
	if event.PlatformID != 0 || event.EventType == "issue_comment" || event.EventType == "review" {
		platformID := event.PlatformID
		out.PlatformID = &platformID
	}
	if event.DiscussionID != "" {
		out.DiscussionID = &event.DiscussionID
	}
	return out
}
```

- [ ] **Step 2: Update DBIssueEvent function**

Update the function to map new fields:

```go
func DBIssueEvent(issueID int64, event IssueEvent) db.IssueEvent {
	out := db.IssueEvent{
		IssueID:            issueID,
		PlatformExternalID: event.PlatformExternalID,
		EventType:          event.EventType,
		Author:             event.Author,
		Summary:            event.Summary,
		Body:               event.Body,
		MetadataJSON:       event.MetadataJSON,
		CreatedAt:          event.CreatedAt,
		DedupeKey:          event.DedupeKey,
	}
	if event.PlatformID != 0 || event.EventType == "issue_comment" {
		platformID := event.PlatformID
		out.PlatformID = &platformID
	}
	if event.DiscussionID != "" {
		out.DiscussionID = &event.DiscussionID
	}
	return out
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: Build succeeds

- [ ] **Step 4: Commit**

```bash
git add internal/platform/persist.go
git commit -m "feat(platform): map discussion fields in persistence layer"
```

---

## Task 6: DB Queries Update

**Files:**
- Modify: `internal/db/queries.go`
- Test: `internal/db/queries_test.go`

- [ ] **Step 1: Write failing test for MR event upsert with discussion_id**

Add to `internal/db/queries_test.go`:

```go
func TestUpsertMREventsWithDiscussionID(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	mrID := insertTestMR(t, d, repoID, 1, "discussion test", base)
	platformID := int64(101)
	discussionID := "abc123def"

	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &platformID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "needs fix",
		CreatedAt:      base,
		DedupeKey:      "note-101",
		DiscussionID:   &discussionID,
		PositionJSON:   `{"new_path":"main.go","new_line":42}`,
		Resolvable:     true,
		Resolved:       false,
	}}))

	events, err := d.ListMREvents(ctx, mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.NotNil(events[0].DiscussionID)
	assert.Equal("abc123def", *events[0].DiscussionID)
	assert.Equal(`{"new_path":"main.go","new_line":42}`, events[0].PositionJSON)
	assert.True(events[0].Resolvable)
	assert.False(events[0].Resolved)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db -run TestUpsertMREventsWithDiscussionID -shuffle=on`
Expected: FAIL (columns not in query)

- [ ] **Step 3: Update UpsertMREvents query**

Find the `UpsertMREvents` function (around line 2425) and update:

```go
func (d *DB) UpsertMREvents(ctx context.Context, events []MREvent) error {
	if len(events) == 0 {
		return nil
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO middleman_mr_events
			    (merge_request_id, platform_id, platform_external_id, event_type, author, summary, body,
			     metadata_json, created_at, dedupe_key, discussion_id, position_json, resolvable, resolved)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(merge_request_id, dedupe_key) DO UPDATE SET
			    platform_id   = excluded.platform_id,
			    platform_external_id = excluded.platform_external_id,
			    event_type    = excluded.event_type,
			    author        = excluded.author,
			    summary       = excluded.summary,
			    body          = excluded.body,
			    metadata_json = excluded.metadata_json,
			    created_at    = excluded.created_at,
			    discussion_id = excluded.discussion_id,
			    position_json = excluded.position_json,
			    resolvable    = excluded.resolvable,
			    resolved      = excluded.resolved`)
		if err != nil {
			return fmt.Errorf("prepare upsert mr events: %w", err)
		}
		defer stmt.Close()

		for i := range events {
			e := &events[i]
			canonicalizeMREventTimestamps(e)
			if _, err := stmt.ExecContext(ctx,
				e.MergeRequestID, e.PlatformID, e.PlatformExternalID, e.EventType, e.Author, e.Summary, e.Body,
				e.MetadataJSON, e.CreatedAt, e.DedupeKey, e.DiscussionID, e.PositionJSON, e.Resolvable, e.Resolved,
			); err != nil {
				return fmt.Errorf("upsert mr event: %w", err)
			}
		}
		return nil
	})
}
```

- [ ] **Step 4: Update ListMREvents query**

Find the `ListMREvents` function (around line 2534) and update:

```go
func (d *DB) ListMREvents(ctx context.Context, mrID int64) ([]MREvent, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT id, merge_request_id, platform_id, platform_external_id, event_type, author, summary, body,
		       metadata_json, created_at, dedupe_key, discussion_id, position_json, resolvable, resolved
		FROM middleman_mr_events
		WHERE merge_request_id = ?
		ORDER BY created_at DESC`, mrID,
	)
	if err != nil {
		return nil, fmt.Errorf("list mr events: %w", err)
	}
	defer rows.Close()

	var events []MREvent
	for rows.Next() {
		var e MREvent
		var createdAtStr string
		if err := rows.Scan(
			&e.ID, &e.MergeRequestID, &e.PlatformID, &e.PlatformExternalID, &e.EventType, &e.Author, &e.Summary,
			&e.Body, &e.MetadataJSON, &createdAtStr, &e.DedupeKey, &e.DiscussionID, &e.PositionJSON, &e.Resolvable, &e.Resolved,
		); err != nil {
			return nil, fmt.Errorf("scan mr event: %w", err)
		}
		t, err := parseDBTime(createdAtStr)
		if err != nil {
			return nil, fmt.Errorf(
				"parse mr event created_at %q: %w",
				createdAtStr, err)
		}
		e.CreatedAt = t
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mr events: %w", err)
	}
	return events, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/db -run TestUpsertMREventsWithDiscussionID -shuffle=on`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/db/queries.go internal/db/queries_test.go
git commit -m "feat(db): add discussion fields to MR event queries"
```

---

## Task 7: Issue Event Queries Update

**Files:**
- Modify: `internal/db/queries.go`
- Test: `internal/db/queries_test.go`

- [ ] **Step 1: Write failing test for issue event upsert with discussion_id**

Add to `internal/db/queries_test.go`:

```go
func TestUpsertIssueEventsWithDiscussionID(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	issueID, err := d.UpsertIssue(ctx, &Issue{
		RepoID:         repoID,
		PlatformID:     301,
		Number:         5,
		URL:            "https://gitlab.com/acme/widget/-/issues/5",
		Title:          "discussion test",
		Author:         "reporter",
		State:          "open",
		CreatedAt:      base,
		UpdatedAt:      base,
		LastActivityAt: base,
	})
	require.NoError(err)

	platformID := int64(501)
	discussionID := "issue-disc-xyz"

	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID:      issueID,
		PlatformID:   &platformID,
		EventType:    "issue_comment",
		Author:       "commenter",
		Body:         "issue comment",
		CreatedAt:    base,
		DedupeKey:    "issue-note-501",
		DiscussionID: &discussionID,
	}}))

	events, err := d.ListIssueEvents(ctx, issueID)
	require.NoError(err)
	require.Len(events, 1)
	assert.NotNil(events[0].DiscussionID)
	assert.Equal("issue-disc-xyz", *events[0].DiscussionID)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db -run TestUpsertIssueEventsWithDiscussionID -shuffle=on`
Expected: FAIL (columns not in query)

- [ ] **Step 3: Update UpsertIssueEvents query**

Find the `UpsertIssueEvents` function (around line 3561) and update:

```go
func (d *DB) UpsertIssueEvents(ctx context.Context, events []IssueEvent) error {
	if len(events) == 0 {
		return nil
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO middleman_issue_events
			    (issue_id, platform_id, platform_external_id, event_type, author, summary, body,
			     metadata_json, created_at, dedupe_key, discussion_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(issue_id, dedupe_key) DO UPDATE SET
			    issue_id       = excluded.issue_id,
			    platform_id    = excluded.platform_id,
			    platform_external_id = excluded.platform_external_id,
			    event_type     = excluded.event_type,
			    author         = excluded.author,
			    summary        = excluded.summary,
			    body           = excluded.body,
			    metadata_json  = excluded.metadata_json,
			    created_at     = excluded.created_at,
			    discussion_id  = excluded.discussion_id`)
		if err != nil {
			return fmt.Errorf("prepare upsert issue events: %w", err)
		}
		defer stmt.Close()

		for i := range events {
			e := &events[i]
			canonicalizeIssueEventTimestamps(e)
			if _, err := stmt.ExecContext(ctx,
				e.IssueID, e.PlatformID, e.PlatformExternalID, e.EventType, e.Author,
				e.Summary, e.Body, e.MetadataJSON, e.CreatedAt,
				e.DedupeKey, e.DiscussionID,
			); err != nil {
				return fmt.Errorf("upsert issue event: %w", err)
			}
		}
		return nil
	})
}
```

- [ ] **Step 4: Update ListIssueEvents query**

Find the `ListIssueEvents` function and update to include `discussion_id`:

```go
func (d *DB) ListIssueEvents(ctx context.Context, issueID int64) ([]IssueEvent, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT id, issue_id, platform_id, platform_external_id, event_type, author, summary, body,
		       metadata_json, created_at, dedupe_key, discussion_id
		FROM middleman_issue_events
		WHERE issue_id = ?
		ORDER BY created_at DESC`, issueID,
	)
	if err != nil {
		return nil, fmt.Errorf("list issue events: %w", err)
	}
	defer rows.Close()

	var events []IssueEvent
	for rows.Next() {
		var e IssueEvent
		var createdAtStr string
		if err := rows.Scan(
			&e.ID, &e.IssueID, &e.PlatformID, &e.PlatformExternalID, &e.EventType, &e.Author, &e.Summary,
			&e.Body, &e.MetadataJSON, &createdAtStr, &e.DedupeKey, &e.DiscussionID,
		); err != nil {
			return nil, fmt.Errorf("scan issue event: %w", err)
		}
		t, err := parseDBTime(createdAtStr)
		if err != nil {
			return nil, fmt.Errorf("parse issue event created_at %q: %w", createdAtStr, err)
		}
		e.CreatedAt = t
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate issue events: %w", err)
	}
	return events, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/db -run TestUpsertIssueEventsWithDiscussionID -shuffle=on`
Expected: PASS

- [ ] **Step 6: Run all DB tests**

Run: `go test ./internal/db -shuffle=on`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/db/queries.go internal/db/queries_test.go
git commit -m "feat(db): add discussion_id to issue event queries"
```

---

## Task 8: GitLab Discussion Normalization

**Files:**
- Modify: `internal/platform/gitlab/normalize.go`
- Test: `internal/platform/gitlab/normalize_test.go`

- [ ] **Step 1: Write failing test for discussion normalization**

Add to `internal/platform/gitlab/normalize_test.go`:

```go
func TestNormalizeMergeRequestDiscussions(t *testing.T) {
	assert := Assert.New(t)
	repo := platform.RepoRef{
		Platform: platform.KindGitLab,
		Host:     "gitlab.com",
		Owner:    "acme",
		Name:     "widget",
		RepoPath: "acme/widget",
	}

	discussions := []*gitlab.Discussion{
		{
			ID: "disc-abc123",
			Notes: []*gitlab.Note{
				{
					ID:         101,
					Body:       "needs fix on this line",
					System:     false,
					Resolvable: true,
					Resolved:   false,
					Author:     &gitlab.NoteAuthor{Username: "reviewer"},
					CreatedAt:  timePtr(time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)),
					Position: &gitlab.NotePosition{
						NewPath: "main.go",
						NewLine: 42,
					},
				},
				{
					ID:         102,
					Body:       "fixed!",
					System:     false,
					Resolvable: true,
					Resolved:   true,
					Author:     &gitlab.NoteAuthor{Username: "author"},
					CreatedAt:  timePtr(time.Date(2026, 5, 22, 11, 0, 0, 0, time.UTC)),
				},
			},
		},
		{
			ID: "disc-def456",
			Notes: []*gitlab.Note{
				{
					ID:     201,
					Body:   "system note",
					System: true,
					Author: &gitlab.NoteAuthor{Username: "gitlab"},
				},
				{
					ID:         202,
					Body:       "general comment",
					System:     false,
					Resolvable: false,
					Author:     &gitlab.NoteAuthor{Username: "commenter"},
					CreatedAt:  timePtr(time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)),
				},
			},
		},
	}

	events := NormalizeMergeRequestDiscussions(repo, 7, discussions)

	// Should have 3 events (system note filtered)
	assert.Len(events, 3)

	// First note from first discussion
	assert.Equal("disc-abc123", events[0].DiscussionID)
	assert.Equal("reviewer", events[0].Author)
	assert.Equal("needs fix on this line", events[0].Body)
	assert.True(events[0].Resolvable)
	assert.False(events[0].Resolved)
	assert.Contains(events[0].PositionJSON, "main.go")
	assert.Contains(events[0].PositionJSON, "42")

	// Second note from first discussion
	assert.Equal("disc-abc123", events[1].DiscussionID)
	assert.Equal("author", events[1].Author)
	assert.True(events[1].Resolved)

	// Note from second discussion (system note skipped)
	assert.Equal("disc-def456", events[2].DiscussionID)
	assert.Equal("commenter", events[2].Author)
	assert.False(events[2].Resolvable)
}

func timePtr(t time.Time) *time.Time {
	return &t
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/gitlab -run TestNormalizeMergeRequestDiscussions -shuffle=on`
Expected: FAIL (function not defined)

- [ ] **Step 3: Implement NormalizeMergeRequestDiscussions**

Add to `internal/platform/gitlab/normalize.go`:

```go
func NormalizeMergeRequestDiscussions(
	repo platform.RepoRef,
	mrNumber int,
	discussions []*gitlab.Discussion,
) []platform.MergeRequestEvent {
	var events []platform.MergeRequestEvent
	for _, discussion := range discussions {
		if discussion == nil {
			continue
		}
		for _, note := range discussion.Notes {
			if note == nil || note.System {
				continue
			}
			events = append(events, platform.MergeRequestEvent{
				Repo:               repo,
				PlatformID:         note.ID,
				PlatformExternalID: strconv.FormatInt(note.ID, 10),
				MergeRequestNumber: mrNumber,
				EventType:          "issue_comment",
				Author:             noteAuthorUsername(note),
				Body:               note.Body,
				CreatedAt:          timeValue(note.CreatedAt),
				DedupeKey:          noteDedupeKey(repo, "mr", mrNumber, "note", strconv.FormatInt(note.ID, 10)),
				DiscussionID:       discussion.ID,
				PositionJSON:       serializeNotePosition(note.Position),
				Resolvable:         note.Resolvable,
				Resolved:           note.Resolved,
			})
		}
	}
	return events
}

func noteAuthorUsername(note *gitlab.Note) string {
	if note == nil || note.Author == nil {
		return ""
	}
	return note.Author.Username
}

func serializeNotePosition(pos *gitlab.NotePosition) string {
	if pos == nil {
		return ""
	}
	data, err := json.Marshal(pos)
	if err != nil {
		return ""
	}
	return string(data)
}
```

Add import for `encoding/json` at the top of the file.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/platform/gitlab -run TestNormalizeMergeRequestDiscussions -shuffle=on`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/platform/gitlab/normalize.go internal/platform/gitlab/normalize_test.go
git commit -m "feat(gitlab): add NormalizeMergeRequestDiscussions function"
```

---

## Task 9: GitLab Issue Discussion Normalization

**Files:**
- Modify: `internal/platform/gitlab/normalize.go`
- Test: `internal/platform/gitlab/normalize_test.go`

- [ ] **Step 1: Write failing test for issue discussion normalization**

Add to `internal/platform/gitlab/normalize_test.go`:

```go
func TestNormalizeIssueDiscussions(t *testing.T) {
	assert := Assert.New(t)
	repo := platform.RepoRef{
		Platform: platform.KindGitLab,
		Host:     "gitlab.com",
		Owner:    "acme",
		Name:     "widget",
		RepoPath: "acme/widget",
	}

	discussions := []*gitlab.Discussion{
		{
			ID: "issue-disc-111",
			Notes: []*gitlab.Note{
				{
					ID:        301,
					Body:      "I can reproduce this",
					System:    false,
					Author:    &gitlab.NoteAuthor{Username: "reporter"},
					CreatedAt: timePtr(time.Date(2026, 5, 22, 14, 0, 0, 0, time.UTC)),
				},
			},
		},
	}

	events := NormalizeIssueDiscussions(repo, 10, discussions)

	assert.Len(events, 1)
	assert.Equal("issue-disc-111", events[0].DiscussionID)
	assert.Equal("reporter", events[0].Author)
	assert.Equal(10, events[0].IssueNumber)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/gitlab -run TestNormalizeIssueDiscussions -shuffle=on`
Expected: FAIL (function not defined)

- [ ] **Step 3: Implement NormalizeIssueDiscussions**

Add to `internal/platform/gitlab/normalize.go`:

```go
func NormalizeIssueDiscussions(
	repo platform.RepoRef,
	issueNumber int,
	discussions []*gitlab.Discussion,
) []platform.IssueEvent {
	var events []platform.IssueEvent
	for _, discussion := range discussions {
		if discussion == nil {
			continue
		}
		for _, note := range discussion.Notes {
			if note == nil || note.System {
				continue
			}
			events = append(events, platform.IssueEvent{
				Repo:               repo,
				PlatformID:         note.ID,
				PlatformExternalID: strconv.FormatInt(note.ID, 10),
				IssueNumber:        issueNumber,
				EventType:          "issue_comment",
				Author:             noteAuthorUsername(note),
				Body:               note.Body,
				CreatedAt:          timeValue(note.CreatedAt),
				DedupeKey:          noteDedupeKey(repo, "issue", issueNumber, "note", strconv.FormatInt(note.ID, 10)),
				DiscussionID:       discussion.ID,
			})
		}
	}
	return events
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/platform/gitlab -run TestNormalizeIssueDiscussions -shuffle=on`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/platform/gitlab/normalize.go internal/platform/gitlab/normalize_test.go
git commit -m "feat(gitlab): add NormalizeIssueDiscussions function"
```

---

## Task 10: GitLab Client - Switch to Discussions API

**Files:**
- Modify: `internal/platform/gitlab/client.go`
- Test: `internal/platform/gitlab/client_test.go`

- [ ] **Step 1: Add listMergeRequestDiscussions function**

Add after `listMergeRequestNotes` (around line 467):

```go
func (c *Client) listMergeRequestDiscussions(ctx context.Context, pid any, number int) ([]*gitlab.Discussion, error) {
	opt := &gitlab.ListMergeRequestDiscussionsOptions{ListOptions: gitlab.ListOptions{Page: 1, PerPage: defaultPageSize}}
	var out []*gitlab.Discussion
	for {
		discussions, resp, err := c.api.Discussions.ListMergeRequestDiscussions(pid, int64(number), opt, gitlab.WithContext(ctx))
		if err != nil {
			return nil, mapGitLabError("list_merge_request_discussions", err)
		}
		out = append(out, discussions...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}
```

- [ ] **Step 2: Add listIssueDiscussions function**

Add after `listIssueNotes`:

```go
func (c *Client) listIssueDiscussions(ctx context.Context, pid any, number int) ([]*gitlab.Discussion, error) {
	opt := &gitlab.ListIssueDiscussionsOptions{ListOptions: gitlab.ListOptions{Page: 1, PerPage: defaultPageSize}}
	var out []*gitlab.Discussion
	for {
		discussions, resp, err := c.api.Discussions.ListIssueDiscussions(pid, int64(number), opt, gitlab.WithContext(ctx))
		if err != nil {
			return nil, mapGitLabError("list_issue_discussions", err)
		}
		out = append(out, discussions...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}
```

- [ ] **Step 3: Update ListMergeRequestEvents to use discussions**

Replace the `ListMergeRequestEvents` function (around line 298):

```go
func (c *Client) ListMergeRequestEvents(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.MergeRequestEvent, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return nil, err
	}
	discussions, err := c.listMergeRequestDiscussions(ctx, pid, number)
	if err != nil {
		return nil, err
	}
	commits, err := c.listMergeRequestCommits(ctx, pid, number)
	if err != nil {
		return nil, err
	}

	events := NormalizeMergeRequestDiscussions(normalizedRef, number, discussions)
	for _, commit := range commits {
		events = append(events, NormalizeCommitEvent(normalizedRef, number, commit))
	}
	return events, nil
}
```

- [ ] **Step 4: Update ListIssueEvents to use discussions**

Replace the `ListIssueEvents` function (around line 365):

```go
func (c *Client) ListIssueEvents(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.IssueEvent, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return nil, err
	}
	discussions, err := c.listIssueDiscussions(ctx, pid, number)
	if err != nil {
		return nil, err
	}
	return NormalizeIssueDiscussions(normalizedRef, number, discussions), nil
}
```

- [ ] **Step 5: Verify build**

Run: `go build ./...`
Expected: Build succeeds

- [ ] **Step 6: Run existing GitLab tests**

Run: `go test ./internal/platform/gitlab -shuffle=on`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/platform/gitlab/client.go
git commit -m "feat(gitlab): switch from Notes to Discussions API"
```

---

## Task 11: GitLab Capabilities and Mutations

**Files:**
- Modify: `internal/platform/gitlab/client.go`
- Create: `internal/platform/gitlab/mutation.go`

- [ ] **Step 1: Update capabilities**

Update the `Capabilities` method in `client.go`:

```go
func (c *Client) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		ReadComments:      true,
		ReadReleases:      true,
		ReadCI:            true,
		DiscussionReply:   true,
		DiscussionResolve: true,
	}
}
```

- [ ] **Step 2: Create mutation.go with ReplyToDiscussion**

Create `internal/platform/gitlab/mutation.go`:

```go
package gitlab

import (
	"context"
	"strconv"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"go.kenn.io/middleman/internal/platform"
)

func (c *Client) ReplyToDiscussion(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	discussionID string,
	body string,
) (platform.MergeRequestEvent, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}

	note, _, err := c.api.Discussions.AddMergeRequestDiscussionNote(
		pid,
		int64(number),
		discussionID,
		&gitlab.AddMergeRequestDiscussionNoteOptions{Body: &body},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.MergeRequestEvent{}, mapGitLabError("reply_to_discussion", err)
	}

	return platform.MergeRequestEvent{
		Repo:               normalizedRef,
		PlatformID:         note.ID,
		PlatformExternalID: strconv.FormatInt(note.ID, 10),
		MergeRequestNumber: number,
		EventType:          "issue_comment",
		Author:             noteAuthorUsername(note),
		Body:               note.Body,
		CreatedAt:          timeValue(note.CreatedAt),
		DiscussionID:       discussionID,
		Resolvable:         note.Resolvable,
		Resolved:           note.Resolved,
	}, nil
}

func (c *Client) ResolveDiscussion(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	discussionID string,
	resolved bool,
) error {
	pid, _, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return err
	}

	_, _, err = c.api.Discussions.ResolveMergeRequestDiscussion(
		pid,
		int64(number),
		discussionID,
		&gitlab.ResolveMergeRequestDiscussionOptions{Resolved: &resolved},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return mapGitLabError("resolve_discussion", err)
	}
	return nil
}

func noteAuthorUsername(note *gitlab.Note) string {
	if note == nil || note.Author == nil {
		return ""
	}
	return note.Author.Username
}
```

- [ ] **Step 3: Remove duplicate noteAuthorUsername from normalize.go**

The function is now in mutation.go, so remove it from normalize.go if it was added there.

- [ ] **Step 4: Add interface assertions to client.go**

Add at the bottom of `client.go`:

```go
var _ platform.DiscussionReplier = (*Client)(nil)
var _ platform.DiscussionResolver = (*Client)(nil)
```

- [ ] **Step 5: Verify build**

Run: `go build ./...`
Expected: Build succeeds

- [ ] **Step 6: Commit**

```bash
git add internal/platform/gitlab/client.go internal/platform/gitlab/mutation.go internal/platform/gitlab/normalize.go
git commit -m "feat(gitlab): add discussion reply and resolve mutations"
```

---

## Task 12: API Types Update

**Files:**
- Modify: `internal/server/api_types.go`

- [ ] **Step 1: Add discussion capabilities to response type**

Update `providerCapabilitiesResponse` struct:

```go
type providerCapabilitiesResponse struct {
	ReadRepositories  bool `json:"read_repositories"`
	ReadMergeRequests bool `json:"read_merge_requests"`
	ReadIssues        bool `json:"read_issues"`
	ReadComments      bool `json:"read_comments"`
	ReadReleases      bool `json:"read_releases"`
	ReadCI            bool `json:"read_ci"`
	ReadLabels        bool `json:"read_labels"`
	CommentMutation   bool `json:"comment_mutation"`
	StateMutation     bool `json:"state_mutation"`
	MergeMutation     bool `json:"merge_mutation"`
	ReviewMutation    bool `json:"review_mutation"`
	WorkflowApproval  bool `json:"workflow_approval"`
	ReadyForReview    bool `json:"ready_for_review"`
	IssueMutation     bool `json:"issue_mutation"`
	LabelMutation     bool `json:"label_mutation"`
	DiscussionReply   bool `json:"discussion_reply"`
	DiscussionResolve bool `json:"discussion_resolve"`
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: Build succeeds

- [ ] **Step 3: Commit**

```bash
git add internal/server/api_types.go
git commit -m "feat(api): add discussion capabilities to response type"
```

---

## Task 13: Discussion Reply Endpoint

**Files:**
- Modify: `internal/server/huma_routes.go`
- Test: `internal/server/e2etest/gitlab_discussions_test.go`

- [ ] **Step 1: Add input/output types for discussion endpoints**

Add to `huma_routes.go` near the other input types:

```go
type replyToDiscussionInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	DiscussionID string `path:"discussion_id"`
	Body         struct {
		Body string `json:"body"`
	}
}

type replyToDiscussionOutput = createdOutput[db.MREvent]

type resolveDiscussionInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	DiscussionID string `path:"discussion_id"`
	Body         struct {
		Resolved bool `json:"resolved"`
	}
}

type resolveDiscussionOutput = okStatusOutput
```

- [ ] **Step 2: Create test file for discussion endpoints**

Create `internal/server/e2etest/gitlab_discussions_test.go`:

```go
package e2etest

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
)

func TestGetPRDetailIncludesDiscussionID(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	env := newTestEnv(t)
	database := env.Database

	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)

	mrID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         7,
		URL:            "https://gitlab.com/acme/widget/-/merge_requests/7",
		Title:          "Discussion test",
		Author:         "author",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	discussionID := "disc-abc123"
	platformID := int64(101)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &platformID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "needs fix",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "note-101",
		DiscussionID:   &discussionID,
		PositionJSON:   `{"new_path":"main.go","new_line":42}`,
		Resolvable:     true,
		Resolved:       false,
	}}))

	resp := env.GET("/api/v1/pulls/gitlab/acme/widget/7")
	require.Equal(http.StatusOK, resp.Code)

	var result struct {
		Events []struct {
			DiscussionID *string `json:"discussion_id"`
			PositionJSON string  `json:"position_json"`
			Resolvable   bool    `json:"resolvable"`
			Resolved     bool    `json:"resolved"`
		} `json:"events"`
	}
	env.DecodeJSON(resp, &result)

	require.Len(result.Events, 1)
	assert.NotNil(result.Events[0].DiscussionID)
	assert.Equal("disc-abc123", *result.Events[0].DiscussionID)
	assert.Equal(`{"new_path":"main.go","new_line":42}`, result.Events[0].PositionJSON)
	assert.True(result.Events[0].Resolvable)
	assert.False(result.Events[0].Resolved)
}
```

- [ ] **Step 3: Run test to verify it passes**

Run: `go test ./internal/server/e2etest -run TestGetPRDetailIncludesDiscussionID -shuffle=on`
Expected: PASS (the DB changes already support this)

- [ ] **Step 4: Commit**

```bash
git add internal/server/huma_routes.go internal/server/e2etest/gitlab_discussions_test.go
git commit -m "feat(api): add discussion endpoint types and e2e test for discussion_id in response"
```

---

## Task 14: Register Discussion Endpoints

**Files:**
- Modify: `internal/server/huma_routes.go`

- [ ] **Step 1: Add reply to discussion handler**

Add the handler function:

```go
func (s *Server) handleReplyToDiscussion(ctx context.Context, input *replyToDiscussionInput) (*replyToDiscussionOutput, error) {
	repo, err := s.resolveProviderRouteRepo(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}

	provider, err := s.registry.Provider(repo.Platform, repo.PlatformHost)
	if err != nil {
		return nil, problemInternal("provider lookup failed")
	}

	replier, ok := provider.(platform.DiscussionReplier)
	if !ok || !provider.Capabilities().DiscussionReply {
		return nil, problemBadRequest(CodeUnsupportedCapability, "provider does not support discussion replies", nil)
	}

	ref := platform.RepoRef{
		Platform:   repo.Platform,
		Host:       repo.PlatformHost,
		Owner:      repo.Owner,
		Name:       repo.Name,
		RepoPath:   repo.RepoPath,
		PlatformID: repo.PlatformRepoID,
	}

	event, err := replier.ReplyToDiscussion(ctx, ref, input.Number, input.DiscussionID, input.Body.Body)
	if err != nil {
		return nil, problemFromPlatformError(err, "reply to discussion failed")
	}

	mr, err := s.database.GetMergeRequestByRepoAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, problemInternal("lookup merge request failed")
	}

	dbEvent := platform.DBMREvent(mr.ID, event)
	if err := s.database.UpsertMREvents(ctx, []db.MREvent{dbEvent}); err != nil {
		return nil, problemInternal("persist event failed")
	}

	events, err := s.database.ListMREvents(ctx, mr.ID)
	if err != nil {
		return nil, problemInternal("reload events failed")
	}

	for _, e := range events {
		if e.DedupeKey == dbEvent.DedupeKey {
			return &replyToDiscussionOutput{Body: e}, nil
		}
	}

	return &replyToDiscussionOutput{Body: dbEvent}, nil
}
```

- [ ] **Step 2: Add resolve discussion handler**

Add the handler function:

```go
func (s *Server) handleResolveDiscussion(ctx context.Context, input *resolveDiscussionInput) (*resolveDiscussionOutput, error) {
	repo, err := s.resolveProviderRouteRepo(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}

	provider, err := s.registry.Provider(repo.Platform, repo.PlatformHost)
	if err != nil {
		return nil, problemInternal("provider lookup failed")
	}

	resolver, ok := provider.(platform.DiscussionResolver)
	if !ok || !provider.Capabilities().DiscussionResolve {
		return nil, problemBadRequest(CodeUnsupportedCapability, "provider does not support discussion resolution", nil)
	}

	ref := platform.RepoRef{
		Platform:   repo.Platform,
		Host:       repo.PlatformHost,
		Owner:      repo.Owner,
		Name:       repo.Name,
		RepoPath:   repo.RepoPath,
		PlatformID: repo.PlatformRepoID,
	}

	if err := resolver.ResolveDiscussion(ctx, ref, input.Number, input.DiscussionID, input.Body.Resolved); err != nil {
		return nil, problemFromPlatformError(err, "resolve discussion failed")
	}

	return &resolveDiscussionOutput{}, nil
}
```

- [ ] **Step 3: Register the routes**

Find the route registration section and add:

```go
huma.Post(api, "/api/v1/pulls/{provider}/{owner}/{name}/{number}/discussions/{discussion_id}/reply", s.handleReplyToDiscussion)
huma.Post(api, "/api/v1/pulls/{provider}/{owner}/{name}/{number}/discussions/{discussion_id}/resolve", s.handleResolveDiscussion)
```

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: Build succeeds

- [ ] **Step 5: Commit**

```bash
git add internal/server/huma_routes.go
git commit -m "feat(api): register discussion reply and resolve endpoints"
```

---

## Task 15: Capability Mapping Update

**Files:**
- Modify: `internal/server/huma_routes.go` (or wherever capabilities are mapped)

- [ ] **Step 1: Find and update capability mapping function**

Search for where `providerCapabilitiesResponse` is populated from `platform.Capabilities` and add the new fields:

```go
func capabilitiesResponse(caps platform.Capabilities) providerCapabilitiesResponse {
	return providerCapabilitiesResponse{
		ReadRepositories:  caps.ReadRepositories,
		ReadMergeRequests: caps.ReadMergeRequests,
		ReadIssues:        caps.ReadIssues,
		ReadComments:      caps.ReadComments,
		ReadReleases:      caps.ReadReleases,
		ReadCI:            caps.ReadCI,
		ReadLabels:        caps.ReadLabels,
		CommentMutation:   caps.CommentMutation,
		StateMutation:     caps.StateMutation,
		MergeMutation:     caps.MergeMutation,
		ReviewMutation:    caps.ReviewMutation,
		WorkflowApproval:  caps.WorkflowApproval,
		ReadyForReview:    caps.ReadyForReview,
		IssueMutation:     caps.IssueMutation,
		LabelMutation:     caps.LabelMutation,
		DiscussionReply:   caps.DiscussionReply,
		DiscussionResolve: caps.DiscussionResolve,
	}
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: Build succeeds

- [ ] **Step 3: Commit**

```bash
git add internal/server/huma_routes.go
git commit -m "feat(api): map discussion capabilities in response"
```

---

## Task 16: E2E Test for Discussion Capabilities

**Files:**
- Modify: `internal/server/e2etest/gitlab_discussions_test.go`

- [ ] **Step 1: Add test for GitLab capabilities including discussion support**

Add to `gitlab_discussions_test.go`:

```go
func TestGitLabRepoCapabilitiesIncludeDiscussions(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	env := newTestEnv(t)
	database := env.Database

	_, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)

	resp := env.GET("/api/v1/repos/gitlab/acme/widget")
	require.Equal(http.StatusOK, resp.Code)

	var result struct {
		Capabilities struct {
			DiscussionReply   bool `json:"discussion_reply"`
			DiscussionResolve bool `json:"discussion_resolve"`
		} `json:"capabilities"`
	}
	env.DecodeJSON(resp, &result)

	assert.True(result.Capabilities.DiscussionReply)
	assert.True(result.Capabilities.DiscussionResolve)
}
```

- [ ] **Step 2: Run test**

Run: `go test ./internal/server/e2etest -run TestGitLabRepoCapabilitiesIncludeDiscussions -shuffle=on`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/server/e2etest/gitlab_discussions_test.go
git commit -m "test(e2e): verify GitLab discussion capabilities in API response"
```

---

## Task 17: Run Full Test Suite

**Files:** None (verification only)

- [ ] **Step 1: Run all Go tests**

Run: `make test`
Expected: PASS

- [ ] **Step 2: Run linter**

Run: `make lint`
Expected: PASS

- [ ] **Step 3: Regenerate API artifacts**

Run: `make api-generate`
Expected: Completes without error

- [ ] **Step 4: Commit any generated changes**

```bash
git add -A
git diff --cached --quiet || git commit -m "chore: regenerate API artifacts"
```

---

## Task 18: Final Verification

**Files:** None (verification only)

- [ ] **Step 1: Build the binary**

Run: `make build`
Expected: Build succeeds

- [ ] **Step 2: Run short tests**

Run: `make test-short`
Expected: PASS

- [ ] **Step 3: Verify migration works on fresh DB**

Run: `rm -f /tmp/middleman-test.db && go test ./internal/db -run TestMigrations -shuffle=on`
Expected: PASS

- [ ] **Step 4: Final commit if needed**

```bash
git status
# If any uncommitted changes, commit them
```

---

## Summary

This plan implements GitLab discussion_id support in 18 tasks:

1. **Tasks 1-2:** Database migration and type extensions
2. **Tasks 3-5:** Platform types, interfaces, and persistence
3. **Tasks 6-7:** DB query updates for new columns
4. **Tasks 8-10:** GitLab normalization and API switch
5. **Tasks 11-12:** GitLab mutations and API types
6. **Tasks 13-16:** API endpoints and capability mapping
7. **Tasks 17-18:** Full test suite and final verification

Each task produces a working, testable increment with a commit.
