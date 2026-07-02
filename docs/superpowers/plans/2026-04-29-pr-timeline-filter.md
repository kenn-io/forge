# PR Timeline Filter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a localStorage-backed PR detail activity filter and expand stored PR timeline events to include cross references, title changes, and base branch changes.

**Architecture:** Extend the GitHub GraphQL timeline fetcher from force-push-only to a typed PR timeline event fetcher, normalize those events into the existing `middleman_mr_events` table, and render/filter them locally in the PR detail UI. No DB migration or settings API change is needed because new events fit the existing event row shape and PR filter state is browser-local.

**Tech Stack:** Go, go-github/v84, GitHub GraphQL API, SQLite, Svelte 5, TypeScript, Vitest, Testing Library, shared `FilterDropdown`.

---

## File Structure

- Modify `internal/github/client.go`: add `PullRequestTimelineEvent`, GraphQL query fields, and `ListPullRequestTimelineEvents`; keep `ListForcePushEvents` as a compatibility wrapper.
- Modify `internal/github/client_test.go`: replace force-push-only GraphQL tests with tests covering force push, cross reference, title rename, base ref change, pagination, GraphQL errors, and null repository/pull request errors.
- Modify `internal/github/normalize.go`: add `NormalizeTimelineEvent` and metadata structs for new system event types.
- Modify `internal/github/normalize_test.go`: cover normalization for `cross_referenced`, `renamed_title`, `base_ref_changed`, and existing `force_push`.
- Modify `internal/github/sync.go`: fetch typed timeline events in `refreshTimeline`, log optional failures, and upsert normalized system events alongside comments/reviews/commits.
- Modify `internal/github/sync_test.go`: update mock client and add sync coverage for new timeline events plus optional timeline fetch failure.
- Create `packages/ui/src/components/detail/prTimelineFilter.ts`: event classification, localStorage persistence, filter defaults, and filtering helpers.
- Create `packages/ui/src/components/detail/prTimelineFilter.test.ts`: unit tests for default state, persistence, classification, bot filtering, and event filtering.
- Create `packages/ui/src/components/detail/PRTimelineFilter.svelte`: reusable PR detail filter control that wraps shared `FilterDropdown`.
- Create `packages/ui/src/components/detail/PRTimelineFilter.test.ts`: component tests proving the shared dropdown control toggles PR timeline filters.
- Modify `packages/ui/src/components/detail/EventTimeline.svelte`: compact rendering for commit/system events and filtered-empty messaging.
- Modify `packages/ui/src/components/detail/EventTimeline.test.ts`: cover compact commits and new system event rendering.
- Modify `packages/ui/src/components/detail/PullDetail.svelte`: wire filter state above the Activity feed and pass filtered events to `EventTimeline`.

---

## Task 1: Backend Timeline Event Types And Client Tests

**Files:**
- Modify: `internal/github/client.go`
- Modify: `internal/github/client_test.go`

- [ ] **Step 1: Write failing client interface and parsing tests**

Add tests in `internal/github/client_test.go` that call `ListPullRequestTimelineEvents` and expect all approved event types to parse from one paginated GraphQL response.

```go
func TestClientInterfaceIncludesListPullRequestTimelineEvents(t *testing.T) {
	_, ok := reflect.TypeFor[Client]().MethodByName("ListPullRequestTimelineEvents")
	require.True(t, ok)
}

func TestListPullRequestTimelineEvents(t *testing.T) {
	require := require.New(t)
	var calls int
	var methods []string
	var contentTypes []string
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		calls++
		methods = append(methods, r.Method)
		contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[{"__typename":"HeadRefForcePushedEvent","id":"HFP_1","actor":{"login":"alice"},"beforeCommit":{"oid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"afterCommit":{"oid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"createdAt":"2024-06-01T12:00:00Z","ref":{"name":"feature"}},{"__typename":"RenamedTitleEvent","id":"RTE_1","actor":{"login":"bob"},"createdAt":"2024-06-01T12:05:00Z","previousTitle":"Old title","currentTitle":"New title"}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"}}}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[{"__typename":"BaseRefChangedEvent","id":"BRC_1","actor":{"login":"carol"},"createdAt":"2024-06-01T12:10:00Z","previousRefName":"main","currentRefName":"release"},{"__typename":"CrossReferencedEvent","id":"CRE_1","actor":{"login":"dave"},"createdAt":"2024-06-01T12:15:00Z","isCrossRepository":true,"willCloseTarget":false,"source":{"__typename":"Issue","number":77,"title":"Related bug","url":"https://github.com/other/repo/issues/77","repository":{"owner":{"login":"other"},"name":"repo"}}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &liveClient{
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL + "/graphql",
	}

	events, err := c.ListPullRequestTimelineEvents(t.Context(), "owner", "repo", 42)
	require.NoError(err)
	require.Len(events, 4)
	require.Equal("force_push", events[0].EventType)
	require.Equal("HFP_1", events[0].NodeID)
	require.Equal("alice", events[0].Actor)
	require.Equal("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", events[0].BeforeSHA)
	require.Equal("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", events[0].AfterSHA)
	require.Equal("feature", events[0].Ref)
	require.Equal("renamed_title", events[1].EventType)
	require.Equal("Old title", events[1].PreviousTitle)
	require.Equal("New title", events[1].CurrentTitle)
	require.Equal("base_ref_changed", events[2].EventType)
	require.Equal("main", events[2].PreviousRefName)
	require.Equal("release", events[2].CurrentRefName)
	require.Equal("cross_referenced", events[3].EventType)
	require.Equal("Issue", events[3].SourceType)
	require.Equal("other", events[3].SourceOwner)
	require.Equal("repo", events[3].SourceRepo)
	require.Equal(77, events[3].SourceNumber)
	require.Equal("Related bug", events[3].SourceTitle)
	require.True(events[3].IsCrossRepository)
	require.False(events[3].WillCloseTarget)
	require.Equal(2, calls)
	require.Equal([]string{http.MethodPost, http.MethodPost}, methods)
	require.Equal([]string{"application/json", "application/json"}, contentTypes)
}
```

Update the existing error tests to call `ListPullRequestTimelineEvents` and expect the same error behavior:

```go
func TestListPullRequestTimelineEventsReturnsGraphQLErrors(t *testing.T) {
	require := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"permission denied"}],"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
	}))
	defer srv.Close()

	c := &liveClient{
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL,
	}

	events, err := c.ListPullRequestTimelineEvents(t.Context(), "owner", "repo", 42)
	require.Nil(events)
	require.ErrorContains(err, "permission denied")
}
```

- [ ] **Step 2: Run the failing client tests**

Run:

```bash
go test ./internal/github -run 'TestClientInterfaceIncludesListPullRequestTimelineEvents|TestListPullRequestTimelineEvents' -shuffle=on
```

Expected: FAIL because `Client` does not yet expose `ListPullRequestTimelineEvents`.

- [ ] **Step 3: Implement typed timeline event fetch**

In `internal/github/client.go`, add the typed event model near `ForcePushEvent`:

```go
type PullRequestTimelineEvent struct {
	NodeID            string
	EventType         string
	Actor             string
	CreatedAt         time.Time
	BeforeSHA         string
	AfterSHA          string
	Ref               string
	PreviousTitle     string
	CurrentTitle      string
	PreviousRefName   string
	CurrentRefName    string
	SourceType        string
	SourceOwner       string
	SourceRepo        string
	SourceNumber      int
	SourceTitle       string
	SourceURL         string
	IsCrossRepository bool
	WillCloseTarget   bool
}
```

Add this method to `Client`:

```go
ListPullRequestTimelineEvents(ctx context.Context, owner, repo string, number int) ([]PullRequestTimelineEvent, error)
```

Replace `forcePushTimelineQuery` with a broader query:

```go
const pullRequestTimelineEventsQuery = `
query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      timelineItems(
        itemTypes: [
          HEAD_REF_FORCE_PUSHED_EVENT,
          CROSS_REFERENCED_EVENT,
          RENAMED_TITLE_EVENT,
          BASE_REF_CHANGED_EVENT
        ],
        first: 100,
        after: $cursor
      ) {
        nodes {
          __typename
          ... on Node { id }
          ... on HeadRefForcePushedEvent {
            actor { login }
            beforeCommit { oid }
            afterCommit { oid }
            createdAt
            ref { name }
          }
          ... on RenamedTitleEvent {
            actor { login }
            createdAt
            previousTitle
            currentTitle
          }
          ... on BaseRefChangedEvent {
            actor { login }
            createdAt
            previousRefName
            currentRefName
          }
          ... on CrossReferencedEvent {
            actor { login }
            createdAt
            isCrossRepository
            willCloseTarget
            source {
              __typename
              ... on Issue {
                number
                title
                url
                repository { owner { login } name }
              }
              ... on PullRequest {
                number
                title
                url
                repository { owner { login } name }
              }
            }
          }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`
```

Implement `ListPullRequestTimelineEvents` by adapting the current `ListForcePushEvents` loop, decoding `__typename`, and mapping type names:

```go
func timelineEventType(graphQLType string) string {
	switch graphQLType {
	case "HeadRefForcePushedEvent":
		return "force_push"
	case "CrossReferencedEvent":
		return "cross_referenced"
	case "RenamedTitleEvent":
		return "renamed_title"
	case "BaseRefChangedEvent":
		return "base_ref_changed"
	default:
		return ""
	}
}
```

Keep `ListForcePushEvents` as a compatibility wrapper:

```go
func (c *liveClient) ListForcePushEvents(
	ctx context.Context, owner, repo string, number int,
) ([]ForcePushEvent, error) {
	timelineEvents, err := c.ListPullRequestTimelineEvents(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	var events []ForcePushEvent
	for _, event := range timelineEvents {
		if event.EventType != "force_push" {
			continue
		}
		events = append(events, ForcePushEvent{
			Actor:     event.Actor,
			BeforeSHA: event.BeforeSHA,
			AfterSHA:  event.AfterSHA,
			Ref:       event.Ref,
			CreatedAt: event.CreatedAt,
		})
	}
	return events, nil
}
```

- [ ] **Step 4: Run the client tests again**

Run:

```bash
go test ./internal/github -run 'TestClientInterfaceIncludesListPullRequestTimelineEvents|TestListPullRequestTimelineEvents|TestListForcePushEvents' -shuffle=on
```

Expected: PASS.

---

## Task 2: Backend Timeline Event Normalization

**Files:**
- Modify: `internal/github/normalize.go`
- Modify: `internal/github/normalize_test.go`

- [ ] **Step 1: Write failing normalization tests**

Add tests in `internal/github/normalize_test.go`:

```go
func TestNormalizeTimelineEventCrossReferenced(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	createdAt := time.Date(2024, 6, 1, 12, 15, 0, 0, time.UTC)

	event := NormalizeTimelineEvent(17, PullRequestTimelineEvent{
		NodeID:            "CRE_1",
		EventType:         "cross_referenced",
		Actor:             "alice",
		CreatedAt:         createdAt,
		SourceType:        "Issue",
		SourceOwner:       "other",
		SourceRepo:        "repo",
		SourceNumber:      77,
		SourceTitle:       "Related bug",
		SourceURL:         "https://github.com/other/repo/issues/77",
		IsCrossRepository: true,
		WillCloseTarget:   false,
	})

	require.NotNil(event)
	assert.Equal(int64(17), event.MergeRequestID)
	assert.Equal("cross_referenced", event.EventType)
	assert.Equal("alice", event.Author)
	assert.Equal("Referenced from other/repo#77", event.Summary)
	assert.Equal(createdAt, event.CreatedAt)
	assert.Equal("timeline-CRE_1", event.DedupeKey)
	assert.Contains(event.MetadataJSON, `"source_title":"Related bug"`)
	assert.Contains(event.MetadataJSON, `"is_cross_repository":true`)
}

func TestNormalizeTimelineEventRenamedTitle(t *testing.T) {
	assert := assert.New(t)
	createdAt := time.Date(2024, 6, 1, 12, 5, 0, 0, time.UTC)

	event := NormalizeTimelineEvent(17, PullRequestTimelineEvent{
		NodeID:        "RTE_1",
		EventType:     "renamed_title",
		Actor:         "bob",
		CreatedAt:     createdAt,
		PreviousTitle: "Old title",
		CurrentTitle:  "New title",
	})

	assert.Equal("renamed_title", event.EventType)
	assert.Equal("bob", event.Author)
	assert.Equal(`"Old title" -> "New title"`, event.Summary)
	assert.Contains(event.MetadataJSON, `"previous_title":"Old title"`)
	assert.Contains(event.MetadataJSON, `"current_title":"New title"`)
}

func TestNormalizeTimelineEventBaseRefChanged(t *testing.T) {
	assert := assert.New(t)
	createdAt := time.Date(2024, 6, 1, 12, 10, 0, 0, time.UTC)

	event := NormalizeTimelineEvent(17, PullRequestTimelineEvent{
		NodeID:          "BRC_1",
		EventType:       "base_ref_changed",
		Actor:           "carol",
		CreatedAt:       createdAt,
		PreviousRefName: "main",
		CurrentRefName:  "release",
	})

	assert.Equal("base_ref_changed", event.EventType)
	assert.Equal("carol", event.Author)
	assert.Equal("main -> release", event.Summary)
	assert.Contains(event.MetadataJSON, `"previous_ref_name":"main"`)
	assert.Contains(event.MetadataJSON, `"current_ref_name":"release"`)
}
```

- [ ] **Step 2: Run the failing normalization tests**

Run:

```bash
go test ./internal/github -run 'TestNormalizeTimelineEvent' -shuffle=on
```

Expected: FAIL because `NormalizeTimelineEvent` does not exist.

- [ ] **Step 3: Implement timeline normalization**

In `internal/github/normalize.go`, add metadata structs and a normalizer:

```go
type crossReferenceMetadata struct {
	SourceType        string `json:"source_type"`
	SourceOwner       string `json:"source_owner"`
	SourceRepo        string `json:"source_repo"`
	SourceNumber      int    `json:"source_number"`
	SourceTitle       string `json:"source_title"`
	SourceURL         string `json:"source_url"`
	IsCrossRepository bool   `json:"is_cross_repository"`
	WillCloseTarget   bool   `json:"will_close_target"`
}

type renamedTitleMetadata struct {
	PreviousTitle string `json:"previous_title"`
	CurrentTitle  string `json:"current_title"`
}

type baseRefChangedMetadata struct {
	PreviousRefName string `json:"previous_ref_name"`
	CurrentRefName  string `json:"current_ref_name"`
}

func NormalizeTimelineEvent(mrID int64, event PullRequestTimelineEvent) *db.MREvent {
	switch event.EventType {
	case "force_push":
		fp := NormalizeForcePushEvent(mrID, ForcePushEvent{
			Actor:     event.Actor,
			BeforeSHA: event.BeforeSHA,
			AfterSHA:  event.AfterSHA,
			Ref:       event.Ref,
			CreatedAt: event.CreatedAt,
		})
		if event.NodeID != "" {
			fp.DedupeKey = "timeline-" + event.NodeID
		}
		return &fp
	case "cross_referenced":
		metadata, _ := json.Marshal(crossReferenceMetadata{
			SourceType:        event.SourceType,
			SourceOwner:       event.SourceOwner,
			SourceRepo:        event.SourceRepo,
			SourceNumber:      event.SourceNumber,
			SourceTitle:       event.SourceTitle,
			SourceURL:         event.SourceURL,
			IsCrossRepository: event.IsCrossRepository,
			WillCloseTarget:   event.WillCloseTarget,
		})
		return &db.MREvent{
			MergeRequestID: mrID,
			EventType:      "cross_referenced",
			Author:         event.Actor,
			Summary:        fmt.Sprintf("Referenced from %s/%s#%d", event.SourceOwner, event.SourceRepo, event.SourceNumber),
			MetadataJSON:   string(metadata),
			CreatedAt:      event.CreatedAt,
			DedupeKey:      timelineDedupeKey(event),
		}
	case "renamed_title":
		metadata, _ := json.Marshal(renamedTitleMetadata{
			PreviousTitle: event.PreviousTitle,
			CurrentTitle:  event.CurrentTitle,
		})
		return &db.MREvent{
			MergeRequestID: mrID,
			EventType:      "renamed_title",
			Author:         event.Actor,
			Summary:        fmt.Sprintf("%q -> %q", event.PreviousTitle, event.CurrentTitle),
			MetadataJSON:   string(metadata),
			CreatedAt:      event.CreatedAt,
			DedupeKey:      timelineDedupeKey(event),
		}
	case "base_ref_changed":
		metadata, _ := json.Marshal(baseRefChangedMetadata{
			PreviousRefName: event.PreviousRefName,
			CurrentRefName:  event.CurrentRefName,
		})
		return &db.MREvent{
			MergeRequestID: mrID,
			EventType:      "base_ref_changed",
			Author:         event.Actor,
			Summary:        event.PreviousRefName + " -> " + event.CurrentRefName,
			MetadataJSON:   string(metadata),
			CreatedAt:      event.CreatedAt,
			DedupeKey:      timelineDedupeKey(event),
		}
	default:
		return nil
	}
}

func timelineDedupeKey(event PullRequestTimelineEvent) string {
	if event.NodeID != "" {
		return "timeline-" + event.NodeID
	}
	raw := strings.Join([]string{
		event.EventType,
		event.CreatedAt.UTC().Format(time.RFC3339Nano),
		event.Actor,
		event.PreviousTitle,
		event.CurrentTitle,
		event.PreviousRefName,
		event.CurrentRefName,
		fmt.Sprintf("%s/%s#%d", event.SourceOwner, event.SourceRepo, event.SourceNumber),
	}, "|")
	return "timeline-" + shortHash(raw)
}
```

Add `shortHash` in `normalize.go` with `crypto/sha1`, `encoding/hex`, and the first 12 hex characters:

```go
func shortHash(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}
```

- [ ] **Step 4: Run normalization tests**

Run:

```bash
go test ./internal/github -run 'TestNormalizeTimelineEvent|TestNormalizeForcePushEvent' -shuffle=on
```

Expected: PASS.

---

## Task 3: Sync New Timeline Events

**Files:**
- Modify: `internal/github/sync.go`
- Modify: `internal/github/sync_test.go`

- [ ] **Step 1: Update mock client and write failing sync tests**

In `internal/github/sync_test.go`, add fields and method to `mockClient`:

```go
	timelineEvents    []PullRequestTimelineEvent
	timelineEventsErr error
```

```go
func (m *mockClient) ListPullRequestTimelineEvents(_ context.Context, _, _ string, _ int) ([]PullRequestTimelineEvent, error) {
	m.trackCall()
	if m.timelineEventsErr != nil {
		return nil, m.timelineEventsErr
	}
	return m.timelineEvents, nil
}
```

Add this test near `TestSyncStoresForcePushEvent`:

```go
func TestSyncStoresPullRequestTimelineEvents(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	mc := &mockClient{
		openPRs: []*gh.PullRequest{buildOpenPR(1, now)},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
		timelineEvents: []PullRequestTimelineEvent{
			{
				NodeID:            "CRE_1",
				EventType:         "cross_referenced",
				Actor:             "alice",
				CreatedAt:         now.Add(-3 * time.Minute),
				SourceType:        "Issue",
				SourceOwner:       "other",
				SourceRepo:        "repo",
				SourceNumber:      77,
				SourceTitle:       "Related bug",
				SourceURL:         "https://github.com/other/repo/issues/77",
				IsCrossRepository: true,
			},
			{
				NodeID:          "BRC_1",
				EventType:       "base_ref_changed",
				Actor:           "bob",
				CreatedAt:       now.Add(-2 * time.Minute),
				PreviousRefName: "main",
				CurrentRefName:  "release",
			},
			{
				NodeID:        "RTE_1",
				EventType:     "renamed_title",
				Actor:         "carol",
				CreatedAt:     now.Add(-1 * time.Minute),
				PreviousTitle: "Old",
				CurrentTitle:  "New",
			},
		},
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))
	syncer.RunOnce(ctx)

	pr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(pr)

	events, err := d.ListMREvents(ctx, pr.ID)
	require.NoError(err)

	byType := map[string]db.MREvent{}
	for _, event := range events {
		byType[event.EventType] = event
	}
	assert.Contains(byType, "cross_referenced")
	assert.Contains(byType, "base_ref_changed")
	assert.Contains(byType, "renamed_title")
	assert.Contains(byType["cross_referenced"].MetadataJSON, `"source_title":"Related bug"`)
	assert.Equal("main -> release", byType["base_ref_changed"].Summary)
	assert.Equal(`"Old" -> "New"`, byType["renamed_title"].Summary)
}
```

Add optional-failure coverage:

```go
func TestSyncIgnoresPullRequestTimelineFetchFailures(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	commentID := int64(123)
	body := "human comment"
	user := &gh.User{Login: new("alice")}

	mc := &mockClient{
		openPRs: []*gh.PullRequest{buildOpenPR(1, now)},
		comments: []*gh.IssueComment{{
			ID:        &commentID,
			User:      user,
			Body:      &body,
			CreatedAt: makeTimestamp(now.Add(-time.Minute)),
		}},
		reviews:           []*gh.PullRequestReview{},
		commits:           []*gh.RepositoryCommit{},
		timelineEventsErr: errors.New("graphql unavailable"),
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))
	syncer.RunOnce(ctx)

	pr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(pr)
	events, err := d.ListMREvents(ctx, pr.ID)
	require.NoError(err)
	require.NotEmpty(events)
	require.Equal("issue_comment", events[0].EventType)
}
```

- [ ] **Step 2: Run the failing sync tests**

Run:

```bash
go test ./internal/github -run 'TestSyncStoresPullRequestTimelineEvents|TestSyncIgnoresPullRequestTimelineFetchFailures' -shuffle=on
```

Expected: FAIL because sync does not call `ListPullRequestTimelineEvents`.

- [ ] **Step 3: Implement sync wiring**

In `refreshTimeline`, replace the force-push-only fetch block with:

```go
timelineEvents, err := client.ListPullRequestTimelineEvents(ctx, repo.Owner, repo.Name, number)
if err != nil {
	slog.Warn("timeline event fetch failed during timeline refresh",
		"repo", repo.Owner+"/"+repo.Name,
		"number", number,
		"err", err,
	)
	timelineEvents = nil
}
```

Replace the force-push append loop with:

```go
for _, timelineEvent := range timelineEvents {
	event := NormalizeTimelineEvent(mrID, timelineEvent)
	if event == nil {
		continue
	}
	events = append(events, *event)
}
```

Keep `computeLastActivity` unchanged for now. The existing behavior derives last activity from PR updated time, comments, reviews, and commits; system timeline events should display in detail but do not need to alter list ordering in this slice.

- [ ] **Step 4: Run sync tests**

Run:

```bash
go test ./internal/github -run 'TestSyncStoresPullRequestTimelineEvents|TestSyncIgnoresPullRequestTimelineFetchFailures|TestSyncStoresForcePushEvent' -shuffle=on
```

Expected: PASS.

---

## Task 4: PR Timeline Filter Helper

**Files:**
- Create: `packages/ui/src/components/detail/prTimelineFilter.ts`
- Create: `packages/ui/src/components/detail/prTimelineFilter.test.ts`

- [ ] **Step 1: Write failing helper tests**

Create `packages/ui/src/components/detail/prTimelineFilter.test.ts`:

```ts
import { beforeEach, describe, expect, it } from "vitest";
import type { PREvent } from "../../api/types.js";
import {
  DEFAULT_PR_TIMELINE_FILTER,
  filterPREvents,
  loadPRTimelineFilter,
  savePRTimelineFilter,
  timelineEventBucket,
} from "./prTimelineFilter.js";

function event(overrides: Partial<PREvent>): PREvent {
  return {
    ID: 1,
    MergeRequestID: 1,
    PlatformID: null,
    EventType: "issue_comment",
    Author: "alice",
    Summary: "",
    Body: "body",
    MetadataJSON: "",
    CreatedAt: "2024-06-01T12:00:00Z",
    DedupeKey: "event-1",
    ...overrides,
  } as PREvent;
}

describe("prTimelineFilter", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("defaults to showing every bucket and bot activity", () => {
    expect(loadPRTimelineFilter()).toEqual(DEFAULT_PR_TIMELINE_FILTER);
  });

  it("persists valid filter state to localStorage", () => {
    savePRTimelineFilter({
      showMessages: false,
      showCommitDetails: true,
      showEvents: true,
      showForcePushes: false,
      hideBots: true,
    });

    expect(loadPRTimelineFilter()).toEqual({
      showMessages: false,
      showCommitDetails: true,
      showEvents: true,
      showForcePushes: false,
      hideBots: true,
    });
  });

  it("classifies event buckets", () => {
    expect(timelineEventBucket(event({ EventType: "issue_comment" }))).toBe("messages");
    expect(timelineEventBucket(event({ EventType: "review" }))).toBe("messages");
    expect(timelineEventBucket(event({ EventType: "commit" }))).toBe("commitDetails");
    expect(timelineEventBucket(event({ EventType: "force_push" }))).toBe("forcePushes");
    expect(timelineEventBucket(event({ EventType: "cross_referenced" }))).toBe("events");
    expect(timelineEventBucket(event({ EventType: "renamed_title" }))).toBe("events");
    expect(timelineEventBucket(event({ EventType: "base_ref_changed" }))).toBe("events");
  });

  it("filters by disabled buckets and bots", () => {
    const events = [
      event({ ID: 1, EventType: "issue_comment", Author: "alice" }),
      event({ ID: 2, EventType: "review", Author: "renovate[bot]" }),
      event({ ID: 3, EventType: "commit", Author: "alice" }),
      event({ ID: 4, EventType: "force_push", Author: "alice" }),
      event({ ID: 5, EventType: "base_ref_changed", Author: "alice" }),
    ];

    expect(filterPREvents(events, {
      showMessages: true,
      showCommitDetails: false,
      showEvents: true,
      showForcePushes: false,
      hideBots: true,
    }).map((item) => item.ID)).toEqual([1, 5]);
  });
});
```

- [ ] **Step 2: Run the failing helper tests**

Run:

```bash
cd frontend && bunx vitest run ../packages/ui/src/components/detail/prTimelineFilter.test.ts
```

Expected: FAIL because `prTimelineFilter.ts` does not exist.

- [ ] **Step 3: Implement helper**

Create `packages/ui/src/components/detail/prTimelineFilter.ts`:

```ts
import type { PREvent } from "../../api/types.js";

export interface PRTimelineFilterState {
  showMessages: boolean;
  showCommitDetails: boolean;
  showEvents: boolean;
  showForcePushes: boolean;
  hideBots: boolean;
}

export type PRTimelineEventBucket =
  | "messages"
  | "commitDetails"
  | "events"
  | "forcePushes";

export const PR_TIMELINE_FILTER_STORAGE_KEY = "middleman-pr-timeline-filter";

export const DEFAULT_PR_TIMELINE_FILTER: PRTimelineFilterState = {
  showMessages: true,
  showCommitDetails: true,
  showEvents: true,
  showForcePushes: true,
  hideBots: false,
};

const BOT_SUFFIXES = ["[bot]", "-bot", "bot"];

export function isBotAuthor(author: string): boolean {
  const lower = author.toLowerCase();
  return BOT_SUFFIXES.some((suffix) => lower.endsWith(suffix));
}

export function timelineEventBucket(event: PREvent): PRTimelineEventBucket {
  switch (event.EventType) {
    case "issue_comment":
    case "review":
    case "review_comment":
      return "messages";
    case "commit":
      return "commitDetails";
    case "force_push":
      return "forcePushes";
    default:
      return "events";
  }
}

function normalizeFilter(value: Partial<PRTimelineFilterState> | null): PRTimelineFilterState {
  return {
    showMessages: value?.showMessages ?? DEFAULT_PR_TIMELINE_FILTER.showMessages,
    showCommitDetails: value?.showCommitDetails ?? DEFAULT_PR_TIMELINE_FILTER.showCommitDetails,
    showEvents: value?.showEvents ?? DEFAULT_PR_TIMELINE_FILTER.showEvents,
    showForcePushes: value?.showForcePushes ?? DEFAULT_PR_TIMELINE_FILTER.showForcePushes,
    hideBots: value?.hideBots ?? DEFAULT_PR_TIMELINE_FILTER.hideBots,
  };
}

export function loadPRTimelineFilter(): PRTimelineFilterState {
  try {
    const raw = localStorage.getItem(PR_TIMELINE_FILTER_STORAGE_KEY);
    if (!raw) return DEFAULT_PR_TIMELINE_FILTER;
    const parsed = JSON.parse(raw) as Partial<PRTimelineFilterState>;
    return normalizeFilter(parsed);
  } catch {
    return DEFAULT_PR_TIMELINE_FILTER;
  }
}

export function savePRTimelineFilter(filter: PRTimelineFilterState): void {
  try {
    localStorage.setItem(PR_TIMELINE_FILTER_STORAGE_KEY, JSON.stringify(filter));
  } catch {
    // localStorage can be unavailable in private browsing or embedded contexts.
  }
}

export function filterPREvents(events: PREvent[], filter: PRTimelineFilterState): PREvent[] {
  return events.filter((event) => {
    if (filter.hideBots && isBotAuthor(event.Author)) return false;
    switch (timelineEventBucket(event)) {
      case "messages":
        return filter.showMessages;
      case "commitDetails":
        return filter.showCommitDetails;
      case "events":
        return filter.showEvents;
      case "forcePushes":
        return filter.showForcePushes;
    }
  });
}

export function activePRTimelineFilterCount(filter: PRTimelineFilterState): number {
  return [
    !filter.showMessages,
    !filter.showCommitDetails,
    !filter.showEvents,
    !filter.showForcePushes,
    filter.hideBots,
  ].filter(Boolean).length;
}
```

- [ ] **Step 4: Run helper tests**

Run:

```bash
cd frontend && bunx vitest run ../packages/ui/src/components/detail/prTimelineFilter.test.ts
```

Expected: PASS.

---

## Task 5: PR Timeline Filter Component

**Files:**
- Create: `packages/ui/src/components/detail/PRTimelineFilter.svelte`
- Create: `packages/ui/src/components/detail/PRTimelineFilter.test.ts`

- [ ] **Step 1: Write failing component tests**

Create `packages/ui/src/components/detail/PRTimelineFilter.test.ts`:

```ts
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import PRTimelineFilter from "./PRTimelineFilter.svelte";
import { DEFAULT_PR_TIMELINE_FILTER } from "./prTimelineFilter.js";

describe("PRTimelineFilter", () => {
  afterEach(() => {
    cleanup();
  });

  it("uses the shared filter dropdown trigger and emits changes", async () => {
    const onChange = vi.fn();
    render(PRTimelineFilter, {
      props: {
        filter: DEFAULT_PR_TIMELINE_FILTER,
        onChange,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /filters/i }));
    await fireEvent.click(screen.getByRole("button", { name: /messages/i }));

    expect(onChange).toHaveBeenCalledWith({
      ...DEFAULT_PR_TIMELINE_FILTER,
      showMessages: false,
    });
    expect(document.querySelector(".filter-dropdown")).toBeTruthy();
  });

  it("shows active filter count and reset", async () => {
    const onChange = vi.fn();
    render(PRTimelineFilter, {
      props: {
        filter: {
          ...DEFAULT_PR_TIMELINE_FILTER,
          showCommitDetails: false,
          hideBots: true,
        },
        onChange,
      },
    });

    expect(screen.getByText("2")).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: /filters/i }));
    await fireEvent.click(screen.getByRole("button", { name: /show all/i }));

    expect(onChange).toHaveBeenCalledWith(DEFAULT_PR_TIMELINE_FILTER);
  });
});
```

- [ ] **Step 2: Run failing component tests**

Run:

```bash
cd frontend && bunx vitest run ../packages/ui/src/components/detail/PRTimelineFilter.test.ts
```

Expected: FAIL because `PRTimelineFilter.svelte` does not exist.

- [ ] **Step 3: Implement component using shared `FilterDropdown`**

Create `packages/ui/src/components/detail/PRTimelineFilter.svelte`:

```svelte
<script lang="ts">
  import FilterDropdown from "../shared/FilterDropdown.svelte";
  import {
    DEFAULT_PR_TIMELINE_FILTER,
    activePRTimelineFilterCount,
    type PRTimelineFilterState,
  } from "./prTimelineFilter.js";

  interface Props {
    filter: PRTimelineFilterState;
    onChange: (filter: PRTimelineFilterState) => void;
  }

  let { filter, onChange }: Props = $props();

  const activeCount = $derived(activePRTimelineFilterCount(filter));

  function update(patch: Partial<PRTimelineFilterState>): void {
    onChange({ ...filter, ...patch });
  }

  const sections = $derived.by(() => [
    {
      title: "Content",
      items: [
        {
          id: "messages",
          label: "Messages",
          active: filter.showMessages,
          color: "var(--accent-blue)",
          onSelect: () => update({ showMessages: !filter.showMessages }),
        },
        {
          id: "commit-details",
          label: "Commit details",
          active: filter.showCommitDetails,
          color: "var(--accent-green)",
          onSelect: () => update({ showCommitDetails: !filter.showCommitDetails }),
        },
        {
          id: "events",
          label: "Events",
          active: filter.showEvents,
          color: "var(--accent-amber)",
          onSelect: () => update({ showEvents: !filter.showEvents }),
        },
        {
          id: "force-pushes",
          label: "Force pushes",
          active: filter.showForcePushes,
          color: "var(--accent-red)",
          onSelect: () => update({ showForcePushes: !filter.showForcePushes }),
        },
      ],
    },
    {
      title: "Visibility",
      items: [
        {
          id: "hide-bots",
          label: "Hide bot activity",
          active: filter.hideBots,
          color: "var(--accent-purple)",
          onSelect: () => update({ hideBots: !filter.hideBots }),
        },
      ],
    },
  ]);
</script>

<FilterDropdown
  label="Filters"
  active={activeCount > 0}
  badgeCount={activeCount}
  title="Filter PR activity"
  {sections}
  minWidth="220px"
  {...activeCount > 0
    ? {
        resetLabel: "Show all",
        onReset: () => onChange(DEFAULT_PR_TIMELINE_FILTER),
      }
    : {}}
/>
```

- [ ] **Step 4: Run component tests and Svelte autofixer**

Run:

```bash
cd frontend && bunx vitest run ../packages/ui/src/components/detail/PRTimelineFilter.test.ts
npx @sveltejs/mcp@0.1.22 svelte-autofixer packages/ui/src/components/detail/PRTimelineFilter.svelte --svelte-version 5
```

Expected: PASS from Vitest and no required Svelte autofixer changes.

---

## Task 6: Compact Event Timeline Rendering

**Files:**
- Modify: `packages/ui/src/components/detail/EventTimeline.svelte`
- Modify: `packages/ui/src/components/detail/EventTimeline.test.ts`

- [ ] **Step 1: Write failing rendering tests**

Extend `packages/ui/src/components/detail/EventTimeline.test.ts`:

```ts
it("renders commit events as compact one-line commit detail rows", () => {
  render(EventTimeline, {
    props: {
      events: [makeEvent({
        EventType: "commit",
        Summary: "abcdef1234567890",
        Body: "feat: add timeline filters\n\nLong body",
      })],
    },
  });

  expect(screen.getByText("abcdef1")).toBeTruthy();
  expect(screen.getByText("feat: add timeline filters")).toBeTruthy();
  expect(document.querySelector(".event--compact")).toBeTruthy();
  expect(document.querySelector(".commit-title")).toBeTruthy();
});

it("renders system events as compact rows", () => {
  render(EventTimeline, {
    props: {
      events: [
        makeEvent({
          ID: 2,
          EventType: "renamed_title",
          Summary: "\"Old\" -> \"New\"",
          MetadataJSON: JSON.stringify({
            previous_title: "Old",
            current_title: "New",
          }),
        }),
        makeEvent({
          ID: 3,
          EventType: "base_ref_changed",
          Summary: "main -> release",
          MetadataJSON: JSON.stringify({
            previous_ref_name: "main",
            current_ref_name: "release",
          }),
        }),
        makeEvent({
          ID: 4,
          EventType: "cross_referenced",
          Summary: "Referenced from other/repo#77",
          MetadataJSON: JSON.stringify({
            source_owner: "other",
            source_repo: "repo",
            source_number: 77,
            source_title: "Related bug",
            source_url: "https://github.com/other/repo/issues/77",
          }),
        }),
      ],
    },
  });

  expect(screen.getByText("Title changed")).toBeTruthy();
  expect(screen.getByText("Base changed")).toBeTruthy();
  expect(screen.getByText("Referenced")).toBeTruthy();
  expect(screen.getByText("Related bug")).toBeTruthy();
  expect(document.querySelectorAll(".event--compact").length).toBe(3);
});

it("shows filtered empty copy when filters hide all events", () => {
  render(EventTimeline, {
    props: {
      events: [],
      filtered: true,
    },
  });

  expect(screen.getByText("No activity matches the current filters")).toBeTruthy();
});
```

- [ ] **Step 2: Run failing rendering tests**

Run:

```bash
cd frontend && bunx vitest run ../packages/ui/src/components/detail/EventTimeline.test.ts
```

Expected: FAIL because compact rendering and `filtered` prop do not exist.

- [ ] **Step 3: Implement compact render paths**

In `EventTimeline.svelte`, add `filtered?: boolean` to props:

```ts
interface Props {
  events: Array<PREvent | IssueEvent>;
  repoOwner?: string;
  repoName?: string;
  filtered?: boolean;
}

const {
  events,
  repoOwner,
  repoName,
  filtered = false,
}: Props = $props();
```

Add helpers:

```ts
function isCompactEvent(eventType: string): boolean {
  return eventType === "commit"
    || eventType === "force_push"
    || eventType === "cross_referenced"
    || eventType === "renamed_title"
    || eventType === "base_ref_changed";
}

function shortCommit(summary: string): string {
  return summary.length > 7 ? summary.slice(0, 7) : summary;
}

function commitTitle(body: string): string {
  return body.split(/\r?\n/, 1)[0] ?? "";
}

function systemEventLabel(eventType: string): string {
  switch (eventType) {
    case "cross_referenced": return "Referenced";
    case "renamed_title": return "Title changed";
    case "base_ref_changed": return "Base changed";
    case "force_push": return "Force-pushed";
    default: return typeLabels[eventType] ?? eventType;
  }
}

function parseMetadata(event: PREvent | IssueEvent): Record<string, unknown> {
  if (!event.MetadataJSON) return {};
  try {
    const parsed = JSON.parse(event.MetadataJSON) as Record<string, unknown>;
    return parsed;
  } catch {
    return {};
  }
}
```

Render compact events inside the existing `{#each}`:

```svelte
{#if isCompactEvent(event.EventType)}
  {@const metadata = parseMetadata(event)}
  <div class="event-card event-card--compact">
    <div class="event-header event-header--compact">
      <span
        class="event-type"
        style="color: {typeColors[event.EventType] ?? 'var(--text-muted)'}"
      >
        {systemEventLabel(event.EventType)}
      </span>
      {#if event.Author}
        <span class="event-author">{event.Author}</span>
      {/if}
      <span class="event-time">{timeAgo(event.CreatedAt)}</span>
      {#if event.EventType === "commit"}
        <span class="commit-sha">{shortCommit(event.Summary)}</span>
        <span class="commit-title">{commitTitle(event.Body)}</span>
      {:else if event.EventType === "cross_referenced"}
        <a
          class="system-event-link"
          href={String(metadata.source_url ?? "")}
          target="_blank"
          rel="noopener noreferrer"
        >{String(metadata.source_title ?? event.Summary)}</a>
      {:else}
        <span class="system-event-summary">{event.Summary}</span>
      {/if}
    </div>
  </div>
{:else}
  <!-- existing full card body -->
{/if}
```

Add compact CSS:

```css
.event-card--compact {
  padding: 7px 10px;
}

.event-header--compact {
  min-width: 0;
  flex-wrap: nowrap;
}

.commit-sha {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text-secondary);
}

.commit-title,
.system-event-summary,
.system-event-link {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.commit-title {
  flex: 1;
  color: var(--text-primary);
}
```

Set the empty copy:

```svelte
{#if events.length === 0}
  <p class="empty">{filtered ? "No activity matches the current filters" : "No activity yet"}</p>
```

- [ ] **Step 4: Run rendering tests and Svelte autofixer**

Run:

```bash
cd frontend && bunx vitest run ../packages/ui/src/components/detail/EventTimeline.test.ts
npx @sveltejs/mcp@0.1.22 svelte-autofixer packages/ui/src/components/detail/EventTimeline.svelte --svelte-version 5
```

Expected: PASS from Vitest and no required Svelte autofixer changes.

---

## Task 7: Wire Filter Into Pull Detail

**Files:**
- Modify: `packages/ui/src/components/detail/PullDetail.svelte`

- [ ] **Step 1: Add filter state and derived filtered events**

In `PullDetail.svelte`, import the filter component and helpers:

```ts
import PRTimelineFilter from "./PRTimelineFilter.svelte";
import {
  filterPREvents,
  loadPRTimelineFilter,
  savePRTimelineFilter,
  activePRTimelineFilterCount,
  type PRTimelineFilterState,
} from "./prTimelineFilter.js";
```

Add state near the other local `$state` fields:

```ts
let timelineFilter = $state<PRTimelineFilterState>(loadPRTimelineFilter());

const filteredTimelineEvents = $derived.by(() => {
  const events = detailStore.getDetail()?.events ?? [];
  return filterPREvents(events, timelineFilter);
});

const hasActiveTimelineFilters = $derived(
  activePRTimelineFilterCount(timelineFilter) > 0,
);

function updateTimelineFilter(next: PRTimelineFilterState): void {
  timelineFilter = next;
  savePRTimelineFilter(next);
}
```

- [ ] **Step 2: Render the filter above `EventTimeline`**

Replace the Activity section header and timeline call:

```svelte
<div class="section">
  <div class="section-title-row">
    <h3 class="section-title">Activity</h3>
    {#if detailStore.getDetailLoaded()}
      <PRTimelineFilter
        filter={timelineFilter}
        onChange={updateTimelineFilter}
      />
    {/if}
  </div>
  {#if detailStore.getDetailLoaded()}
    <EventTimeline
      events={filteredTimelineEvents}
      repoOwner={owner}
      repoName={name}
      filtered={hasActiveTimelineFilters}
    />
  {:else if detailStore.isDetailSyncing()}
    <div class="loading-placeholder">
      <svg class="sync-spinner" width="14" height="14" viewBox="0 0 16 16" fill="none">
        <circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="2" stroke-dasharray="28" stroke-dashoffset="8" stroke-linecap="round"/>
      </svg>
      Loading discussion...
    </div>
  {:else}
    <div class="loading-placeholder">Detail not yet loaded</div>
  {/if}
</div>
```

Add scoped CSS for the title/filter row:

```css
.section-title-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
}
```

- [ ] **Step 3: Run Svelte autofixer**

Run:

```bash
npx @sveltejs/mcp@0.1.22 svelte-autofixer packages/ui/src/components/detail/PullDetail.svelte --svelte-version 5
```

Expected: no required Svelte autofixer changes.

---

## Task 8: Verification And API Artifacts

**Files:**
- Modify generated files only if Huma/OpenAPI output changes unexpectedly.

- [ ] **Step 1: Run focused Go tests**

Run:

```bash
go test ./internal/github -run 'TestListPullRequestTimelineEvents|TestNormalizeTimelineEvent|TestSyncStoresPullRequestTimelineEvents|TestSyncIgnoresPullRequestTimelineFetchFailures|TestSyncStoresForcePushEvent' -shuffle=on
```

Expected: PASS.

- [ ] **Step 2: Run focused frontend tests**

Run:

```bash
cd frontend && bunx vitest run \
  ../packages/ui/src/components/detail/prTimelineFilter.test.ts \
  ../packages/ui/src/components/detail/PRTimelineFilter.test.ts \
  ../packages/ui/src/components/detail/EventTimeline.test.ts
```

Expected: PASS.

- [ ] **Step 3: Run frontend checks**

Run:

```bash
make frontend-check
```

Expected: PASS.

- [ ] **Step 4: Run full relevant Go tests**

Run:

```bash
go test ./internal/github ./internal/server ./internal/db -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Build frontend**

Run:

```bash
make frontend
```

Expected: PASS and `internal/web/dist/` refreshed.

- [ ] **Step 6: Inspect git diff**

Run:

```bash
git status --short
git diff --stat
```

Expected: only planned backend/frontend files and generated frontend dist files if `make frontend` refreshed embedded assets.

- [ ] **Step 7: Commit implementation**

Run:

```bash
git add internal/github packages/ui/src/components/detail internal/web/dist
git commit -m "feat: filter PR timeline activity" -m "Add local PR detail feed filters and sync additional GitHub timeline events so maintainers can focus on human messages while retaining commit and system-event context."
```

Expected: commit succeeds through hooks. Do not push.
