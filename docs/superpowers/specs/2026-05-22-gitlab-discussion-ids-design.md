# GitLab Discussion ID Support Design

**Date:** 2026-05-22
**Goal:** Add GitLab discussion_id support to enable automated threaded replies and discussion resolution.

## Context

GitLab groups related comments into "discussions" — threaded conversations that can be on the MR generally or positioned on specific diff lines. The current middleman sync uses the Notes API which loses this grouping. To enable automation that replies in the same thread or resolves code review discussions, we need to capture and expose discussion IDs.

## Requirements

1. Sync and store `discussion_id` for GitLab MR and issue comments
2. Store position metadata (file, line numbers) for inline diff comments (read-only)
3. Track `resolvable` and `resolved` status for MR discussions
4. Enable replying to existing discussion threads via API
5. Enable resolving/unresolving discussions via API
6. Design schema for future provider extensibility (other providers can add discussion support later)

## Database Schema

### Migration: `000025_gitlab_discussion_ids.up.sql`

```sql
ALTER TABLE middleman_mr_events ADD COLUMN discussion_id TEXT;
ALTER TABLE middleman_mr_events ADD COLUMN position_json TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_mr_events ADD COLUMN resolvable INTEGER NOT NULL DEFAULT 0;
ALTER TABLE middleman_mr_events ADD COLUMN resolved INTEGER NOT NULL DEFAULT 0;

ALTER TABLE middleman_issue_events ADD COLUMN discussion_id TEXT;

CREATE INDEX idx_mr_events_discussion
    ON middleman_mr_events(discussion_id) WHERE discussion_id IS NOT NULL;
```

### Migration: `000025_gitlab_discussion_ids.down.sql`

```sql
DROP INDEX IF EXISTS idx_mr_events_discussion;

ALTER TABLE middleman_mr_events DROP COLUMN discussion_id;
ALTER TABLE middleman_mr_events DROP COLUMN position_json;
ALTER TABLE middleman_mr_events DROP COLUMN resolvable;
ALTER TABLE middleman_mr_events DROP COLUMN resolved;

ALTER TABLE middleman_issue_events DROP COLUMN discussion_id;
```

### Column Descriptions

| Column | Type | Description |
|--------|------|-------------|
| `discussion_id` | TEXT (nullable) | GitLab discussion ID; NULL for non-GitLab or pre-migration events |
| `position_json` | TEXT | JSON blob of GitLab position metadata for inline comments; empty string if not positioned |
| `resolvable` | INTEGER | 1 if discussion can be resolved, 0 otherwise |
| `resolved` | INTEGER | 1 if discussion is currently resolved, 0 otherwise |

## Platform Types

### `internal/platform/types.go`

Extend `MergeRequestEvent`:
```go
type MergeRequestEvent struct {
    // ... existing fields ...
    DiscussionID string
    PositionJSON string
    Resolvable   bool
    Resolved     bool
}
```

Extend `IssueEvent`:
```go
type IssueEvent struct {
    // ... existing fields ...
    DiscussionID string
}
```

Extend `Capabilities`:
```go
type Capabilities struct {
    // ... existing fields ...
    DiscussionReply   bool
    DiscussionResolve bool
}
```

## GitLab Sync Changes

### Switch from Notes to Discussions API

**File:** `internal/platform/gitlab/client.go`

Replace:
```go
func (c *Client) listMergeRequestNotes(ctx context.Context, pid any, number int) ([]*gitlab.Note, error)
```

With:
```go
func (c *Client) listMergeRequestDiscussions(ctx context.Context, pid any, number int) ([]*gitlab.Discussion, error)
```

This calls `api.Discussions.ListMergeRequestDiscussions` which returns discussions containing their notes.

Update `ListMergeRequestEvents` to use the new function and pass discussions to normalization.

Same pattern for `listIssueDiscussions` replacing `listIssueNotes`.

### Normalization

**File:** `internal/platform/gitlab/normalize.go`

New function:
```go
func NormalizeMergeRequestDiscussions(
    repo platform.RepoRef,
    mrNumber int,
    discussions []*gitlab.Discussion,
) []platform.MergeRequestEvent
```

For each discussion:
1. Iterate notes in the discussion
2. Skip system notes (`note.System == true`)
3. Extract `discussion.ID` as `DiscussionID`
4. Extract `note.Resolvable`, `note.Resolved`
5. Serialize `note.Position` to JSON if present
6. Build dedupe key incorporating discussion context

Similar function for issues (without position/resolvable).

### Position JSON Structure

The `position_json` field stores GitLab's native position format:
```json
{
  "old_path": "internal/server/api.go",
  "new_path": "internal/server/api.go",
  "old_line": null,
  "new_line": 42,
  "position_type": "text",
  "head_sha": "abc123...",
  "base_sha": "def456...",
  "start_sha": "ghi789..."
}
```

This is stored as-is for read-only display. No parsing or validation required.

## Persistence Layer

### DB Types

**File:** `internal/db/types.go`

```go
type MREvent struct {
    // ... existing fields ...
    DiscussionID *string
    PositionJSON string
    Resolvable   bool
    Resolved     bool
}

type IssueEvent struct {
    // ... existing fields ...
    DiscussionID *string
}
```

### Conversion

**File:** `internal/platform/persist.go`

Update `DBMREvent`:
```go
func DBMREvent(mrID int64, event MergeRequestEvent) db.MREvent {
    out := db.MREvent{
        // ... existing fields ...
        PositionJSON: event.PositionJSON,
        Resolvable:   event.Resolvable,
        Resolved:     event.Resolved,
    }
    if event.DiscussionID != "" {
        out.DiscussionID = &event.DiscussionID
    }
    // ...
    return out
}
```

### Queries

**File:** `internal/db/queries.go`

Update `UpsertMREvents` to include new columns in INSERT and ON CONFLICT UPDATE.

Update `ListMREvents` SELECT to retrieve new columns.

New query:
```go
func (d *DB) ListMREventsByDiscussion(ctx context.Context, mrID int64, discussionID string) ([]MREvent, error)
```

## New API Endpoints

### Reply to Discussion

```
POST /api/v1/pulls/{provider}/{owner}/{name}/{number}/discussions/{discussion_id}/reply
```

**Request:**
```json
{
  "body": "Thanks, fixed in the latest commit."
}
```

**Response:** `201 Created`
```json
{
  "id": 456,
  "event_type": "issue_comment",
  "author": "bot",
  "body": "Thanks, fixed in the latest commit.",
  "discussion_id": "abc123def",
  "created_at": "2026-05-22T15:30:00Z"
}
```

### Resolve Discussion

```
POST /api/v1/pulls/{provider}/{owner}/{name}/{number}/discussions/{discussion_id}/resolve
```

**Request:**
```json
{
  "resolved": true
}
```

**Response:** `200 OK`

## Platform Interfaces

**File:** `internal/platform/client.go`

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

## GitLab Mutation Implementation

**File:** `internal/platform/gitlab/mutation.go` (new file)

```go
func (c *Client) ReplyToDiscussion(
    ctx context.Context,
    ref platform.RepoRef,
    number int,
    discussionID string,
    body string,
) (platform.MergeRequestEvent, error) {
    // Calls api.Discussions.AddMergeRequestDiscussionNote
}

func (c *Client) ResolveDiscussion(
    ctx context.Context,
    ref platform.RepoRef,
    number int,
    discussionID string,
    resolved bool,
) error {
    // Calls api.Discussions.ResolveMergeRequestDiscussion
}
```

**Capabilities:**

Update `Client.Capabilities()`:
```go
func (c *Client) Capabilities() platform.Capabilities {
    return platform.Capabilities{
        // ... existing ...
        DiscussionReply:   true,
        DiscussionResolve: true,
    }
}
```

## API Response Changes

The `db.MREvent` struct embeds directly into API responses. New fields automatically appear:

```json
{
  "events": [
    {
      "id": 123,
      "event_type": "issue_comment",
      "author": "reviewer",
      "body": "This needs a nil check",
      "created_at": "2026-05-22T10:00:00Z",
      "discussion_id": "abc123def",
      "position_json": "{\"new_path\":\"main.go\",\"new_line\":42}",
      "resolvable": true,
      "resolved": false
    }
  ]
}
```

## Testing

### Unit Tests

- `internal/platform/gitlab/normalize_test.go`: Test `NormalizeMergeRequestDiscussions` with positioned comments, system note filtering, resolved threads
- `internal/platform/gitlab/client_test.go`: Test discussion fetch pagination

### Wire-Level Integration Tests

- `internal/server/e2etest/gitlab_discussions_test.go`:
  - Seed DB with events containing discussion_ids
  - GET PR detail verifies discussion_id in response
  - POST reply returns 201 with new event
  - POST resolve returns 200
  - Non-GitLab provider returns capability error

### Database Tests

- `internal/db/queries_test.go`: Upsert/query with new columns, `ListMREventsByDiscussion`

### Migration Test

- Verify migration on existing DB with NULL discussion_id values
- Verify existing events remain queryable

## Provider Extensibility

The schema supports future providers:
- `discussion_id` column is nullable and provider-agnostic
- Capability flags (`DiscussionReply`, `DiscussionResolve`) let providers opt-in
- Other providers (GitHub, Forgejo, Gitea) can populate discussion_id if their APIs support threading

GitHub has review comment threads with `in_reply_to_id`; a future enhancement could map this to `discussion_id` using a synthetic ID scheme.

## Out of Scope

- Creating new positioned discussions (only replying to existing)
- UI changes to display threaded discussions
- GitHub/Forgejo/Gitea discussion support (future work)
