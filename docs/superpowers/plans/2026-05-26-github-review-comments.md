# GitHub Inline Review Comments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fetch GitHub pull request review threads (inline code comments) via GraphQL and display them in the PR activity timeline, threaded by diff position with resolve/unresolve support.

**Architecture:** Extend the existing `gqlPR` struct to include `reviewThreads` connection, add normalization to convert threads to `review_comment` events with `ThreadID` for grouping, and implement `ThreadResolver` interface for GitHub to enable resolve mutations via GraphQL.

**Tech Stack:** Go, GitHub GraphQL API (githubv4), SQLite

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/github/graphql.go` | GraphQL types for review threads and comments |
| `internal/github/graphql_review_threads.go` | Review thread type definitions (new file) |
| `internal/platform/github/normalize.go` | Normalization of review threads to MergeRequestEvent |
| `internal/github/sync.go` | Add ThreadResolve capability, implement ThreadResolver |
| `internal/github/graphql_test.go` | Unit tests for GraphQL type conversion |
| `internal/platform/github/normalize_test.go` | Unit tests for review thread normalization |
| `internal/server/e2etest/github_review_threads_test.go` | E2E tests for review thread fetch and resolve |

---

### Task 1: Add GraphQL Types for Review Threads

**Files:**
- Create: `internal/github/graphql_review_threads.go`
- Modify: `internal/github/graphql.go:37-93` (gqlPR struct)

- [ ] **Step 1: Create new file with review thread types**

Create `internal/github/graphql_review_threads.go`:

```go
package github

import "time"

// gqlReviewThread represents a GitHub pull request review thread from GraphQL.
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

// gqlReviewComment represents a comment within a review thread.
type gqlReviewComment struct {
	DatabaseId int64
	Author     struct{ Login string }
	Body       string
	CreatedAt  time.Time
	DiffHunk   string
}

// ReviewThread is the exported type for review thread data.
type ReviewThread struct {
	ID         string
	IsResolved bool
	Path       string
	Line       *int
	StartLine  *int
	Comments   []ReviewComment
}

// ReviewComment is the exported type for review comment data.
type ReviewComment struct {
	DatabaseID int64
	Author     string
	Body       string
	CreatedAt  time.Time
	DiffHunk   string
}

// adaptReviewThread converts a GraphQL review thread to the exported type.
func adaptReviewThread(gql *gqlReviewThread) ReviewThread {
	thread := ReviewThread{
		ID:         gql.ID,
		IsResolved: gql.IsResolved,
		Path:       gql.Path,
		Line:       gql.Line,
		StartLine:  gql.StartLine,
		Comments:   make([]ReviewComment, 0, len(gql.Comments.Nodes)),
	}
	for i := range gql.Comments.Nodes {
		c := &gql.Comments.Nodes[i]
		thread.Comments = append(thread.Comments, ReviewComment{
			DatabaseID: c.DatabaseId,
			Author:     c.Author.Login,
			Body:       c.Body,
			CreatedAt:  c.CreatedAt,
			DiffHunk:   c.DiffHunk,
		})
	}
	return thread
}
```

- [ ] **Step 2: Run gofmt to verify syntax**

Run: `gofmt -d internal/github/graphql_review_threads.go`
Expected: No output (file is properly formatted)

- [ ] **Step 3: Add reviewThreads to gqlPR struct**

Edit `internal/github/graphql.go` to add the `ReviewThreads` field to `gqlPR` struct (after line 92, before the closing brace):

```go
type gqlPR struct {
	DatabaseId     int64 `graphql:"databaseId"`
	Number         int
	Title          string
	State          string
	IsDraft        bool
	Locked         bool
	Body           string
	URL            string
	Author         struct{ Login string }
	CreatedAt      time.Time
	UpdatedAt      time.Time
	MergedAt       *time.Time
	ClosedAt       *time.Time
	Additions      int
	Deletions      int
	Mergeable      string
	ReviewDecision string
	HeadRefName    string
	BaseRefName    string
	HeadRefOid     string `graphql:"headRefOid"`
	BaseRefOid     string `graphql:"baseRefOid"`
	HeadRepository *struct {
		URL string
	}
	Labels struct {
		Nodes []gqlLabel
	} `graphql:"labels(first: 100)"`
	Comments struct {
		Nodes    []gqlComment
		PageInfo pageInfo
	} `graphql:"comments(first: 100)"`
	Reviews struct {
		Nodes    []gqlReview
		PageInfo pageInfo
	} `graphql:"reviews(first: 100)"`
	ReviewThreads struct {
		Nodes    []gqlReviewThread
		PageInfo pageInfo
	} `graphql:"reviewThreads(first: 100)"`
	AllCommits struct {
		Nodes    []gqlCommitNode
		PageInfo pageInfo
	} `graphql:"allCommits: commits(first: 100)"`
	LastCommit struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes    []gqlCheckContext
						PageInfo pageInfo
					} `graphql:"contexts(first: 100)"`
				}
			}
		}
	} `graphql:"lastCommit: commits(last: 1)"`
	TimelineItems struct {
		Nodes    []gqlPullRequestTimelineItem
		PageInfo pageInfo
	} `graphql:"timelineItems(itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT, COMMENT_DELETED_EVENT, CROSS_REFERENCED_EVENT, RENAMED_TITLE_EVENT, BASE_REF_CHANGED_EVENT], first: 100)"`
}
```

- [ ] **Step 4: Run tests to verify GraphQL struct compiles**

Run: `go build ./internal/github/...`
Expected: Build succeeds

- [ ] **Step 5: Commit**

```bash
git add internal/github/graphql_review_threads.go internal/github/graphql.go
git commit -m "feat(github): add GraphQL types for review threads

Add gqlReviewThread and gqlReviewComment types for fetching inline
code comments from GitHub's reviewThreads connection."
```

---

### Task 2: Add ReviewThreads to BulkPR and convertGQLPR

**Files:**
- Modify: `internal/github/graphql.go:500-517` (BulkPR struct)
- Modify: `internal/github/graphql.go:774-811` (convertGQLPR function)

- [ ] **Step 1: Add ReviewThreads field to BulkPR**

Edit `internal/github/graphql.go` to add `ReviewThreads` and `ReviewThreadsComplete` fields to `BulkPR` struct:

```go
// BulkPR holds a PR and its nested data from a single GraphQL query.
// The *Complete flags indicate whether each nested connection was
// fully paginated. When false, the data is partial and the detail
// drain should fill in via REST.
type BulkPR struct {
	PR                     *gh.PullRequest
	Comments               []*gh.IssueComment
	Reviews                []*gh.PullRequestReview
	ReviewThreads          []ReviewThread
	Commits                []*gh.RepositoryCommit
	TimelineEvents         []PullRequestTimelineEvent
	CheckRuns              []*gh.CheckRun
	Statuses               []*gh.RepoStatus
	CommentsComplete       bool
	ReviewsComplete        bool
	ReviewThreadsComplete  bool
	CommitsComplete        bool
	TimelineComplete       bool
	CIComplete             bool
}
```

- [ ] **Step 2: Update convertGQLPR to populate ReviewThreads**

Edit `internal/github/graphql.go` in the `convertGQLPR` function to add review thread conversion after the reviews loop:

```go
func convertGQLPR(gql *gqlPR) BulkPR {
	bulk := BulkPR{
		PR:                    adaptPR(gql),
		CommentsComplete:      !gql.Comments.PageInfo.HasNextPage,
		ReviewsComplete:       !gql.Reviews.PageInfo.HasNextPage,
		ReviewThreadsComplete: !gql.ReviewThreads.PageInfo.HasNextPage,
		CommitsComplete:       !gql.AllCommits.PageInfo.HasNextPage,
		TimelineComplete:      !gql.TimelineItems.PageInfo.HasNextPage,
	}

	for i := range gql.Comments.Nodes {
		bulk.Comments = append(bulk.Comments, adaptComment(&gql.Comments.Nodes[i]))
	}
	for i := range gql.Reviews.Nodes {
		bulk.Reviews = append(bulk.Reviews, adaptReview(&gql.Reviews.Nodes[i]))
	}
	for i := range gql.ReviewThreads.Nodes {
		bulk.ReviewThreads = append(bulk.ReviewThreads, adaptReviewThread(&gql.ReviewThreads.Nodes[i]))
	}
	for i := range gql.AllCommits.Nodes {
		bulk.Commits = append(bulk.Commits, adaptCommit(&gql.AllCommits.Nodes[i]))
	}
	for i := range gql.TimelineItems.Nodes {
		event, ok := adaptPullRequestTimelineEvent(&gql.TimelineItems.Nodes[i])
		if ok {
			bulk.TimelineEvents = append(bulk.TimelineEvents, event)
		}
	}

	bulk.CIComplete = true
	if len(gql.LastCommit.Nodes) > 0 {
		rollup := gql.LastCommit.Nodes[0].Commit.StatusCheckRollup
		if rollup != nil {
			bulk.CIComplete = !rollup.Contexts.PageInfo.HasNextPage
			bulk.CheckRuns, bulk.Statuses = splitCheckContexts(
				rollup.Contexts.Nodes,
			)
		}
	}

	return bulk
}
```

- [ ] **Step 3: Run build to verify changes**

Run: `go build ./internal/github/...`
Expected: Build succeeds

- [ ] **Step 4: Commit**

```bash
git add internal/github/graphql.go
git commit -m "feat(github): populate review threads in BulkPR

Convert reviewThreads from GraphQL response into BulkPR.ReviewThreads
for downstream normalization."
```

---

### Task 3: Add NormalizeReviewThreads Function

**Files:**
- Modify: `internal/platform/github/normalize.go`

- [ ] **Step 1: Add position JSON struct and serialization**

Add to `internal/platform/github/normalize.go` after the existing metadata structs (around line 361):

```go
type reviewCommentPositionMetadata struct {
	Path      string `json:"path"`
	Line      *int   `json:"line,omitempty"`
	StartLine *int   `json:"start_line,omitempty"`
}

func serializeReviewCommentPosition(path string, line, startLine *int) string {
	if path == "" {
		return ""
	}
	pos := reviewCommentPositionMetadata{
		Path:      path,
		Line:      line,
		StartLine: startLine,
	}
	data, err := json.Marshal(pos)
	if err != nil {
		return ""
	}
	return string(data)
}
```

- [ ] **Step 2: Add NormalizeReviewThreads function**

Add to `internal/platform/github/normalize.go` after NormalizeTimelineEvent function (around line 278):

```go
// NormalizeReviewThreads converts GitHub review threads to MergeRequestEvents.
// Each comment in a thread becomes a separate event, all sharing the same ThreadID.
func NormalizeReviewThreads(
	repo platform.RepoRef,
	mrNumber int,
	threads []ghsync.ReviewThread,
) []platform.MergeRequestEvent {
	var events []platform.MergeRequestEvent
	for _, thread := range threads {
		for _, comment := range thread.Comments {
			events = append(events, platform.MergeRequestEvent{
				Repo:               repo,
				PlatformID:         comment.DatabaseID,
				PlatformExternalID: fmt.Sprintf("%d", comment.DatabaseID),
				MergeRequestNumber: mrNumber,
				EventType:          "review_comment",
				Author:             comment.Author,
				Body:               comment.Body,
				CreatedAt:          comment.CreatedAt,
				DedupeKey:          fmt.Sprintf("review-comment-%d", comment.DatabaseID),
				ThreadID:           thread.ID,
				PositionJSON:       serializeReviewCommentPosition(thread.Path, thread.Line, thread.StartLine),
				Resolvable:         true,
				Resolved:           thread.IsResolved,
			})
		}
	}
	return events
}
```

- [ ] **Step 3: Add import for internal/github package**

The `ReviewThread` type is defined in `internal/github/graphql_review_threads.go`. Add an import alias to `internal/platform/github/normalize.go`:

```go
import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	gh "github.com/google/go-github/v84/github"
	ghsync "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
)
```

Then update the function signature to use the aliased type:

```go
func NormalizeReviewThreads(
	repo platform.RepoRef,
	mrNumber int,
	threads []ghsync.ReviewThread,
) []platform.MergeRequestEvent {
```

- [ ] **Step 4: Run build to verify**

Run: `go build ./internal/platform/github/...`
Expected: Build succeeds

- [ ] **Step 5: Commit**

```bash
git add internal/platform/github/normalize.go
git commit -m "feat(github): add NormalizeReviewThreads function

Convert GitHub review threads to review_comment MergeRequestEvents
with ThreadID for grouping and position metadata."
```

---

### Task 4: Add Unit Tests for NormalizeReviewThreads

**Files:**
- Modify: `internal/platform/github/normalize_test.go`

- [ ] **Step 1: Add test for NormalizeReviewThreads**

Add import to `internal/platform/github/normalize_test.go`:

```go
import (
	// ... existing imports ...
	ghsync "go.kenn.io/middleman/internal/github"
)
```

Add the test function:

```go
func TestNormalizeReviewThreads(t *testing.T) {
	assert := assert.New(t)

	repo := platform.RepoRef{
		Platform: platform.KindGitHub,
		Host:     "github.com",
		Owner:    "owner",
		Name:     "repo",
		RepoPath: "owner/repo",
	}

	threads := []ghsync.ReviewThread{
		{
			ID:         "PRRT_thread1",
			IsResolved: false,
			Path:       "src/main.go",
			Line:       intPtr(42),
			StartLine:  nil,
			Comments: []ghsync.ReviewComment{
				{
					DatabaseID: 1001,
					Author:     "reviewer",
					Body:       "This needs a fix",
					CreatedAt:  time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
					DiffHunk:   "@@ -40,6 +40,8 @@",
				},
				{
					DatabaseID: 1002,
					Author:     "author",
					Body:       "Fixed!",
					CreatedAt:  time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
					DiffHunk:   "@@ -40,6 +40,8 @@",
				},
			},
		},
		{
			ID:         "PRRT_thread2",
			IsResolved: true,
			Path:       "src/util.go",
			Line:       intPtr(10),
			StartLine:  intPtr(8),
			Comments: []ghsync.ReviewComment{
				{
					DatabaseID: 1003,
					Author:     "reviewer",
					Body:       "Consider refactoring",
					CreatedAt:  time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
					DiffHunk:   "@@ -5,10 +5,12 @@",
				},
			},
		},
	}

	events := NormalizeReviewThreads(repo, 123, threads)

	assert.Len(events, 3)

	// First comment from first thread
	assert.Equal("review_comment", events[0].EventType)
	assert.Equal(int64(1001), events[0].PlatformID)
	assert.Equal("reviewer", events[0].Author)
	assert.Equal("This needs a fix", events[0].Body)
	assert.Equal("PRRT_thread1", events[0].ThreadID)
	assert.True(events[0].Resolvable)
	assert.False(events[0].Resolved)
	assert.Contains(events[0].PositionJSON, "src/main.go")
	assert.Contains(events[0].PositionJSON, "42")
	assert.Equal("review-comment-1001", events[0].DedupeKey)

	// Second comment from first thread (same ThreadID)
	assert.Equal("PRRT_thread1", events[1].ThreadID)
	assert.Equal(int64(1002), events[1].PlatformID)
	assert.Equal("author", events[1].Author)
	assert.False(events[1].Resolved)

	// Comment from second thread (resolved)
	assert.Equal("PRRT_thread2", events[2].ThreadID)
	assert.Equal(int64(1003), events[2].PlatformID)
	assert.True(events[2].Resolved)
	assert.Contains(events[2].PositionJSON, "src/util.go")
	assert.Contains(events[2].PositionJSON, "start_line")
}

func intPtr(i int) *int {
	return &i
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/platform/github/... -run TestNormalizeReviewThreads -v -shuffle=on`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/platform/github/normalize_test.go
git commit -m "test(github): add unit tests for NormalizeReviewThreads"
```

---

### Task 5: Add Sync-Layer Wrapper and Integrate into Sync Flow

**Files:**
- Modify: `internal/github/normalize.go`
- Modify: `internal/github/sync.go:4057-4059`

- [ ] **Step 1: Add NormalizeReviewThreadEvents wrapper in internal/github/normalize.go**

Add after `NormalizeReviewEvent` function (around line 119):

```go
// NormalizeReviewThreadEvents converts GitHub review threads to db.MREvents.
func NormalizeReviewThreadEvents(mrID int64, threads []ReviewThread) []db.MREvent {
	events := platformgithub.NormalizeReviewThreads(platform.RepoRef{}, 0, threads)
	result := make([]db.MREvent, 0, len(events))
	for _, e := range events {
		result = append(result, dbMREvent(mrID, e))
	}
	return result
}
```

- [ ] **Step 2: Add review thread processing to sync flow**

Edit `internal/github/sync.go` around line 4059. After the `for _, r := range bulk.Reviews` loop, add:

```go
	for _, r := range bulk.Reviews {
		events = append(events, NormalizeReviewEvent(mrID, r))
	}
	events = append(events, NormalizeReviewThreadEvents(mrID, bulk.ReviewThreads)...)
```

- [ ] **Step 3: Run build to verify integration**

Run: `go build ./internal/github/...`
Expected: Build succeeds

- [ ] **Step 4: Run existing sync tests**

Run: `go test ./internal/github/... -run Sync -shuffle=on`
Expected: All tests pass

- [ ] **Step 5: Commit**

```bash
git add internal/github/normalize.go internal/github/sync.go
git commit -m "feat(github): integrate review threads into PR sync flow

Add sync-layer wrapper and normalize review threads alongside
reviews and comments during PR detail synchronization."
```

---

### Task 6: Add ThreadResolve Capability for GitHub

**Files:**
- Modify: `internal/github/sync.go:609-628` (Capabilities method)

- [ ] **Step 1: Add ThreadResolve to capabilities**

Edit the `Capabilities()` method of `gitHubClientProvider` in `internal/github/sync.go`:

```go
func (p gitHubClientProvider) Capabilities() platform.Capabilities {
	_, labels := p.client.(githubLabelClient)
	return platform.Capabilities{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		ReadComments:      true,
		ReadReleases:      true,
		ReadCI:            true,
		ReadLabels:        labels,
		CommentMutation:   true,
		StateMutation:     true,
		MergeMutation:     true,
		ReviewMutation:    true,
		WorkflowApproval:  true,
		ReadyForReview:    true,
		IssueMutation:     true,
		LabelMutation:     labels,
		ThreadResolve:     true,
	}
}
```

- [ ] **Step 2: Run build to verify**

Run: `go build ./internal/github/...`
Expected: Build succeeds

- [ ] **Step 3: Commit**

```bash
git add internal/github/sync.go
git commit -m "feat(github): add ThreadResolve capability

Enable resolve/unresolve support for GitHub review threads."
```

---

### Task 7: Extend gitHubClientProvider with GraphQL Client

**Files:**
- Modify: `internal/github/sync.go:560-563` (gitHubClientProvider struct)
- Modify: `internal/github/sync.go:570-580` (registryFromGitHubClients function)

- [ ] **Step 1: Add gqlClient field to gitHubClientProvider**

Edit `internal/github/sync.go` to extend the struct:

```go
type gitHubClientProvider struct {
	host      string
	client    Client
	gqlClient *githubv4.Client
}
```

- [ ] **Step 2: Update registryFromGitHubClients to accept GraphQL clients**

The function signature and body need to accept a map of GraphQL clients. Find `registryFromGitHubClients` and update it:

```go
func registryFromGitHubClients(clients map[string]Client, gqlClients map[string]*githubv4.Client) *platform.Registry {
	registry, err := platform.NewRegistry()
	if err != nil {
		panic(fmt.Sprintf("create empty provider registry: %v", err))
	}
	for host, client := range clients {
		registry.RegisterProvider(gitHubClientProvider{
			host:      host,
			client:    client,
			gqlClient: gqlClients[host],
		})
	}
	return registry
}
```

- [ ] **Step 3: Update callers of registryFromGitHubClients**

Search for calls to `registryFromGitHubClients` and update them to pass the GraphQL clients map. This is likely in the Syncer initialization.

- [ ] **Step 4: Run build to verify struct changes**

Run: `go build ./internal/github/...`
Expected: Build succeeds (or shows errors for callers that need updating)

- [ ] **Step 5: Commit**

```bash
git add internal/github/sync.go
git commit -m "refactor(github): add GraphQL client to provider struct

Extend gitHubClientProvider to hold a reference to the GraphQL client
for mutations that require it."
```

---

### Task 8: Implement ThreadResolver Interface for GitHub

**Files:**
- Create: `internal/github/resolve_thread.go`

- [ ] **Step 1: Create resolve thread mutation file**

Create `internal/github/resolve_thread.go`:

```go
package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/shurcooL/githubv4"
	"go.kenn.io/middleman/internal/platform"
)

// ResolveThread resolves or unresolves a GitHub review thread.
func (p gitHubClientProvider) ResolveThread(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	threadID string,
	resolved bool,
) error {
	if p.gqlClient == nil {
		return fmt.Errorf("GraphQL client not configured for host %s", p.host)
	}

	if err := validateGitHubThreadID(threadID); err != nil {
		return err
	}

	if resolved {
		return resolveReviewThread(ctx, p.gqlClient, threadID)
	}
	return unresolveReviewThread(ctx, p.gqlClient, threadID)
}

func validateGitHubThreadID(threadID string) error {
	// GitHub review thread IDs are GraphQL node IDs (base64-encoded strings)
	if threadID == "" {
		return fmt.Errorf("thread ID cannot be empty")
	}
	// Node IDs are typically at least 20+ characters
	if len(threadID) < 10 {
		return fmt.Errorf("invalid thread ID format: too short")
	}
	return nil
}

type resolveReviewThreadMutation struct {
	ResolveReviewThread struct {
		Thread struct {
			ID string
		}
	} `graphql:"resolveReviewThread(input: $input)"`
}

type unresolveReviewThreadMutation struct {
	UnresolveReviewThread struct {
		Thread struct {
			ID string
		}
	} `graphql:"unresolveReviewThread(input: $input)"`
}

type resolveReviewThreadInput struct {
	ThreadID githubv4.ID `json:"threadId"`
}

func resolveReviewThread(ctx context.Context, client *githubv4.Client, threadID string) error {
	var mutation resolveReviewThreadMutation
	input := resolveReviewThreadInput{
		ThreadID: githubv4.ID(threadID),
	}
	err := client.Mutate(ctx, &mutation, input, nil)
	if err != nil {
		return fmt.Errorf("resolveReviewThread mutation failed: %w", err)
	}
	return nil
}

func unresolveReviewThread(ctx context.Context, client *githubv4.Client, threadID string) error {
	var mutation unresolveReviewThreadMutation
	input := resolveReviewThreadInput{
		ThreadID: githubv4.ID(threadID),
	}
	err := client.Mutate(ctx, &mutation, input, nil)
	if err != nil {
		return fmt.Errorf("unresolveReviewThread mutation failed: %w", err)
	}
	return nil
}

var _ platform.ThreadResolver = gitHubClientProvider{}
```

- [ ] **Step 2: Run build to verify**

Run: `go build ./internal/github/...`
Expected: Build succeeds

- [ ] **Step 3: Commit**

```bash
git add internal/github/resolve_thread.go
git commit -m "feat(github): implement ThreadResolver interface

Add ResolveThread method using GitHub GraphQL resolveReviewThread
and unresolveReviewThread mutations."
```

---

### Task 9: Add E2E Test for Review Threads

**Files:**
- Create: `internal/server/e2etest/github_review_threads_test.go`

- [ ] **Step 1: Create E2E test file**

Create `internal/server/e2etest/github_review_threads_test.go`:

```go
package e2etest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

func TestGitHubReviewThreadsE2E(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	testDB := openTestDB(t)
	defer testDB.Close()

	// Insert a test repo
	repoID, err := testDB.UpsertRepo(context.Background(), db.RepoRow{
		Provider:     "github",
		PlatformHost: "github.com",
		Owner:        "testowner",
		Name:         "testrepo",
		RepoPath:     "testowner/testrepo",
	})
	require.NoError(err)

	// Insert a test PR
	prID, err := testDB.UpsertMergeRequest(context.Background(), db.MergeRequestRow{
		RepoID:    repoID,
		Number:    42,
		Title:     "Test PR",
		Author:    "author",
		State:     "open",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(err)

	// Insert review comment events
	err = testDB.UpsertMergeRequestEvents(context.Background(), prID, []db.MergeRequestEventRow{
		{
			MergeRequestID: prID,
			EventType:      "review_comment",
			Author:         "reviewer",
			Body:           "This needs a fix",
			CreatedAt:      time.Now().Add(-2 * time.Hour),
			DedupeKey:      "review-comment-1001",
			ThreadID:       "PRRT_thread1",
			PositionJSON:   `{"path":"src/main.go","line":42}`,
			Resolvable:     true,
			Resolved:       false,
		},
		{
			MergeRequestID: prID,
			EventType:      "review_comment",
			Author:         "author",
			Body:           "Fixed!",
			CreatedAt:      time.Now().Add(-1 * time.Hour),
			DedupeKey:      "review-comment-1002",
			ThreadID:       "PRRT_thread1",
			PositionJSON:   `{"path":"src/main.go","line":42}`,
			Resolvable:     true,
			Resolved:       false,
		},
	})
	require.NoError(err)

	srv := newTestServer(t, testDB, &fakeGitHubProvider{
		caps: platform.Capabilities{
			ReadMergeRequests: true,
			ThreadResolve:     true,
		},
	})

	// Fetch PR detail and verify review comments are returned
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pulls/github/testowner/testrepo/42", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)

	var result struct {
		Events []struct {
			EventType string `json:"EventType"`
			ThreadID  string `json:"ThreadID"`
			Body      string `json:"Body"`
			Resolved  bool   `json:"Resolved"`
		} `json:"events"`
	}
	err = json.NewDecoder(rr.Body).Decode(&result)
	require.NoError(err)

	// Find review_comment events
	var reviewComments []struct {
		EventType string
		ThreadID  string
		Body      string
		Resolved  bool
	}
	for _, e := range result.Events {
		if e.EventType == "review_comment" {
			reviewComments = append(reviewComments, e)
		}
	}

	assert.Len(reviewComments, 2)
	assert.Equal("PRRT_thread1", reviewComments[0].ThreadID)
	assert.Equal("PRRT_thread1", reviewComments[1].ThreadID)
}

type fakeGitHubProvider struct {
	caps platform.Capabilities
}

func (f *fakeGitHubProvider) Platform() platform.Kind {
	return platform.KindGitHub
}

func (f *fakeGitHubProvider) Host() string {
	return "github.com"
}

func (f *fakeGitHubProvider) Capabilities() platform.Capabilities {
	return f.caps
}
```

- [ ] **Step 2: Run the E2E test**

Run: `go test ./internal/server/e2etest/... -run TestGitHubReviewThreadsE2E -v -shuffle=on`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/server/e2etest/github_review_threads_test.go
git commit -m "test(e2e): add GitHub review threads integration test

Verify review_comment events are stored and returned with proper
ThreadID for grouping."
```

---

### Task 10: Add Unit Test for GraphQL Review Thread Adapter

**Files:**
- Create: `internal/github/graphql_review_threads_test.go`

- [ ] **Step 1: Create test file**

Create `internal/github/graphql_review_threads_test.go`:

```go
package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAdaptReviewThread(t *testing.T) {
	assert := assert.New(t)

	gql := &gqlReviewThread{
		ID:         "PRRT_abc123",
		IsResolved: true,
		Path:       "src/main.go",
		Line:       intPtr(42),
		StartLine:  intPtr(40),
	}
	gql.Comments.Nodes = []gqlReviewComment{
		{
			DatabaseId: 1001,
			Author:     struct{ Login string }{Login: "reviewer"},
			Body:       "Needs fix",
			CreatedAt:  time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			DiffHunk:   "@@ -40,6 +40,8 @@",
		},
		{
			DatabaseId: 1002,
			Author:     struct{ Login string }{Login: "author"},
			Body:       "Fixed",
			CreatedAt:  time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
			DiffHunk:   "@@ -40,6 +40,8 @@",
		},
	}

	thread := adaptReviewThread(gql)

	assert.Equal("PRRT_abc123", thread.ID)
	assert.True(thread.IsResolved)
	assert.Equal("src/main.go", thread.Path)
	assert.Equal(42, *thread.Line)
	assert.Equal(40, *thread.StartLine)
	assert.Len(thread.Comments, 2)

	assert.Equal(int64(1001), thread.Comments[0].DatabaseID)
	assert.Equal("reviewer", thread.Comments[0].Author)
	assert.Equal("Needs fix", thread.Comments[0].Body)
	assert.Equal("@@ -40,6 +40,8 @@", thread.Comments[0].DiffHunk)

	assert.Equal(int64(1002), thread.Comments[1].DatabaseID)
	assert.Equal("author", thread.Comments[1].Author)
}

func TestAdaptReviewThread_EmptyComments(t *testing.T) {
	assert := assert.New(t)

	gql := &gqlReviewThread{
		ID:         "PRRT_empty",
		IsResolved: false,
		Path:       "README.md",
	}

	thread := adaptReviewThread(gql)

	assert.Equal("PRRT_empty", thread.ID)
	assert.False(thread.IsResolved)
	assert.Empty(thread.Comments)
}

func TestAdaptReviewThread_NilLineNumbers(t *testing.T) {
	assert := assert.New(t)

	gql := &gqlReviewThread{
		ID:        "PRRT_nolines",
		Path:      "file.txt",
		Line:      nil,
		StartLine: nil,
	}

	thread := adaptReviewThread(gql)

	assert.Nil(thread.Line)
	assert.Nil(thread.StartLine)
}

func intPtr(i int) *int {
	return &i
}
```

- [ ] **Step 2: Run the tests**

Run: `go test ./internal/github/... -run TestAdaptReviewThread -v -shuffle=on`
Expected: All tests PASS

- [ ] **Step 3: Commit**

```bash
git add internal/github/graphql_review_threads_test.go
git commit -m "test(github): add unit tests for review thread adapter"
```

---

### Task 11: Final Integration Test

**Files:**
- None (verification only)

- [ ] **Step 1: Run all GitHub tests**

Run: `go test ./internal/github/... -v -shuffle=on`
Expected: All tests pass

- [ ] **Step 2: Run all platform/github tests**

Run: `go test ./internal/platform/github/... -v -shuffle=on`
Expected: All tests pass

- [ ] **Step 3: Run all E2E tests**

Run: `go test ./internal/server/e2etest/... -v -shuffle=on`
Expected: All tests pass

- [ ] **Step 4: Run full test suite**

Run: `make test`
Expected: All tests pass

- [ ] **Step 5: Final commit with all changes verified**

```bash
git add -A
git status
# Verify no uncommitted changes remain
```

If there are any uncommitted changes, commit them with an appropriate message.
