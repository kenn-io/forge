# Disabled Repository Feature Sync Cooldown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Classify repository-disabled issue and merge-request responses explicitly, stop the affected background work immediately, and probe that repository scope at most once every 24 hours.

**Architecture:** Add a provider-neutral `repository_feature_disabled` error with stable issue and merge-request capability names. GitHub translates only definitive 410 responses into that error. The syncer owns an in-memory, mutex-protected cooldown keyed by full repository identity and capability; all background lanes consult it, while explicit sync intent bypasses it through context.

**Tech Stack:** Go 1.25+, `github.com/google/go-github/v88`, SQLite-backed sync fixtures, `testify`.

## Global Constraints

- The cooldown is exactly 24 hours and is not persisted across process restarts.
- The key is `(provider, platform_host, owner, repo, issues|merge_requests)`; one scope, provider, host, or repository must not suppress another.
- Only definitive provider signals become `repository_feature_disabled`; ambiguous `403`, `404`, authentication, transport, and unexpected errors retain existing behavior.
- A disabled scope is not a transient partial failure and must not enter `failedRepos` or force ETag invalidation.
- Explicit user-triggered repository and global syncs bypass the cooldown.
- No HTTP API, generated client, database schema, or frontend contract changes are part of this work.
- Run every direct `go test` command with `-shuffle=on`; do not add `-count=1` or `-v`.
- Before each commit, run the repository-local `context-sync --commit` workflow and the mandatory commit skill. Never amend and never bypass hooks.

---

### Task 1: Add the typed platform category and definitive GitHub classification

**Files:**
- Modify: `internal/platform/errors.go`
- Create: `internal/platform/errors_test.go`
- Modify: `internal/github/pages.go`
- Modify: `internal/github/pages_test.go`

**Interfaces:**
- Consumes: `platform.Error`, `githubStatusCode(error) int`, and `*github.ErrorResponse`.
- Produces: `platform.ErrRepositoryFeatureDisabled`, `platform.ErrCodeRepositoryFeatureDisabled`, `platform.RepositoryFeatureIssues`, `platform.RepositoryFeatureMergeRequests`, `platform.RepositoryFeatureDisabled(kind, host, capability, cause)`, and `githubRepositoryFeatureDisabled(host, capability, err)`.

- [ ] **Step 1: Write the failing platform error test**

Create `internal/platform/errors_test.go` with a table that protects sentinel matching, full provider identity, capability, and cause preservation:

```go
package platform

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryFeatureDisabled(t *testing.T) {
	cause := errors.New("provider disabled issues")
	err := RepositoryFeatureDisabled(
		KindGitHub,
		"github.example.com",
		RepositoryFeatureIssues,
		cause,
	)

	require.ErrorIs(t, err, ErrRepositoryFeatureDisabled)
	require.ErrorIs(t, err, cause)
	var platformErr *Error
	require.ErrorAs(t, err, &platformErr)
	assert := assert.New(t)
	assert.Equal(ErrCodeRepositoryFeatureDisabled, platformErr.Code)
	assert.Equal(KindGitHub, platformErr.Provider)
	assert.Equal("github.example.com", platformErr.PlatformHost)
	assert.Equal(RepositoryFeatureIssues, platformErr.Capability)
}
```

- [ ] **Step 2: Run the platform test and verify RED**

Run:

```bash
go test ./internal/platform -run TestRepositoryFeatureDisabled -shuffle=on
```

Expected: compile failure because the new constants, sentinel, and constructor do not exist.

- [ ] **Step 3: Implement the platform category**

Add the category and stable capability names to `internal/platform/errors.go`:

```go
const (
	RepositoryFeatureIssues        = "issues"
	RepositoryFeatureMergeRequests = "merge_requests"
)

const (
	ErrCodeUnsupportedCapability    PlatformErrorCode = "unsupported_capability"
	ErrCodeRepositoryFeatureDisabled PlatformErrorCode = "repository_feature_disabled"
)
```

Keep the existing error-code constants in the same block, add the sentinel beside the other sentinels, and add the constructor beside `UnsupportedCapability`:

```go
var ErrRepositoryFeatureDisabled = &Error{Code: ErrCodeRepositoryFeatureDisabled}

func RepositoryFeatureDisabled(kind Kind, host, capability string, err error) error {
	return &Error{
		Code:         ErrCodeRepositoryFeatureDisabled,
		Provider:     kind,
		PlatformHost: host,
		Capability:   capability,
		Err:          err,
	}
}
```

- [ ] **Step 4: Run the platform test and verify GREEN**

Run:

```bash
go test ./internal/platform -run TestRepositoryFeatureDisabled -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Write the failing GitHub classification test**

Add this table to `internal/github/pages_test.go`:

```go
func TestGitHubRepositoryFeatureDisabled(t *testing.T) {
	tests := []struct {
		name       string
		capability string
		status     int
		message    string
		want       bool
	}{
		{"issues disabled", platform.RepositoryFeatureIssues, http.StatusGone, "Issues are disabled for this repo", true},
		{"pull requests disabled", platform.RepositoryFeatureMergeRequests, http.StatusGone, "Pull Requests are disabled for this repo", true},
		{"unrelated gone", platform.RepositoryFeatureIssues, http.StatusGone, "Resource is gone", false},
		{"ambiguous forbidden", platform.RepositoryFeatureIssues, http.StatusForbidden, "Issues are disabled for this repo", false},
		{"ambiguous not found", platform.RepositoryFeatureIssues, http.StatusNotFound, "Issues are disabled for this repo", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &gh.ErrorResponse{
				Response: &http.Response{StatusCode: tt.status},
				Message:  tt.message,
			}
			classified := githubRepositoryFeatureDisabled("github.example.com", tt.capability, err)
			if !tt.want {
				assert.NoError(t, classified)
				return
			}
			require.ErrorIs(t, classified, platform.ErrRepositoryFeatureDisabled)
			var platformErr *platform.Error
			require.ErrorAs(t, classified, &platformErr)
			assert.Equal(t, tt.capability, platformErr.Capability)
			assert.Equal(t, "github.example.com", platformErr.PlatformHost)
		})
	}
}
```

Add the missing `gh "github.com/google/go-github/v88/github"` import.

- [ ] **Step 6: Run the GitHub classification test and verify RED**

Run:

```bash
go test ./internal/github -run TestGitHubRepositoryFeatureDisabled -shuffle=on
```

Expected: compile failure because `githubRepositoryFeatureDisabled` does not exist.

- [ ] **Step 7: Implement definitive GitHub classification at list and lookup boundaries**

Add this helper to `internal/github/pages.go`:

```go
func githubRepositoryFeatureDisabled(host, capability string, err error) error {
	var responseErr *gh.ErrorResponse
	if !errors.As(err, &responseErr) || responseErr.Response == nil ||
		responseErr.Response.StatusCode != http.StatusGone {
		return nil
	}

	message := strings.ToLower(responseErr.Message)
	var phrase string
	switch capability {
	case platform.RepositoryFeatureIssues:
		phrase = "issues are disabled"
	case platform.RepositoryFeatureMergeRequests:
		phrase = "pull requests are disabled"
	default:
		return nil
	}
	if !strings.Contains(message, phrase) {
		return nil
	}
	return platform.RepositoryFeatureDisabled(
		platform.KindGitHub, host, capability, err,
	)
}
```

Call it before generic transport/lookup mapping in both single-item classifiers:

```go
if disabledErr := githubRepositoryFeatureDisabled(
	p.host, platform.RepositoryFeatureIssues, err,
); disabledErr != nil {
	return "", nil, disabledErr
}
```

Use `RepositoryFeatureMergeRequests` in `classifyMergeRequestLookup`. Also wrap the raw list calls in `ListOpenGitHubIssues` and `ListOpenMergeRequests`:

```go
if err != nil {
	if disabledErr := githubRepositoryFeatureDisabled(
		p.host, platform.RepositoryFeatureIssues, err,
	); disabledErr != nil {
		return nil, disabledErr
	}
	return nil, err
}
```

Use `RepositoryFeatureMergeRequests` in the merge-request list path. Do not classify status alone and do not change 403/404 behavior.

- [ ] **Step 8: Run the focused platform and GitHub tests**

Run:

```bash
go test ./internal/platform ./internal/github -run 'TestRepositoryFeatureDisabled|TestGitHubRepositoryFeatureDisabled|TestGitHubLiveGetMapsLookupOutcomes' -shuffle=on
```

Expected: PASS.

- [ ] **Step 9: Context-sync and commit Task 1**

Run the `context-sync` skill with `--commit`, then the mandatory commit skill. Stage only the four Task 1 files and create:

```text
feat: classify repository-disabled provider features

Definitive disabled-feature responses need a stable category so background
scheduling can distinguish them from transient provider failures without
branching on provider prose.
```

---

### Task 2: Stop index work and apply the scope-specific 24-hour cooldown

**Files:**
- Create: `internal/github/feature_cooldown.go`
- Create: `internal/github/feature_cooldown_test.go`
- Modify: `internal/github/sync.go`
- Modify: `internal/github/sync_test.go`

**Interfaces:**
- Consumes: Task 1's `platform.ErrRepositoryFeatureDisabled`, stable feature names, and `platform.Error.Capability`.
- Produces: `repositoryFeatureCooldowns`, `withRepositoryFeatureCooldownBypass(context.Context)`, `(*Syncer).repositoryFeatureDue`, `(*Syncer).recordRepositoryFeatureDisabled`, and `(*Syncer).clearRepoFailedScope`.

- [ ] **Step 1: Write the failing cooldown behavior test**

Create `internal/github/feature_cooldown_test.go` with an observable scheduling test rather than a map-only test:

```go
package github

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v88/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/platform"
)

func TestDisabledIssueScopeUsesDailyBackgroundProbeAndManualBypass(t *testing.T) {
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "acme", Name: "widget"}
	client := &partialFailureMock{}
	client.openPRs = nil
	client.openIssues = nil
	client.listOpenIssuesErr = platform.RepositoryFeatureDisabled(
		platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
		assert.AnError,
	)
	var issueListCalls atomic.Int32
	client.listOpenIssuesFn = func(context.Context, string, string) ([]*gh.Issue, error) {
		issueListCalls.Add(1)
		return nil, client.listOpenIssuesErr
	}

	syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, []RepoRef{repo}, time.Minute, nil, nil)
	syncer.now = func() time.Time { return now }

	syncer.RunOnce(t.Context())
	syncer.RunOnce(t.Context())
	assert.Equal(t, int32(1), issueListCalls.Load())
	_, failed := syncer.failedRepos.Load(repoFailKey(repo))
	assert.False(t, failed)

	require.NoError(t, syncer.SyncRepoOnProvider(t.Context(), platform.KindGitHub, "github.com", "acme", "widget"))
	assert.Equal(t, int32(2), issueListCalls.Load())

	now = now.Add(24*time.Hour - time.Second)
	syncer.RunOnce(t.Context())
	assert.Equal(t, int32(2), issueListCalls.Load())

	now = now.Add(time.Second)
	syncer.RunOnce(t.Context())
	assert.Equal(t, int32(3), issueListCalls.Load())

	client.listOpenIssuesErr = nil
	require.NoError(t, syncer.SyncRepoOnProvider(t.Context(), platform.KindGitHub, "github.com", "acme", "widget"))
	assert.Equal(t, int32(4), issueListCalls.Load())
	client.listOpenIssuesErr = platform.RepositoryFeatureDisabled(
		platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
		assert.AnError,
	)
	syncer.RunOnce(t.Context())
	assert.Equal(t, int32(5), issueListCalls.Load(), "successful manual probe must clear the cooldown")
}
```

Add this field to `partialFailureMock`:

```go
listOpenIssuesFn func(context.Context, string, string) ([]*gh.Issue, error)
```

Then make `partialFailureMock.ListOpenIssues` consult it before existing error/cache behavior:

```go
func (m *partialFailureMock) ListOpenIssues(ctx context.Context, owner, repo string) ([]*gh.Issue, error) {
	if m.listOpenIssuesFn != nil {
		return m.listOpenIssuesFn(ctx, owner, repo)
	}
	if m.listOpenIssuesErr != nil {
		return nil, m.listOpenIssuesErr
	}
	if m.issuesCached {
		return nil, notModifiedErr()
	}
	m.issuesCached = true
	return m.openIssues, nil
}
```

Keep the function on the existing mock rather than introducing a second full `Client` fake.

- [ ] **Step 2: Run the cooldown test and verify RED**

Run:

```bash
go test ./internal/github -run TestDisabledIssueScopeUsesDailyBackgroundProbeAndManualBypass -shuffle=on
```

Expected: FAIL because background cycles still call the issue list and still mark the repository failed.

- [ ] **Step 3: Implement the concurrency-safe cooldown state**

Create `internal/github/feature_cooldown.go`:

```go
package github

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/platform"
)

const repositoryFeatureProbeInterval = 24 * time.Hour

type repositoryFeatureCooldownKey struct {
	platform platform.Kind
	host     string
	repoPath string
	feature  string
}

type repositoryFeatureCooldowns struct {
	mu        sync.Mutex
	nextProbe map[repositoryFeatureCooldownKey]time.Time
}

type repositoryFeatureCooldownBypassKey struct{}

func withRepositoryFeatureCooldownBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, repositoryFeatureCooldownBypassKey{}, true)
}

func repositoryFeatureCooldownBypassed(ctx context.Context) bool {
	bypass, _ := ctx.Value(repositoryFeatureCooldownBypassKey{}).(bool)
	return bypass
}

func repositoryFeatureKey(repo RepoRef, feature string) repositoryFeatureCooldownKey {
	ref := platformRepoRef(repo)
	return repositoryFeatureCooldownKey{
		platform: ref.Platform,
		host:     ref.Host,
		repoPath: ref.RepoPath,
		feature:  feature,
	}
}

func (c *repositoryFeatureCooldowns) due(repo RepoRef, feature string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	nextProbe, ok := c.nextProbe[repositoryFeatureKey(repo, feature)]
	return !ok || !nextProbe.After(now)
}

func (c *repositoryFeatureCooldowns) deferUntil(repo RepoRef, feature string, nextProbe time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nextProbe == nil {
		c.nextProbe = make(map[repositoryFeatureCooldownKey]time.Time)
	}
	c.nextProbe[repositoryFeatureKey(repo, feature)] = nextProbe
}

func (c *repositoryFeatureCooldowns) clear(repo RepoRef, feature string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.nextProbe, repositoryFeatureKey(repo, feature))
}

func (s *Syncer) repositoryFeatureDue(ctx context.Context, repo RepoRef, feature string) bool {
	if repositoryFeatureCooldownBypassed(ctx) {
		return true
	}
	return s.featureCooldowns.due(repo, feature, s.now().UTC())
}

func (s *Syncer) recordRepositoryFeatureDisabled(repo RepoRef, feature string, err error) bool {
	var platformErr *platform.Error
	if !errors.As(err, &platformErr) ||
		platformErr.Code != platform.ErrCodeRepositoryFeatureDisabled ||
		platformErr.Capability != feature {
		return false
	}
	nextProbe := s.now().UTC().Add(repositoryFeatureProbeInterval)
	s.featureCooldowns.deferUntil(repo, feature, nextProbe)
	slog.Info("repository feature disabled; deferring background sync",
		"platform", repoPlatform(repo),
		"host", repoHost(repo),
		"repo", platformRepoRef(repo).RepoPath,
		"feature", feature,
		"next_probe_at", nextProbe,
	)
	return true
}
```

Add `featureCooldowns repositoryFeatureCooldowns` to `Syncer`. Use the existing injected `s.now` clock; do not call `time.Now` in cooldown policy.

- [ ] **Step 4: Add explicit manual bypass and scoped failed-repo clearing**

In `runOnce`, set the intent before `WithSyncBudget`:

```go
if bypassNextSyncAfter {
	ctx = withRepositoryFeatureCooldownBypass(ctx)
}
ctx = WithSyncBudget(ctx)
```

In `SyncRepoOnProvider`, pass the same intent into the existing direct sync:

```go
return s.syncRepo(withRepositoryFeatureCooldownBypass(ctx), repo)
```

Add this compare-and-swap helper beside `clearRepoFailed`:

```go
func (s *Syncer) clearRepoFailedScope(repo RepoRef, scope failScope) {
	key := repoFailKey(repo)
	for {
		value, ok := s.failedRepos.Load(key)
		if !ok {
			return
		}
		remaining := value.(failScope) &^ scope
		if remaining == 0 {
			if s.failedRepos.CompareAndDelete(key, value) {
				return
			}
			continue
		}
		if s.failedRepos.CompareAndSwap(key, value, remaining) {
			return
		}
	}
}
```

- [ ] **Step 5: Gate index scopes and distinguish disabled from failed**

In `indexSyncRepo`, add attempted and disabled bitmasks beside `failedScope`:

```go
var attemptedScope failScope
var disabledScope failScope
```

Change each capability gate to include the cooldown and mark it attempted:

```go
if caps.ReadMergeRequests && s.repositoryFeatureDue(ctx, repo, platform.RepositoryFeatureMergeRequests) {
	attemptedScope |= failMR
}
```

Place the existing merge-request body inside that branch. Use the equivalent issue branch with `RepositoryFeatureIssues` and `failIssues`.

At every list, REST-list processing, and GraphQL processing error boundary, classify before setting `failedScope`:

```go
if s.recordRepositoryFeatureDisabled(repo, platform.RepositoryFeatureIssues, err) {
	disabledScope |= failIssues
	s.clearRepoFailedScope(repo, failIssues)
} else {
	failedScope |= failIssues
}
```

Use the merge-request feature and bit in the MR path. Do not emit the existing error-level log for the disabled branch.

After each attempted scope finishes without failure or disablement, clear stale cooldown state:

```go
if attemptedScope&failIssues != 0 &&
	failedScope&failIssues == 0 && disabledScope&failIssues == 0 {
	s.featureCooldowns.clear(repo, platform.RepositoryFeatureIssues)
}
if attemptedScope&failMR != 0 &&
	failedScope&failMR == 0 && disabledScope&failMR == 0 {
	s.featureCooldowns.clear(repo, platform.RepositoryFeatureMergeRequests)
}
```

Only `failedScope` contributes to `PartialSyncError`. Add `disabledScope` checks to the unchanged-list comment refresh conditions so a just-disabled scope cannot enqueue comment work.

- [ ] **Step 6: Abort item loops on the first typed disabled error**

In `syncMergeRequestsFromList`, `syncIssuesFromList`, `syncPlatformIssuesFromList`, `doSyncRepoGraphQL`, and `doSyncRepoGraphQLIssues`, return the typed error immediately before generic per-item logging or aggregation:

```go
if errors.Is(err, platform.ErrRepositoryFeatureDisabled) {
	return err
}
```

Apply this check to both open-item processing and closure-detection loops. Preserve the current aggregation behavior for every other error.

- [ ] **Step 7: Add a closure-burst regression test**

Add `TestDisabledIssuesStopClosureDetectionAfterFirstLookup` to `internal/github/sync_test.go`. Seed two open issues in the same repository, then use an empty open list and a `getIssueFn` that returns a GitHub 410 disabled response while counting calls:

```go
var getIssueCalls atomic.Int32
client.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) {
	getIssueCalls.Add(1)
	return nil, &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusGone},
		Message:  "Issues are disabled for this repo",
	}
}

syncer.RunOnce(t.Context())

assert.Equal(t, int32(1), getIssueCalls.Load())
_, failed := syncer.failedRepos.Load(repoFailKey(repo))
assert.False(t, failed)
```

Use the existing issue fixture builders and database APIs in `sync_test.go` to seed the rows through an initial successful sync; do not insert raw SQL.

- [ ] **Step 8: Run the focused index tests**

Run:

```bash
go test ./internal/github -run 'TestDisabledIssueScopeUsesDailyBackgroundProbeAndManualBypass|TestDisabledIssuesStopClosureDetectionAfterFirstLookup|TestSyncerSyncOpenIssueFailureMarksRepoFailed|TestSyncerClosedIssueFailureMarksRepoFailed|TestSyncerMRListFailureMarksRepoFailed' -shuffle=on
```

Expected: PASS, including the existing transient partial-failure behavior.

- [ ] **Step 9: Context-sync and commit Task 2**

Run the `context-sync` skill with `--commit`, then the mandatory commit skill. Stage only Task 2 files and create:

```text
fix: defer disabled repository sync scopes

Repository-disabled features are stable scheduling conditions, not transient
partial failures. Stop the affected scope immediately and reserve normal
background probes for one daily retry while preserving manual refresh.
```

---

### Task 3: Gate secondary background lanes and capture the invariant

**Files:**
- Modify: `internal/github/sync.go`
- Modify: `internal/github/sync_test.go`
- Modify: `context/platform-sync-invariants.md`
- Modify: `context/retries-and-backoffs.md`

**Interfaces:**
- Consumes: Task 2's context intent, `repositoryFeatureDue`, and `recordRepositoryFeatureDisabled`.
- Produces: cooldown-aware watched-MR sync, detail drain, and queued comment drain; durable provider-neutral scheduling guidance.

- [ ] **Step 1: Write failing secondary-lane tests**

Add these focused tests to `internal/github/sync_test.go`:

```go
func TestDisabledIssueCooldownSkipsDetailDrain(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "acme", Name: "widget"}
	repoID, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID: repoID, PlatformID: 1001, Number: 1,
		URL: "https://github.com/acme/widget/issues/1", Title: "needs detail",
		Author: "ada", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	client := &conditionalIssueTrackingClient{}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil, []RepoRef{repo},
		time.Minute, nil, testBudget(1000),
	)
	syncer.now = func() time.Time { return now }
	require.True(syncer.recordRepositoryFeatureDisabled(
		repo, platform.RepositoryFeatureIssues,
		platform.RepositoryFeatureDisabled(
			platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues, assert.AnError,
		),
	))

	syncer.drainDetailQueue(ctx, map[string]bool{"github.com": true})

	assert.Zero(int(client.conditionalCalls.Load()))
}

func TestDisabledMergeRequestCooldownSkipsWatchedSync(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "acme", Name: "widget"}
	client := &detailTrackingClient{}
	client.singlePR = buildOpenPR(7, now)
	client.comments = []*gh.IssueComment{}
	client.reviews = []*gh.PullRequestReview{}
	client.commits = []*gh.RepositoryCommit{}
	syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, []RepoRef{repo}, time.Minute, nil, nil)
	syncer.now = func() time.Time { return now }
	syncer.SetWatchedMRs([]WatchedMR{{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget", Number: 7,
	}})
	require.True(syncer.recordRepositoryFeatureDisabled(
		repo, platform.RepositoryFeatureMergeRequests,
		platform.RepositoryFeatureDisabled(
			platform.KindGitHub, "github.com", platform.RepositoryFeatureMergeRequests, assert.AnError,
		),
	))

	syncer.syncWatchedMRs(t.Context())

	assert.Zero(int(client.getPRCalls.Load()))
}

func TestDisabledIssueCooldownDoesNotCrossProviderHostOrScope(t *testing.T) {
	assert := assert.New(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	syncer := &Syncer{now: func() time.Time { return now }}
	disabled := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	assert.True(syncer.recordRepositoryFeatureDisabled(
		disabled, platform.RepositoryFeatureIssues,
		platform.RepositoryFeatureDisabled(
			platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues, assert.AnError,
		),
	))

	assert.False(syncer.repositoryFeatureDue(t.Context(), disabled, platform.RepositoryFeatureIssues))
	assert.True(syncer.repositoryFeatureDue(t.Context(), disabled, platform.RepositoryFeatureMergeRequests))
	assert.True(syncer.repositoryFeatureDue(t.Context(), RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "ghe.example.com",
		Owner: "acme", Name: "widget",
	}, platform.RepositoryFeatureIssues))
	assert.True(syncer.repositoryFeatureDue(t.Context(), RepoRef{
		Platform: platform.KindGitLab, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}, platform.RepositoryFeatureIssues))
	assert.True(syncer.repositoryFeatureDue(t.Context(), RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "other-widget",
	}, platform.RepositoryFeatureIssues))
}
```

These tests reuse the existing detail clients and assert upstream eligibility or call counts. They do not inspect the cooldown map.

- [ ] **Step 2: Run the secondary-lane tests and verify RED**

Run:

```bash
go test ./internal/github -run 'TestDisabledIssueCooldownSkipsDetailDrain|TestDisabledMergeRequestCooldownSkipsWatchedSync|TestDisabledIssueCooldownDoesNotCrossProviderHostOrScope' -shuffle=on
```

Expected: FAIL because the lanes do not consult the cooldown.

- [ ] **Step 3: Gate watched-MR fast sync**

Before rate-limit checks in the `syncWatchedMRs` item loop, construct the repository reference and skip cooled-down MRs:

```go
repo := RepoRef{
	Platform:     watchedMRPlatform(mr),
	PlatformHost: watchedMRHost(mr),
	Owner:        mr.Owner,
	Name:         mr.Name,
}
if !s.repositoryFeatureDue(ctx, repo, platform.RepositoryFeatureMergeRequests) {
	slog.Debug("skipping fast-sync for disabled repository feature",
		"platform", repoPlatform(repo),
		"host", repoHost(repo),
		"repo", platformRepoRef(repo).RepoPath,
		"feature", platform.RepositoryFeatureMergeRequests,
	)
	continue
}
```

When `syncMRWithWatchedRef` returns an error, call `recordRepositoryFeatureDisabled` before the existing warning. If it returns true, continue without warning.

- [ ] **Step 4: Gate detail drain and learn from typed detail failures**

After constructing the tracked `repo` in `drainDetailQueue`, derive the feature and skip before clone or budget-consuming work:

```go
feature := platform.RepositoryFeatureIssues
if qi.Type == QueueItemPR {
	feature = platform.RepositoryFeatureMergeRequests
}
if !s.repositoryFeatureDue(ctx, repo, feature) {
	continue
}
```

In the detail-fetch error branch, call `recordRepositoryFeatureDisabled(repo, feature, err)` before the existing warning and suppress the warning when it returns true. The context bypass set by `runOnce` makes a manual global sync eligible automatically.

- [ ] **Step 5: Gate queued and unchanged-list comment refresh**

In both loops in `drainPendingCommentSyncs`, check `repositoryFeatureDue` after host eligibility and before client resolution:

```go
if !s.repositoryFeatureDue(ctx, item.repo, platform.RepositoryFeatureMergeRequests) {
	continue
}
```

Use `RepositoryFeatureIssues` in the issue loop. Also guard `refreshRepoPRComments` and `refreshRepoIssueComments` calls in `indexSyncRepo` with `disabledScope` and `repositoryFeatureDue`; this prevents cached unchanged lists from starting comment work after another lane establishes the cooldown.

- [ ] **Step 6: Run the complete affected Go test package**

Run:

```bash
go test ./internal/platform ./internal/github -shuffle=on
```

Expected: PASS.

- [ ] **Step 7: Update durable context**

Add one terse bullet under `context/platform-sync-invariants.md` → `Sync Capabilities`:

```markdown
- A definitive repository-disabled issue or merge-request response is a typed
  `repository_feature_disabled` scheduling condition, scoped by full repository
  identity and feature; it must not become transient partial-failure recovery.
```

Add one terse paragraph under `context/retries-and-backoffs.md` → `Scheduling cadence`:

```markdown
Repository-disabled issue and merge-request scopes use a 24-hour in-memory
background probe gate. Explicit sync bypasses it; this is cadence policy, not a
transient retry or rate-limit backoff.
```

During context-sync, add symbol anchors for the final helper names if the context guide requires them.

- [ ] **Step 8: Run final verification**

Run:

```bash
git diff --check
go test ./internal/platform ./internal/github -shuffle=on
make test-short
```

Expected: all commands PASS. Review `git diff --stat` and `git diff HEAD` to confirm there are no API, generated, schema, or frontend changes.

- [ ] **Step 9: Context-sync and commit Task 3**

Run the `context-sync` skill with `--commit`, then the mandatory commit skill. Stage only Task 3 files and any clear context-sync edits, then create:

```text
fix: honor disabled feature cooldown across background sync

Index suppression alone still leaves detail, comment, and watched-item lanes
able to spend provider budget. Apply the same full-identity scheduling policy
to every background entry point and record the invariant for future changes.
```
