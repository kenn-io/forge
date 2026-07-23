# Cross-Provider Disabled Repository Feature Classification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make GitLab, Gitea, and Forgejo return `repository_feature_disabled` for issue or merge-request reads only when a candidate endpoint error is confirmed by authoritative repository metadata.

**Architecture:** Add tri-state issue/MR feature state to the transient provider-neutral repository model, then keep classification inside each provider adapter. Successful feature reads do not fetch metadata; only HTTP 403, 404, or 410 failures trigger one repository lookup, and inconclusive lookups preserve the original mapped operation error. Existing sync and archive cooldown code consumes the typed error unchanged.

**Tech Stack:** Go 1.25, `gitlab.com/gitlab-org/api/client-go/v2`, Gitea SDK, Forgejo SDK, `net/http/httptest`, Testify.

## Global Constraints

- Do not add a success-path repository metadata preflight.
- Trigger confirmation only for HTTP 403, 404, or 410 equivalents.
- Fetch repository metadata once after a candidate operation failure.
- Return `repository_feature_disabled` only for a definitively disabled matching feature.
- Preserve the original mapped operation error when metadata reports enabled/unknown, normalization fails, or metadata lookup fails.
- Keep GitHub's existing HTTP 410 classifier unchanged.
- Do not persist or cache repository feature state.
- Do not expand classification to mutation paths.
- Do not add compatibility aliases, legacy fallbacks, or dual read/write behavior.
- Classify Gitea-like optional timeline failures before the existing 404 suppression.
- Never bypass hooks with `--no-verify`; run `scripts/context-sync --check` before every commit.

---

### Task 1: Add transient tri-state repository feature state

**Files:**
- Modify: `internal/platform/types.go`
- Modify: `internal/platform/types_test.go`
- Modify: `internal/platform/gitealike/types.go`
- Modify: `internal/platform/gitealike/normalize.go`
- Modify: `internal/platform/gitealike/normalize_test.go`
- Modify: `internal/platform/gitlab/normalize.go`
- Modify: `internal/platform/gitlab/normalize_test.go`

**Interfaces:**
- Produces: `platform.RepositoryFeatures{IssuesEnabled, MergeRequestsEnabled *bool}`.
- Produces: `func (Repository) FeatureEnabled(feature string) (enabled bool, known bool)`.
- Consumed by: provider-local candidate-error classifiers in Tasks 2 and 4.

- [ ] **Step 1: Write failing platform tri-state lookup tests**

Add to `internal/platform/types_test.go`:

```go
func TestRepositoryFeatureEnabled(t *testing.T) {
	t.Parallel()
	trueValue, falseValue := true, false
	repo := Repository{Features: RepositoryFeatures{
		IssuesEnabled:        &falseValue,
		MergeRequestsEnabled: &trueValue,
	}}

	tests := []struct {
		name    string
		feature string
		enabled bool
		known   bool
	}{
		{name: "issues disabled", feature: RepositoryFeatureIssues, known: true},
		{name: "merge requests enabled", feature: RepositoryFeatureMergeRequests, enabled: true, known: true},
		{name: "unknown feature", feature: "releases"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			enabled, known := repo.FeatureEnabled(tt.feature)
			assert.Equal(t, tt.enabled, enabled)
			assert.Equal(t, tt.known, known)
		})
	}

	unknown := Repository{}
	_, known := unknown.FeatureEnabled(RepositoryFeatureIssues)
	assert.False(t, known)
}
```

- [ ] **Step 2: Run the platform test and verify it fails**

Run:

```bash
go test ./internal/platform -run TestRepositoryFeatureEnabled -count=1
```

Expected: compilation fails because `Repository.Features`, `RepositoryFeatures`, and `FeatureEnabled` do not exist.

- [ ] **Step 3: Implement the transient feature model and lookup**

Add to `internal/platform/types.go` adjacent to `Repository`:

```go
type RepositoryFeatures struct {
	IssuesEnabled        *bool
	MergeRequestsEnabled *bool
}

type Repository struct {
	Ref                RepoRef
	PlatformID         int64
	PlatformExternalID string
	Description        string
	Private            bool
	Archived           bool
	Features           RepositoryFeatures
	MergeSettings      *RepositoryMergeSettings
	ViewerCanMerge     *bool
	DefaultBranch      string
	WebURL             string
	CloneURL           string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (r Repository) FeatureEnabled(feature string) (bool, bool) {
	var enabled *bool
	switch feature {
	case RepositoryFeatureIssues:
		enabled = r.Features.IssuesEnabled
	case RepositoryFeatureMergeRequests:
		enabled = r.Features.MergeRequestsEnabled
	default:
		return false, false
	}
	if enabled == nil {
		return false, false
	}
	return *enabled, true
}
```

Do not add the feature fields to DB rows, persistence conversion, API response types, or configuration.

- [ ] **Step 4: Write failing normalization tests**

In `internal/platform/gitealike/normalize_test.go`, add a test that passes explicit pointer values through `RepositoryDTO`:

```go
func TestNormalizeRepositoryPreservesFeatureState(t *testing.T) {
	t.Parallel()
	issuesEnabled, pullsEnabled := false, true
	repo, err := NormalizeRepository(platform.KindGitea, "gitea.example", RepositoryDTO{
		ID: 1, Owner: UserDTO{UserName: "owner"}, Name: "repo", FullName: "owner/repo",
		IssuesEnabled: &issuesEnabled, MergeRequestsEnabled: &pullsEnabled,
	})
	require.NoError(t, err)
	assert.Same(t, &issuesEnabled, repo.Features.IssuesEnabled)
	assert.Same(t, &pullsEnabled, repo.Features.MergeRequestsEnabled)
}
```

In `internal/platform/gitlab/normalize_test.go`, extend `TestNormalizeProjectPreservesGitLabIdentity` or add:

```go
func TestNormalizeProjectPreservesFeatureState(t *testing.T) {
	t.Parallel()
	repo, err := NormalizeProject("gitlab.example.com", &gitlab.Project{
		ID: 42, Path: "project", PathWithNamespace: "group/project",
		IssuesEnabled: false, MergeRequestsEnabled: true,
	})
	require.NoError(t, err)
	issuesEnabled, known := repo.FeatureEnabled(platform.RepositoryFeatureIssues)
	assert.True(t, known)
	assert.False(t, issuesEnabled)
	mergeRequestsEnabled, known := repo.FeatureEnabled(platform.RepositoryFeatureMergeRequests)
	assert.True(t, known)
	assert.True(t, mergeRequestsEnabled)
}
```

- [ ] **Step 5: Run normalization tests and verify they fail**

Run:

```bash
go test ./internal/platform/gitealike ./internal/platform/gitlab -run 'TestNormalize(Repository|Project)PreservesFeatureState' -count=1
```

Expected: compilation fails because the DTO and normalizers do not carry feature state.

- [ ] **Step 6: Thread feature state through shared and GitLab normalization**

Extend `gitealike.RepositoryDTO` in `internal/platform/gitealike/types.go`:

```go
IssuesEnabled        *bool
MergeRequestsEnabled *bool
```

Set the neutral field in `NormalizeRepository`:

```go
Features: platform.RepositoryFeatures{
	IssuesEnabled:        repo.IssuesEnabled,
	MergeRequestsEnabled: repo.MergeRequestsEnabled,
},
```

In `NormalizeProject`, copy the SDK booleans into stable local addresses before the return literal:

```go
issuesEnabled := p.IssuesEnabled
mergeRequestsEnabled := p.MergeRequestsEnabled
```

Then add:

```go
Features: platform.RepositoryFeatures{
	IssuesEnabled:        &issuesEnabled,
	MergeRequestsEnabled: &mergeRequestsEnabled,
},
```

- [ ] **Step 7: Run the focused tests**

Run:

```bash
go test ./internal/platform ./internal/platform/gitealike ./internal/platform/gitlab -run 'TestRepositoryFeatureEnabled|TestNormalize(Repository|Project)PreservesFeatureState' -shuffle=on -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit the neutral model**

Run `scripts/context-sync --check`, stage only the Task 1 files, and invoke `$commit` with subject:

```text
feat: expose repository feature state to adapters
```

The body must explain that tri-state state stays transient so adapters can confirm disabled endpoints without persisting or preflighting it.

---

### Task 2: Classify all shared Gitea-like issue and pull-request reads

**Files:**
- Create: `internal/platform/gitealike/feature_disabled.go`
- Create: `internal/platform/gitealike/feature_disabled_test.go`
- Modify: `internal/platform/gitealike/provider.go`
- Modify: `internal/platform/gitealike/pages.go`

**Interfaces:**
- Consumes: `Repository.FeatureEnabled(feature string) (bool, bool)` from Task 1.
- Produces: `func (p *Provider) repositoryFeatureError(ctx context.Context, ref platform.RepoRef, feature string, err error) error`.
- Preserves: `mapTransportError` for every unconfirmed failure.

- [ ] **Step 1: Write classifier decision-table tests**

Create `internal/platform/gitealike/feature_disabled_test.go` with a small transport that embeds the existing `fakeTransport` and overrides repository lookup:

```go
type featureFailureTransport struct {
	*fakeTransport
	repository    RepositoryDTO
	repositoryErr error
	repositoryCalls int
	failAt        string
	operationErr  error
}

func (t *featureFailureTransport) GetRepository(context.Context, string, string) (RepositoryDTO, error) {
	t.repositoryCalls++
	return t.repository, t.repositoryErr
}

func (t *featureFailureTransport) failure(name string) error {
	if t.failAt == name {
		return t.operationErr
	}
	return nil
}
```

Add `TestRepositoryFeatureError` with these exact expectations:

```text
403 + disabled metadata -> repository_feature_disabled
404 + disabled metadata -> repository_feature_disabled
410 + disabled metadata -> repository_feature_disabled
403 + enabled metadata -> permission_denied
404 + enabled metadata -> not_found
410 + enabled metadata -> original *HTTPError
404 + nil feature pointer -> not_found
404 + metadata lookup error -> not_found
401/429/500 -> no metadata lookup and the existing mapped/raw result
context.Canceled/context.DeadlineExceeded -> no metadata lookup and the context error
```

Count repository lookup calls in the fake. Candidate rows perform exactly one lookup; non-candidate and context rows perform zero.

The core invocation is:

```go
classified := provider.repositoryFeatureError(t.Context(), ref, tt.feature, tt.operationErr)
```

For the disabled case, assert:

```go
require.ErrorIs(t, classified, platform.ErrRepositoryFeatureDisabled)
require.ErrorIs(t, classified, tt.operationErr)
var platformErr *platform.Error
require.ErrorAs(t, classified, &platformErr)
assert.Equal(t, provider.kind, platformErr.Provider)
assert.Equal(t, provider.host, platformErr.PlatformHost)
assert.Equal(t, tt.feature, platformErr.Capability)
```

- [ ] **Step 2: Run the classifier test and verify it fails**

Run:

```bash
go test ./internal/platform/gitealike -run TestRepositoryFeatureError -count=1
```

Expected: compilation fails because `repositoryFeatureError` does not exist.

- [ ] **Step 3: Implement candidate detection and metadata confirmation**

Create `internal/platform/gitealike/feature_disabled.go`:

```go
package gitealike

import (
	"context"
	"errors"
	"net/http"

	"go.kenn.io/middleman/internal/platform"
)

func (p *Provider) repositoryFeatureError(
	ctx context.Context,
	ref platform.RepoRef,
	feature string,
	err error,
) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return p.mapError(err)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr == nil ||
		(httpErr.StatusCode != http.StatusForbidden &&
			httpErr.StatusCode != http.StatusNotFound &&
			httpErr.StatusCode != http.StatusGone) {
		return p.mapError(err)
	}

	dto, lookupErr := p.transport.GetRepository(ctx, ref.Owner, ref.Name)
	if lookupErr != nil {
		return p.mapError(err)
	}
	repository, normalizeErr := NormalizeRepository(p.kind, p.host, dto)
	if normalizeErr != nil {
		return p.mapError(err)
	}
	enabled, known := repository.FeatureEnabled(feature)
	if known && !enabled {
		return platform.RepositoryFeatureDisabled(p.kind, p.host, feature, err)
	}
	return p.mapError(err)
}
```

This helper must call `p.transport.GetRepository` directly; calling `p.GetRepository` would route lookup errors back through provider mapping and obscure that metadata failure is merely inconclusive.

- [ ] **Step 4: Write failing boundary-order tests**

Complete `featureFailureTransport` with these exact overrides:

```go
func (t *featureFailureTransport) ListOpenPullRequests(
	context.Context, platform.RepoRef, PageOptions,
) ([]PullRequestDTO, Page, error) {
	return nil, Page{}, t.failure("open_pull_requests")
}

func (t *featureFailureTransport) GetPullRequest(
	context.Context, platform.RepoRef, int,
) (PullRequestDTO, error) {
	return PullRequestDTO{}, t.failure("pull_request")
}

func (t *featureFailureTransport) ListPullRequestComments(
	context.Context, platform.RepoRef, int, PageOptions,
) ([]CommentDTO, Page, error) {
	return nil, Page{}, t.failure("pull_comments")
}

func (t *featureFailureTransport) ListPullRequestReviews(
	context.Context, platform.RepoRef, int, PageOptions,
) ([]ReviewDTO, Page, error) {
	return nil, Page{}, t.failure("pull_reviews")
}

func (t *featureFailureTransport) ListPullRequestCommits(
	context.Context, platform.RepoRef, int, PageOptions,
) ([]CommitDTO, Page, error) {
	return nil, Page{}, t.failure("pull_commits")
}

func (t *featureFailureTransport) ListIssueTimeline(
	context.Context, platform.RepoRef, int, PageOptions,
) ([]TimelineEventDTO, Page, error) {
	return nil, Page{}, t.failure("timeline")
}

func (t *featureFailureTransport) ListOpenIssues(
	context.Context, platform.RepoRef, PageOptions,
) ([]IssueDTO, Page, error) {
	return nil, Page{}, t.failure("open_issues")
}

func (t *featureFailureTransport) GetIssue(
	context.Context, platform.RepoRef, int,
) (IssueDTO, error) {
	return IssueDTO{}, t.failure("issue")
}

func (t *featureFailureTransport) ListIssueComments(
	context.Context, platform.RepoRef, int, PageOptions,
) ([]CommentDTO, Page, error) {
	return nil, Page{}, t.failure("issue_comments")
}

func (t *featureFailureTransport) ListIssuesPage(
	context.Context, platform.RepoRef, ArchiveListOptions,
) ([]IssueDTO, Page, error) {
	return nil, Page{}, t.failure("archive_issues")
}

func (t *featureFailureTransport) ListPullRequestsPage(
	context.Context, platform.RepoRef, ArchiveListOptions,
) ([]PullRequestDTO, Page, error) {
	return nil, Page{}, t.failure("archive_pull_requests")
}
```

Add a table whose invocation closures call:

```go
provider.ListOpenMergeRequests
provider.GetMergeRequest
provider.ListMergeRequestEvents // once each for comments, reviews, commits, and timeline
provider.ListOpenIssues
provider.GetIssue
provider.ListIssueEvents         // once for comments and once for timeline
provider.ListIssuesPage
provider.ListMergeRequestsPage
```

For every row, configure the matching feature flag false, use `&HTTPError{StatusCode: http.StatusNotFound}`, and assert `platform.ErrRepositoryFeatureDisabled`. Add one dedicated timeline row with metadata enabled and assert the existing 404 suppression still returns no error; this proves classification runs before suppression without removing optional-endpoint behavior.

- [ ] **Step 5: Run the boundary test and verify it fails**

Run:

```bash
go test ./internal/platform/gitealike -run 'TestRepositoryFeature(ReadBoundaries|TimelineSuppression)' -count=1
```

Expected: failures show ordinary permission/not-found mapping or optional-timeline suppression instead of disabled-feature classification.

- [ ] **Step 6: Apply classification at every shared read boundary**

In `internal/platform/gitealike/provider.go`, replace generic mapping only on issue/MR reads:

```go
return nil, p.repositoryFeatureError(ctx, ref, platform.RepositoryFeatureMergeRequests, err)
return platform.MergeRequest{}, p.repositoryFeatureError(ctx, ref, platform.RepositoryFeatureMergeRequests, err)
return nil, p.repositoryFeatureError(ctx, ref, platform.RepositoryFeatureIssues, err)
return platform.Issue{}, p.repositoryFeatureError(ctx, ref, platform.RepositoryFeatureIssues, err)
```

Use the MR feature independently for comments, reviews, commits, and MR timeline failures; use the issue feature for issue comments and issue timeline failures.

Change the timeline helper signature to carry the matching feature:

```go
func (p *Provider) listTimelineEvents(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	feature string,
) ([]TimelineEventDTO, error)
```

Map the raw timeline error before not-found suppression:

```go
if err != nil {
	err = p.repositoryFeatureError(ctx, ref, feature, err)
	if errors.Is(err, platform.ErrNotFound) {
		return nil, nil
	}
	return nil, err
}
```

Pass `platform.RepositoryFeatureMergeRequests` from `ListMergeRequestEvents` and `platform.RepositoryFeatureIssues` from `ListIssueEvents`.

In `internal/platform/gitealike/pages.go`, classify `ListIssuesPage` and `ListPullRequestsPage` transport errors with the matching feature before returning.

Do not alter release, tag, CI, label, mutation, assignee, reviewer-mutation, or merge paths.

- [ ] **Step 7: Run shared adapter tests**

Run:

```bash
go test ./internal/platform/gitealike -shuffle=on -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit the shared classifier**

Run `scripts/context-sync --check`, stage only the Task 2 files, and invoke `$commit` with subject:

```text
fix: classify disabled Gitea-like read scopes
```

The body must explain why metadata confirmation happens after candidate failures and before optional timeline suppression.

---

### Task 3: Populate Gitea and Forgejo feature flags and prove HTTP integration

**Files:**
- Modify: `internal/platform/gitea/convert.go`
- Modify: `internal/platform/gitea/convert_test.go`
- Modify: `internal/platform/gitea/client_test.go`
- Modify: `internal/platform/forgejo/convert.go`
- Modify: `internal/platform/forgejo/convert_test.go`
- Modify: `internal/platform/forgejo/client_test.go`

**Interfaces:**
- Produces: non-nil `RepositoryDTO.IssuesEnabled` and `RepositoryDTO.MergeRequestsEnabled` from each concrete SDK repository response.
- Consumes: the shared Gitea-like classifier from Task 2.

- [ ] **Step 1: Write failing conversion tests**

In `internal/platform/gitea/convert_test.go`, add a table for `(has_issues, has_pull_requests)` and assert both DTO pointers are non-nil and preserve true/false exactly:

```go
repo, err := convertRepository(&giteasdk.Repository{
	ID: 1, Name: "repo", FullName: "owner/repo",
	HasIssues: tt.issues, HasPullRequests: tt.pullRequests,
})
require.NoError(t, err)
require.NotNil(t, repo.IssuesEnabled)
require.NotNil(t, repo.MergeRequestsEnabled)
assert.Equal(t, tt.issues, *repo.IssuesEnabled)
assert.Equal(t, tt.pullRequests, *repo.MergeRequestsEnabled)
```

In `internal/platform/forgejo/convert_test.go`, use the same assertions with:

```go
repo, err := convertRepository(&forgejosdk.Repository{
	ID: 1, Name: "repo", FullName: "owner/repo",
	HasIssues: tt.issues, HasPullRequests: tt.pullRequests,
})
```

- [ ] **Step 2: Run conversion tests and verify they fail**

Run:

```bash
go test ./internal/platform/gitea ./internal/platform/forgejo -run TestConvertRepositoryPreservesFeatureState -count=1
```

Expected: assertions fail because the DTO pointers are nil.

- [ ] **Step 3: Write one failing client-level disabled-issues test per provider**

In each `client_test.go`, create an `httptest.Server` with two routes:

```go
switch r.URL.Path {
case "/api/v1/repos/owner/repo/issues":
	http.Error(w, "issues disabled", http.StatusNotFound)
case "/api/v1/repos/owner/repo":
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{
		"id":1,"name":"repo","full_name":"owner/repo",
		"owner":{"id":2,"login":"owner"},
		"has_issues":false,"has_pull_requests":true
	}`))
default:
	http.NotFound(w, r)
}
```

In the Gitea test, construct the client with host `gitea.test`, then call:

```go
_, err = client.ListOpenIssues(t.Context(), platform.RepoRef{
	Platform: platform.KindGitea, Host: "gitea.test", Owner: "owner", Name: "repo",
})
```

In the Forgejo test, use host `forgejo.test` and `platform.KindForgejo`. Assert `platform.ErrRepositoryFeatureDisabled`, the concrete provider kind and host, issue capability, and exactly one issue request plus one metadata request.

- [ ] **Step 4: Run client-level tests and verify they fail**

Run:

```bash
go test ./internal/platform/gitea ./internal/platform/forgejo -run TestClientClassifiesDisabledIssuesFromRepositoryMetadata -count=1
```

Expected: failures show ordinary not-found mapping because the SDK repository booleans are not yet copied into the shared DTO.

- [ ] **Step 5: Populate concrete repository flags**

In each `convertRepository`, create stable locals:

```go
issuesEnabled := repo.HasIssues
mergeRequestsEnabled := repo.HasPullRequests
```

Then add to the `RepositoryDTO` literal:

```go
IssuesEnabled:        &issuesEnabled,
MergeRequestsEnabled: &mergeRequestsEnabled,
```

- [ ] **Step 6: Run complete concrete-adapter tests**

Run:

```bash
go test ./internal/platform/gitea ./internal/platform/forgejo -shuffle=on -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit concrete adapter support**

Run `scripts/context-sync --check`, stage only the Task 3 files, and invoke `$commit` with subject:

```text
fix: expose Gitea-like repository feature flags
```

The body must explain that the concrete SDK booleans make shared candidate-error classification authoritative for both providers.

---

### Task 4: Classify GitLab issue and merge-request read failures

**Files:**
- Create: `internal/platform/gitlab/feature_disabled.go`
- Create: `internal/platform/gitlab/feature_disabled_test.go`
- Modify: `internal/platform/gitlab/client.go`
- Modify: `internal/platform/gitlab/pages.go`
- Modify: `internal/platform/gitlab/diff_review.go`

**Interfaces:**
- Consumes: normalized GitLab repository feature state from Task 1.
- Produces: `func (c *Client) repositoryFeatureError(ctx context.Context, ref platform.RepoRef, feature, capability string, err error) error`.
- Preserves: `mapGitLabError` for every unconfirmed failure.

- [ ] **Step 1: Write failing GitLab classifier tests**

Create `internal/platform/gitlab/feature_disabled_test.go`. Use typed `*gitlab.ErrorResponse` operation errors and an `httptest.Server` whose project metadata response controls feature state.

The classifier decision table must cover 403, 404, 410, 401, 429, 500, enabled metadata, and metadata lookup failure. Construct the operation error with:

```go
operationErr := &gitlab.ErrorResponse{StatusCode: tt.status, Message: "feature read failed"}
classified := client.repositoryFeatureError(
	t.Context(), ref, tt.feature, "test_read", operationErr,
)
```

For candidate disabled cases, the project endpoint returns:

```json
{
  "id": 42,
  "path": "project",
  "path_with_namespace": "group/project",
  "issues_enabled": false,
  "merge_requests_enabled": true
}
```

Assert the typed error wraps `operationErr`. For enabled/failed metadata cases, compare the result with `mapGitLabErrorForHost(client.host, "test_read", operationErr)` using `errors.Is` plus the mapped `platform.Error` fields.

- [ ] **Step 2: Run the classifier test and verify it fails**

Run:

```bash
go test ./internal/platform/gitlab -run TestRepositoryFeatureError -count=1
```

Expected: compilation fails because the helper does not exist.

- [ ] **Step 3: Implement GitLab candidate confirmation**

Create `internal/platform/gitlab/feature_disabled.go`:

```go
package gitlab

import (
	"context"
	"errors"
	"net/http"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"go.kenn.io/middleman/internal/platform"
)

func (c *Client) repositoryFeatureError(
	ctx context.Context,
	ref platform.RepoRef,
	feature string,
	capability string,
	err error,
) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return c.mapGitLabError(capability, err)
	}
	var responseErr *gitlab.ErrorResponse
	if !errors.As(err, &responseErr) || responseErr == nil ||
		(!responseErr.HasStatusCode(http.StatusForbidden) &&
			!responseErr.HasStatusCode(http.StatusNotFound) &&
			!responseErr.HasStatusCode(http.StatusGone)) {
		return c.mapGitLabError(capability, err)
	}

	repository, lookupErr := c.GetRepository(ctx, ref)
	if lookupErr != nil {
		return c.mapGitLabError(capability, err)
	}
	enabled, known := repository.FeatureEnabled(feature)
	if known && !enabled {
		return platform.RepositoryFeatureDisabled(platform.KindGitLab, c.host, feature, err)
	}
	return c.mapGitLabError(capability, err)
}
```

The helper must wrap the original feature-operation error, not the project lookup error.

- [ ] **Step 4: Write failing GitLab read-boundary tests**

Add table-driven HTTP tests covering these exact boundary rows:

```text
live issue list: /api/v4/projects/42/issues
live MR detail: /api/v4/projects/42/merge_requests/7
MR discussions/review threads: /api/v4/projects/42/merge_requests/7/discussions
MR commits: /api/v4/projects/42/merge_requests/7/commits
issue archive page: /api/v4/projects/42/issues with ItemOrderCreated
MR maintenance page: /api/v4/projects/42/merge_requests with ItemOrderUpdated
```

For each row, return 403, 404, or 410 from the feature endpoint and a project document with the matching feature false from `/api/v4/projects/42`. Assert `platform.ErrRepositoryFeatureDisabled` and exactly one metadata request after the failed feature request.

Add preservation rows where the project reports the matching feature true or the project lookup returns 500; assert the original permission/not-found mapping instead of disabled.

- [ ] **Step 5: Run the GitLab boundary tests and verify they fail**

Run:

```bash
go test ./internal/platform/gitlab -run 'TestClientClassifiesDisabled(FeatureReads|ArchivePages)' -count=1
```

Expected: failures show ordinary mapped errors at the affected boundaries.

- [ ] **Step 6: Apply classification to every GitLab read boundary**

Replace direct `mapGitLabError` calls for these operation errors:

```text
ListOpenMergeRequests
GetMergeRequest
listMergeRequestDiscussionEvents
ListMergeRequestReviewThreads
listMergeRequestCommits
ListOpenIssues
GetIssue
listIssueDiscussions
listInventoryIssuesPage
listInventoryMergeRequestsPage
```

Use `platform.RepositoryFeatureMergeRequests` for MR list/detail/discussions/review threads/commits/archive and `platform.RepositoryFeatureIssues` for issue list/detail/discussions/archive. Preserve each call site's existing capability string.

Pass the normalized repository ref into shared page helpers that currently have only `pid` and `number`:

```go
func (c *Client) listIssueDiscussions(
	ctx context.Context, pid any, ref platform.RepoRef, number int,
) ([]*gitlab.Discussion, error)

func (c *Client) listMergeRequestCommits(
	ctx context.Context, pid any, ref platform.RepoRef, number int,
) ([]*gitlab.Commit, error)
```

At page-fetch closures, classify the raw SDK error before it enters the neutral collector:

```go
return nil, 0, c.repositoryFeatureError(
	ctx, normalizedRef, platform.RepositoryFeatureMergeRequests,
	"list_merge_request_discussions", err,
)
```

Do not alter source-project enrichment, releases, tags, CI, labels, mutations, or GitHub-specific code.

- [ ] **Step 7: Run complete GitLab tests**

Run:

```bash
go test ./internal/platform/gitlab -shuffle=on -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit GitLab classification**

Run `scripts/context-sync --check`, stage only the Task 4 files, and invoke `$commit` with subject:

```text
fix: confirm disabled GitLab repository features
```

The body must explain the error-triggered metadata confirmation and why inconclusive metadata preserves the original mapped read error.

---

### Task 5: Document the invariant and run branch-level verification

**Files:**
- Modify: `context/platform-sync-invariants.md`
- Modify: `context/retries-and-backoffs.md`

**Interfaces:**
- Documents: provider-specific evidence rules feeding the existing provider-neutral cooldown.
- Verifies: all affected provider packages plus the shared sync/archive lifecycle.

- [ ] **Step 1: Update the provider sync invariant**

Replace the existing disabled-feature bullet in `context/platform-sync-invariants.md` with:

```markdown
- A repository-disabled issue or merge-request response is typed
  `repository_feature_disabled` only from definitive provider evidence: GitHub's
  disabled 410 response, or a GitLab/Gitea/Forgejo 403/404/410 confirmed against
  repository feature metadata. (`internal/platform/errors.go::RepositoryFeatureDisabled`)
```

- [ ] **Step 2: Update retry/cooldown classification guidance**

Replace the GitHub-only classification paragraph in `context/retries-and-backoffs.md` with:

```markdown
Classify repository-disabled reads before generic fallback or detail handling. GitHub
uses its definitive disabled 410 response; GitLab, Gitea, and Forgejo confirm candidate
403/404/410 failures against repository metadata, preserving the original mapped error
when confirmation is unavailable. (`internal/platform/errors.go::RepositoryFeatureDisabled`)
```

Keep the separate GitHub GraphQL structured-error bullet because it remains a GitHub transport invariant.

- [ ] **Step 3: Format and run focused provider verification**

Run:

```bash
gofmt -w \
  internal/platform/types.go internal/platform/types_test.go \
  internal/platform/gitealike/feature_disabled.go internal/platform/gitealike/feature_disabled_test.go \
  internal/platform/gitealike/types.go internal/platform/gitealike/normalize.go \
  internal/platform/gitealike/normalize_test.go internal/platform/gitealike/provider.go \
  internal/platform/gitealike/pages.go \
  internal/platform/gitea/convert.go internal/platform/gitea/convert_test.go internal/platform/gitea/client_test.go \
  internal/platform/forgejo/convert.go internal/platform/forgejo/convert_test.go internal/platform/forgejo/client_test.go \
  internal/platform/gitlab/feature_disabled.go internal/platform/gitlab/feature_disabled_test.go \
  internal/platform/gitlab/client.go internal/platform/gitlab/pages.go internal/platform/gitlab/diff_review.go \
  internal/platform/gitlab/normalize.go internal/platform/gitlab/normalize_test.go

go test ./internal/platform ./internal/platform/gitealike ./internal/platform/gitea ./internal/platform/forgejo ./internal/platform/gitlab -shuffle=on -count=1
```

Expected: formatting makes no further changes on a second run; all focused packages pass.

- [ ] **Step 4: Verify cooldown and archive consumers**

Run:

```bash
go test -race ./internal/github -run 'RepositoryFeature|FeatureCooldown|Archive.*Feature' -shuffle=on -count=1
go test ./internal/archive ./internal/github -shuffle=on -count=1
```

Expected: PASS; the new provider errors enter the existing generation-safe cooldown and archive lifecycle without syncer changes.

- [ ] **Step 5: Run repository gates**

Run:

```bash
make nilaway
scripts/context-sync --check
make test-short-precommit
git diff --check
```

Expected: every command exits zero.

- [ ] **Step 6: Commit documentation and final adjustments**

Run context-sync in commit mode, stage the two context documents plus any hook-applied edits to those same documents, and invoke `$commit` with subject:

```text
docs: define cross-provider disabled-feature evidence
```

If all implementation files were already committed in Tasks 1-4, this commit contains only the two context documents. Do not create an empty commit.

- [ ] **Step 7: Push the complete PR head**

Push the local branch to the PR branch without bypassing hooks:

```bash
git push origin HEAD:fix/disabled-feature-sync-cooldown
```

Record the pushed SHA, then verify local HEAD, `origin/fix/disabled-feature-sync-cooldown`, and PR `headRefOid` match.

- [ ] **Step 8: Run the refine-pr exact-head completion gate**

For the pushed SHA, inspect all independent evidence surfaces:

```bash
gh pr checks 719 --json name,state,bucket,link,workflow,startedAt,completedAt
gh pr view 719 --json number,url,headRefName,headRefOid,baseRefName,mergeable,mergeStateStatus,reviewDecision,latestReviews,reviews,comments
gh api repos/kenn-io/middleman/issues/719/comments --paginate
gh api repos/kenn-io/middleman/pulls/719/reviews --paginate
gh api repos/kenn-io/middleman/pulls/719/comments --paginate
roborev wait --sha "$(git rev-parse HEAD)"
roborev list --branch fix/disabled-feature-sync-cooldown --repo "$(git rev-parse --show-toplevel)" --json
```

Fetch all GraphQL review-thread pages and confirm no actionable unresolved non-outdated thread remains. Wait for the trusted GitHub `roborev-ci` synthesis that names the exact pushed SHA, and separately inspect all same-SHA local roborev jobs without creating a review. If any surface reports an actionable finding, begin the next refine-pr repair iteration rather than declaring the PR clean.
