# GitHub Inline Review Comments

## Overview

Add support for fetching GitHub pull request review threads (inline code comments) so they appear in the PR activity timeline, threaded by diff position.

## Problem

GitHub has three types of PR comments:
1. **Issue Comments** - top-level comments on the PR (already fetched)
2. **Reviews** - approve/request-changes submissions (already fetched)
3. **Review Comments** - inline code comments attached to specific diff lines (NOT fetched)

The `gqlPR` struct fetches `Reviews` but not `reviewThreads`. Users see review submissions but not the inline comments that accompany them.

## Design

### 1. GraphQL Query Extension

Add `reviewThreads` connection to `gqlPR` struct in `internal/github/graphql.go`:

```go
type gqlPR struct {
    // ... existing fields ...
    ReviewThreads struct {
        Nodes    []gqlReviewThread
        PageInfo pageInfo
    } `graphql:"reviewThreads(first: 100)"`
}

type gqlReviewThread struct {
    ID         string
    IsResolved bool
    Path       string
    Line       *int
    StartLine  *int
    Comments   struct {
        Nodes    []gqlReviewComment
        PageInfo pageInfo
    } `graphql:"comments(first: 100)"`
}

type gqlReviewComment struct {
    DatabaseId  int64
    Author      struct{ Login string }
    Body        string
    CreatedAt   time.Time
    DiffHunk    string
}
```

### 2. Normalization

Add normalization in `internal/platform/github/normalize.go`:

```go
func NormalizeReviewThreads(
    ref platform.RepoRef,
    number int,
    threads []gqlReviewThread,
) []platform.MergeRequestEvent
```

For each comment in each thread:
- `EventType`: `"review_comment"`
- `ThreadID`: review thread's GraphQL ID (e.g., `"PRRT_kwDOABC123..."`)
- `PositionJSON`: JSON with `path`, `line`, `startLine` (stored but not rendered)
- `Resolvable`: `true`
- `Resolved`: thread's `isResolved` value
- `DedupeKey`: `github/{host}/{repo}/mr/{number}/review_comment/{comment_db_id}`

### 3. Capabilities

Update GitHub provider in `internal/platform/github/client.go`:

```go
func (c *Client) Capabilities() platform.Capabilities {
    return platform.Capabilities{
        // ... existing ...
        ThreadResolve: true,  // Add this
    }
}
```

### 4. Resolve Mutation

Implement `platform.ThreadResolver` interface for GitHub:

```go
func (c *Client) ResolveThread(
    ctx context.Context,
    ref platform.RepoRef,
    number int,
    threadID string,
    resolved bool,
) error
```

Uses GitHub GraphQL mutations:
- `resolveReviewThread(input: {threadId: $threadID})`
- `unresolveReviewThread(input: {threadId: $threadID})`

### 5. Data Flow

```
GitHub GraphQL API
        │
        ▼
gqlPR.ReviewThreads
        │
        ▼
NormalizeReviewThreads()
        │
        ▼
[]platform.MergeRequestEvent
    - EventType: "review_comment"
    - ThreadID: thread GraphQL ID
    - Resolvable: true
    - Resolved: isResolved
    - PositionJSON: {path, line, startLine}
        │
        ▼
Database (middleman_mr_events)
        │
        ▼
API Response (PREvent)
        │
        ▼
EventTimeline.svelte
    - Threads by ThreadID
    - Shows resolve/unresolve controls
```

## UI Behavior

- Review comments appear in EventTimeline threaded by `ThreadID`
- Matches current GitLab discussion rendering
- No file path/line context displayed (consistent with GitLab)
- Resolve/unresolve via existing thread controls when `ThreadResolve` capability is true

## Not Included

- File path/line number display in UI
- Code snippet/diff hunk rendering
- Reply to review threads (separate feature, would need `ThreadReply` capability)

## Testing

1. Unit tests for `NormalizeReviewThreads`
2. E2E test for fetching review threads via API
3. E2E test for resolve/unresolve mutation
4. Manual verification on real GitHub PR with inline comments
