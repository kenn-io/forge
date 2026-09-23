package pulltest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/stacks"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfake"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitealike"

	platformgithub "go.kenn.io/forge/platform/github"

	platformgitlab "go.kenn.io/forge/platform/gitlab"

	gitcmd "go.kenn.io/kit/git/cmd"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m))
}

func seedPRWithHeadSHA(t *testing.T, database *db.DB, owner, name string, number int, headSHA string) int64 {
	t.Helper()
	return serverfake.SeedPR(t, database, owner, name, number, serverfake.WithSeedPRHeadSHA(headSHA))
}

func TestAPIListPulls(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ListPullsWithResponse(t.Context(), &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert := assert.New(t)
	assert.Equal("acme", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("widget", (*resp.JSON200)[0].RepoName)
	assert.Equal("github.com", (*resp.JSON200)[0].PlatformHost)

	raw := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, raw.Code)
	var body []struct {
		Repo httpapi.RepoRefResponse `json:"repo"`
	}
	require.NoError(json.Unmarshal(raw.Body.Bytes(), &body))
	require.Len(body, 1)
	assert.Equal("github", body[0].Repo.Provider)
	assert.Equal("github.com", body[0].Repo.PlatformHost)
	assert.Equal("acme/widget", body[0].Repo.RepoPath)
}

func TestAPIPullResponsesNormalizeMissingKanbanStateToNew(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	mrID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7000,
		Number:         7,
		URL:            "https://github.com/acme/widget/pull/7",
		Title:          "Default kanban PR",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature/default-kanban",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	kanbanState, err := database.GetKanbanState(ctx, mrID)
	require.NoError(err)
	require.Nil(kanbanState)

	rawList := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, rawList.Code)
	var list []pullapi.MergeRequestResponse
	require.NoError(json.Unmarshal(rawList.Body.Bytes(), &list))
	require.Len(list, 1)
	assert.Equal(db.KanbanStatusNew, list[0].KanbanStatus)

	rawNewList := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls?kanban=new", nil)
	require.Equal(http.StatusOK, rawNewList.Code)
	var newList []pullapi.MergeRequestResponse
	require.NoError(json.Unmarshal(rawNewList.Body.Bytes(), &newList))
	require.Len(newList, 1)
	assert.Equal(db.KanbanStatusNew, newList[0].KanbanStatus)

	rawDetail := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/gh/acme/widget/7", nil)
	require.Equal(http.StatusOK, rawDetail.Code)
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.Unmarshal(rawDetail.Body.Bytes(), &detail))
	require.NotNil(detail.MergeRequest)
	assert.Equal(db.KanbanStatusNew, detail.MergeRequest.KanbanStatus)
}

// TestAPIGetPullIncludesCIChecks confirms the PR-detail response decodes the
// merge request's cached ci_checks_json into a top-level checks array.
func TestAPIGetPullIncludesCIChecks(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	checksJSON := `[{"name":"build","status":"completed","conclusion":"success","url":"https://ci.example/build"},` +
		`{"name":"lint","status":"in_progress","conclusion":"","url":"https://ci.example/lint"}]`
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7000,
		Number:         7,
		URL:            "https://github.com/acme/widget/pull/7",
		Title:          "Checks PR",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature/checks",
		BaseBranch:     "main",
		CIStatus:       "pending",
		CIChecksJSON:   checksJSON,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	rawDetail := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/gh/acme/widget/7", nil)
	require.Equal(http.StatusOK, rawDetail.Code)
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.Unmarshal(rawDetail.Body.Bytes(), &detail))
	require.Len(detail.Checks, 2)
	assert.Equal("build", detail.Checks[0].Name)
	assert.Equal("completed", detail.Checks[0].Status)
	assert.Equal("success", detail.Checks[0].Conclusion)
	assert.Equal("https://ci.example/build", detail.Checks[0].URL)
	assert.Equal("lint", detail.Checks[1].Name)
	assert.Equal("in_progress", detail.Checks[1].Status)
}

// TestAPIGetPullToleratesMalformedCIChecks confirms a corrupt ci_checks_json
// cache does not fail the detail response: checks are simply omitted.
func TestAPIGetPullToleratesMalformedCIChecks(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7000,
		Number:         7,
		URL:            "https://github.com/acme/widget/pull/7",
		Title:          "Corrupt checks PR",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature/corrupt",
		BaseBranch:     "main",
		CIChecksJSON:   "not valid json",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	rawDetail := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/gh/acme/widget/7", nil)
	require.Equal(http.StatusOK, rawDetail.Code)
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.Unmarshal(rawDetail.Body.Bytes(), &detail))
	require.Empty(detail.Checks, "malformed checks cache yields no checks, not an error")
}

func TestAPIGetPullIsDBOnly(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			require.Fail("GET pull detail should not call GitHub API")
			return nil, nil
		},
		ListWorkflowRunsForHeadFn: func(_ context.Context, _, _, _ string) ([]*gh.WorkflowRun, error) {
			require.Fail("GET pull detail should not call ListWorkflowRunsForHeadSHA")
			return nil, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	seedPRWithHeadSHA(t, database, "acme", "widget", 1, "deadbeef")
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.MergeRequest)
	// Seeded PR has no DetailFetchedAt, so detail_loaded should be false.
	assert.False(resp.JSON200.DetailLoaded)
	assert.Nil(resp.JSON200.DetailFetchedAt)
	// GET path uses DB state (useLivePR=false) and must not make
	// any live GitHub calls, including ListWorkflowRunsForHeadSHA.
	// WorkflowApproval is empty (zero value) since the DB-only path
	// returns early without checking workflows.
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.False(resp.JSON200.WorkflowApproval.Checked)
}

func TestAPISyncPRIncludesWorkflowApproval(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			sha := "abc123"
			state := "open"
			title := "Synced PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Head:      &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		ListWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			require.Equal("abc123", headSHA)
			return []*gh.WorkflowRun{
				{
					ID:           new(int64(77)),
					HeadSHA:      new("abc123"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	// Sync response uses workflowCheckRuns mode: reads PR state
	// from DB (just synced) and fetches workflow runs live.
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked)
	assert.True(resp.JSON200.WorkflowApproval.Required)
	assert.Equal(int64(1), resp.JSON200.WorkflowApproval.Count)
}

func TestAPIEnqueuePRSyncQueuesOneRerun(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondDone := make(chan struct{})
	var calls atomic.Int64

	mock := &serverfake.MockGH{
		GetPullRequestFn: func(ctx context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			call := calls.Add(1)
			if call == 1 {
				close(firstStarted)
				select {
				case <-releaseFirst:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}

			id := int64(2000 + call)
			sha := fmt.Sprintf("head-%d", call)
			state := "open"
			title := "Async synced PR"
			url := "https://github.com/acme/widget/pull/1"
			now := gh.Timestamp{Time: time.Now().UTC()}
			pr := &gh.PullRequest{
				ID: &id, Number: &number, State: &state, Title: &title,
				HTMLURL: &url, UpdatedAt: &now, CreatedAt: &now,
				Head: &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base: &gh.PullRequestBranch{Ref: new("main")},
			}
			if call == 2 {
				close(secondDone)
			}
			return pr, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.EnqueuePrSyncWithResponse(t.Context(), &generated.EnqueuePrSyncRequestOptions{PathParams: &generated.EnqueuePrSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode)
	select {
	case <-firstStarted:
	case <-time.After(10 * time.Second):
		require.Fail("Condition never satisfied")
	}

	for range 3 {
		resp, err = client.HTTP.EnqueuePrSyncWithResponse(t.Context(), &generated.EnqueuePrSyncRequestOptions{PathParams: &generated.EnqueuePrSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
		require.NoError(err)
		require.Equal(http.StatusAccepted, resp.StatusCode)
	}
	close(releaseFirst)

	// The rerun re-verifies repository identity and persists the settings
	// observation before its PR fetch; give the loaded CI runner headroom.
	select {
	case <-secondDone:
	case <-time.After(10 * time.Second):
		require.Fail("Condition never satisfied")
	}
	assert.Equal(int64(2), calls.Load())
	assert.Never(
		func() bool { return calls.Load() > 2 },
		100*time.Millisecond,
		time.Millisecond,
		"duplicate async sync requests must collapse to one pending rerun",
	)
}

// TestAPIGetPullClearsWorkflowApprovalWhenHeadMoves verifies that a
// persisted "required" approval from a prior head SHA does not bleed
// onto the new head. After a sync that moves the head forward (and
// finds no pending runs), GET must report checked=true, required=false.
func TestAPIGetPullClearsWorkflowApprovalWhenHeadMoves(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	var headSHA atomic.Value
	headSHA.Store("abc123")

	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(2002)
			sha := headSHA.Load().(string)
			state := "open"
			title := "Force-pushed PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Head:      &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		ListWorkflowRunsForHeadFn: func(_ context.Context, _, _, sha string) ([]*gh.WorkflowRun, error) {
			if sha == "abc123" {
				return []*gh.WorkflowRun{
					{
						ID:           new(int64(77)),
						HeadSHA:      new("abc123"),
						Event:        new("pull_request"),
						PullRequests: []*gh.PullRequest{{Number: new(1)}},
					},
				}, nil
			}
			return nil, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	// First sync: persists required=true for abc123.
	syncResp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode)
	require.True(syncResp.JSON200.WorkflowApproval.Required)

	// Head moves forward (force-push); new SHA has no action_required runs.
	headSHA.Store("def456")
	syncResp2, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp2.StatusCode)

	detail, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.NotNil(detail.JSON200)
	wa := detail.JSON200.WorkflowApproval
	assert.True(wa.Checked, "head re-synced: approval state was rechecked")
	assert.False(wa.Required, "new head has no pending runs")
	assert.Equal(int64(0), wa.Count)
}

// TestAPISyncPRIncludesWorkflowApprovalForForkPR covers the regression where
// runs from fork-based PRs have an empty pull_requests array in GitHub's API.
// The sync path must still flag workflow approval as required, otherwise the
// UI never shows the approve button for the exact case it was built for.
func TestAPISyncPRIncludesWorkflowApprovalForForkPR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(2001)
			sha := "forkhead"
			state := "open"
			title := "Fork PR"
			url := "https://github.com/acme/widget/pull/1"
			cloneURL := "https://github.com/fork/widget.git"
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Head: &gh.PullRequestBranch{
					SHA:  &sha,
					Ref:  new("feature"),
					Repo: &gh.Repository{CloneURL: &cloneURL, FullName: new("fork/widget")},
				},
				Base: &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		ListWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			require.Equal("forkhead", headSHA)
			return []*gh.WorkflowRun{
				{
					ID:             new(int64(55)),
					HeadSHA:        new("forkhead"),
					Event:          new("pull_request"),
					HeadBranch:     new("feature"),
					HeadRepository: &gh.Repository{FullName: new("fork/widget")},
					PullRequests:   []*gh.PullRequest{},
				},
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked)
	assert.True(resp.JSON200.WorkflowApproval.Required)
	assert.Equal(int64(1), resp.JSON200.WorkflowApproval.Count)
}

// TestAPISyncPRIgnoresWorkflowRunsForOtherPRAtSameSHA covers the regression
// where two PRs share a head SHA and a populated pull_requests association
// points at the other PR. The sync path must not flag workflow approval as
// required for the wrong PR.
func TestAPISyncPRIgnoresWorkflowRunsForOtherPRAtSameSHA(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(3001)
			sha := "sharedsha"
			state := "open"
			title := "Shared SHA PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Head:      &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		ListWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			require.Equal("sharedsha", headSHA)
			return []*gh.WorkflowRun{
				{
					ID:           new(int64(88)),
					HeadSHA:      new("sharedsha"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(99)}},
				},
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked)
	assert.False(resp.JSON200.WorkflowApproval.Required)
	assert.Equal(int64(0), resp.JSON200.WorkflowApproval.Count)
}

// TestAPIGetPullEmitsDiffWarningWhenSHAsMissing covers the case where a
// previous diff sync failed and left the PR row without diff SHAs. The
// resolveItem path treats DiffSyncError as success and the resolve
// response has no warnings field, so the only place a client can learn
// the diff is unavailable is the next getPull call. This regression
// test pins that behavior so the warning can't silently disappear.
func TestAPIGetPullEmitsDiffWarningWhenSHAsMissing(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	// HasDiffSync gates the inferred warning, so the syncer must be
	// constructed with a non-nil clone manager. The manager itself is
	// never invoked by getPull.
	clonesDir := t.TempDir()
	clones := gitclone.New(clonesDir, nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, clones, serverfake.DefaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	serverfake.SeedPR(t, database, "acme", "widget", 1)

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when diff is missing")
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	warning := warnings[0]
	assert.Contains(warning, "Diff data is unavailable")

	// Sanitization invariants: the warning must not leak any internal
	// detail even when emitted from the read path.
	assert.NotContains(warning, clonesDir)
	assert.NotContains(warning, "refs/")
	assert.NotContains(warning, "rev-parse")
}

// TestAPIGetPullNoDiffWarningWhenSHAsPresent verifies the warning does
// not fire when the row already carries valid diff SHAs that match the
// latest platform head.
func TestAPIGetPullNoDiffWarningWhenSHAsPresent(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, clones, serverfake.DefaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	serverfake.SeedPR(t, database, "acme", "widget", 2)

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	headSHA := "deadbeef00000000000000000000000000000001"
	baseSHA := "deadbeef00000000000000000000000000000010"
	require.NoError(database.UpdatePlatformSHAs(
		ctx, repoID, 2, headSHA, baseSHA,
	))
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 2,
		headSHA,
		baseSHA,
		"deadbeef00000000000000000000000000000003",
	))

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(2)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	if resp.JSON200.Warnings != nil {
		assert.Empty(resp.JSON200.Warnings)
	}
}

// TestAPIGetPullEmitsStaleDiffWarning covers the case where a diff sync
// populated the row but a later push advanced the platform head while
// the next diff sync failed. The recorded DiffHeadSHA is valid but no
// longer matches PlatformHeadSHA, so the UI would show a diff from the
// previous revision without any indication of drift. The warning must
// fire in that case.
func TestAPIGetPullEmitsStaleDiffWarning(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, clones, serverfake.DefaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	serverfake.SeedPR(t, database, "acme", "widget", 3)

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	// Platform reports the latest head; the recorded diff SHAs are from
	// an earlier push that no longer matches.
	require.NoError(database.UpdatePlatformSHAs(
		ctx, repoID, 3,
		"deadbeef00000000000000000000000000000099",
		"deadbeef00000000000000000000000000000010",
	))
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 3,
		"deadbeef00000000000000000000000000000001",
		"deadbeef00000000000000000000000000000002",
		"deadbeef00000000000000000000000000000003",
	))

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(3)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when diff is stale")
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "out of date")
}

// TestAPIGetPullEmitsStaleDiffWarningOnBaseDrift covers the symmetric
// case to the head-drift test: the PR head is unchanged but the base
// branch advanced and the next diff sync failed. diffWarnings must
// mirror getDiff staleness logic, which treats base drift as stale
// for open PRs.
func TestAPIGetPullEmitsStaleDiffWarningOnBaseDrift(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, clones, serverfake.DefaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	serverfake.SeedPR(t, database, "acme", "widget", 4)

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	// Head matches, but the platform base advanced past the recorded
	// diff base — for example a merge landed on main after the diff
	// sync ran.
	headSHA := "deadbeef00000000000000000000000000000001"
	require.NoError(database.UpdatePlatformSHAs(
		ctx, repoID, 4,
		headSHA,
		"deadbeef00000000000000000000000000000099",
	))
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 4,
		headSHA,
		"deadbeef00000000000000000000000000000010",
		"deadbeef00000000000000000000000000000020",
	))

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(4)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when base drifted")
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "out of date")
}

// TestAPISyncPRSanitizesDiffFailureWarning drives the syncPR handler
// through a real diff-sync failure and asserts the HTTP response body
// contains only the sanitized UserMessage. Previous roborev reviews
// flagged that nothing pins the boundary between the raw Error() chain
// (which may carry clone paths, refs, SHAs, and git stderr) and the
// sanitized client-facing string; a future refactor could reintroduce
// the leak without breaking any lower-level test. This test closes
// that gap by wiring a real Syncer to a clone Manager whose base dir
// is unreadable, so EnsureClone fails and the handler must surface
// only the sanitized warning.
func TestAPISyncPRSanitizesDiffFailureWarning(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	// Create a clone base dir that cannot be used: 0o000 blocks every
	// git command rooted under it, so syncMRDiff fails at the clone
	// stage. The exact error message will contain the locked path,
	// which is precisely the detail that must NOT reach the client.
	lockedBase := filepath.Join(t.TempDir(), "locked-clones")
	require.NoError(os.MkdirAll(lockedBase, 0o755))
	require.NoError(os.Chmod(lockedBase, 0o000))
	t.Cleanup(func() { _ = os.Chmod(lockedBase, 0o755) })
	clones := gitclone.New(lockedBase, nil)

	// Mock returns a live open PR with head and base SHAs populated,
	// so syncMRDiff enters the merge-base path rather than the early
	// return for missing SHAs.
	now := gh.Timestamp{Time: time.Now().UTC().Truncate(time.Second)}
	prState := "open"
	prID := int64(9001)
	prNumber := 9
	title := "sync-warning repro"
	body := "body"
	url := "https://github.com/acme/widget/pull/9"
	headSHA := "deadbeef00000000000000000000000000000099"
	baseSHA := "deadbeef00000000000000000000000000000088"
	login := "author"
	headRef := "feature"
	baseRef := "main"
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return &gh.PullRequest{
				ID:        &prID,
				Number:    &prNumber,
				State:     &prState,
				Title:     &title,
				Body:      &body,
				HTMLURL:   &url,
				User:      &gh.User{Login: &login},
				Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
				Base:      &gh.PullRequestBranch{Ref: &baseRef, SHA: &baseSHA},
				CreatedAt: &now,
				UpdatedAt: &now,
			}, nil
		},
	}

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, clones, serverfake.DefaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	_, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	// Diff-sync failures are non-fatal: the handler must return 200
	// with the PR row and a warning, not a 502.
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings)
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	warning := warnings[0]
	assert.Contains(warning, "Diff data is unavailable")

	// Sanitization invariants: the warning must not leak any internal
	// detail from the underlying error chain. This is the regression
	// test the reviewer asked for.
	assert.NotContains(warning, lockedBase, "warning must not leak clone path")
	assert.NotContains(warning, "chdir", "warning must not leak chdir stderr")
	assert.NotContains(warning, "fetch", "warning must not leak git command name")
	assert.NotContains(warning, "ensure bare clone", "warning must not leak fmt.Errorf chain")
	assert.NotContains(warning, "github.com/acme", "warning must not leak remote URL path")
}

func TestAPIGitLabSyncReadsTokenFileAfterRotation(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "gitlab-token")
	require.NoError(os.WriteFile(tokenPath, []byte("first-token\n"), 0o600))

	var tokens []string
	gitlabAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, r.Header.Get("Private-Token"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42":
			_, _ = fmt.Fprint(w, `{
				"id": 42,
				"path": "project",
				"path_with_namespace": "group/project",
				"web_url": "https://gitlab.example.com/group/project",
				"default_branch": "main"
			}`)
		case "/api/v4/projects/42/merge_requests/7":
			_, _ = fmt.Fprint(w, `{
				"id": 7001,
				"iid": 7,
				"project_id": 42,
				"title": "GitLab token-file rotation",
				"state": "opened",
				"web_url": "https://gitlab.example.com/group/project/-/merge_requests/7",
				"author": {"username": "ada", "name": "Ada Lovelace"},
				"source_branch": "feature/token-rotation",
				"target_branch": "main",
				"created_at": "2026-04-01T10:00:00Z",
				"updated_at": "2026-04-02T10:00:00Z"
			}`)
		case "/api/v4/projects/42/merge_requests/7/discussions",
			"/api/v4/projects/42/merge_requests/7/commits":
			_, _ = fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gitlabAPI.Close)

	source := tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: string(platform.KindGitLab), Host: "gitlab.example.com"},
		Candidates: []tokenauth.Candidate{{
			Kind:     tokenauth.SourceKindFile,
			FilePath: tokenPath,
		}},
	}, tokenauth.Options{})
	client, err := platformgitlab.NewClient(
		"gitlab.example.com",
		source,
		platformgitlab.WithBaseURLForTesting(gitlabAPI.URL+"/api/v4"), platformgitlab.
			WithTransport(http.DefaultTransport),
	)
	require.NoError(err)
	registry, err := platform.NewRegistry(client)
	require.NoError(err)

	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         42,
		PlatformExternalID: "42",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	repoID, err := database.UpsertRepo(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	repo := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		Owner:              ref.Owner,
		Name:               ref.Name,
		PlatformHost:       ref.Host,
		RepoPath:           ref.RepoPath,
		PlatformRepoID:     ref.PlatformID,
		PlatformExternalID: ref.PlatformExternalID,
		WebURL:             ref.WebURL,
		CloneURL:           ref.CloneURL,
		DefaultBranch:      ref.DefaultBranch,
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	firstRR := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/sync",
		nil)

	require.Equal(http.StatusOK, firstRR.Code, firstRR.Body.String())
	firstCallCount := len(tokens)
	require.Positive(firstCallCount)
	for _, token := range tokens {
		assert.Equal("first-token", token)
	}

	require.NoError(os.WriteFile(tokenPath, []byte("second-token\n"), 0o600))
	secondRR := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/sync",
		nil)

	require.Equal(http.StatusOK, secondRR.Code, secondRR.Body.String())
	require.Greater(len(tokens), firstCallCount)
	for _, token := range tokens[firstCallCount:] {
		assert.Equal("second-token", token)
	}

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("GitLab token-file rotation", mr.Title)
	require.NotNil(mr.DetailFetchedAt)
}

func TestAPIGitHubSyncReadsCloneTokenFileAfterRotation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()

	remote := filepath.Join(dir, "remote.git")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", remote)
	work := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "clone", remote, work)
	gitfixture.Run(t, work, "config", "user.email", "test@test.com")
	gitfixture.Run(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "base commit")
	gitfixture.Run(t, work, "push", "origin", "main")
	baseSHA := gitOutput(t, work, "rev-parse", "HEAD")
	gitfixture.Run(t, work, "checkout", "-b", "feature/token-rotation")
	require.NoError(os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o644))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "feature commit")
	gitfixture.Run(t, work, "push", "origin", "feature/token-rotation")
	headSHA := gitOutput(t, work, "rev-parse", "HEAD")

	tokenPath := filepath.Join(dir, "github-token")
	require.NoError(os.WriteFile(tokenPath, []byte("first-token\n"), 0o600))
	source := tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: string(platform.KindGitHub), Host: "github.com"},
		Candidates: []tokenauth.Candidate{{
			Kind:     tokenauth.SourceKindFile,
			FilePath: tokenPath,
		}},
	}, tokenauth.Options{})

	capturePath := installCredentialCapturingGit(t, dir)

	cloneURL := gitLocalRemoteURL(remote)
	number := 7
	title := "GitHub clone token-file rotation"
	openState := "open"
	featureBranch := "feature/token-rotation"
	mainBranch := "main"
	htmlURL := "https://github.com/acme/widget/pull/7"
	login := "ada"
	fullName := "acme/widget"
	now := gh.Timestamp{Time: time.Now().UTC().Truncate(time.Second)}
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
			return &gh.PullRequest{
				Number:    &number,
				Title:     &title,
				State:     &openState,
				HTMLURL:   &htmlURL,
				User:      &gh.User{Login: &login},
				CreatedAt: &now,
				UpdatedAt: &now,
				Head: &gh.PullRequestBranch{
					Ref: &featureBranch,
					SHA: &headSHA,
					Repo: &gh.Repository{
						CloneURL: &cloneURL,
						FullName: &fullName,
					},
				},
				Base: &gh.PullRequestBranch{
					Ref: &mainBranch,
					SHA: &baseSHA,
					Repo: &gh.Repository{
						CloneURL: &cloneURL,
						FullName: &fullName,
					},
				},
			}, nil
		},
	}

	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	clones := gitclone.New(filepath.Join(dir, "clones"), gitclone.HostSources{
		"github.com": source,
	})
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		clones,
		[]ghclient.RepoRef{{
			Owner:         "acme",
			Name:          "widget",
			PlatformHost:  "github.com",
			CloneURL:      cloneURL,
			DefaultBranch: "main",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	firstRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/pulls/gh/acme/widget/7/sync", nil)
	require.Equal(http.StatusOK, firstRR.Code, firstRR.Body.String())
	firstCredentials := readCapturedCredentials(t, capturePath)
	require.NotEmpty(firstCredentials)
	for _, token := range firstCredentials {
		assert.Equal("first-token", token)
	}

	require.NoError(os.WriteFile(tokenPath, []byte("second-token\n"), 0o600))
	secondRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/pulls/gh/acme/widget/7/sync", nil)
	require.Equal(http.StatusOK, secondRR.Code, secondRR.Body.String())
	allCredentials := readCapturedCredentials(t, capturePath)
	require.Greater(len(allCredentials), len(firstCredentials))
	for _, token := range allCredentials[len(firstCredentials):] {
		assert.Equal("second-token", token)
	}

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(headSHA, mr.DiffHeadSHA)
	assert.Equal(baseSHA, mr.DiffBaseSHA)
	assert.Equal(baseSHA, mr.MergeBaseSHA)
}

// installCredentialCapturingGit prepends a git wrapper to PATH that, on
// every invocation carrying an injected credential.helper (i.e. every
// networked git command kenn-forge authenticates), runs the helper's get
// action and appends its output to the returned capture file before
// exec'ing the real git. Local reads run without a helper and leave no
// trace, so the file records exactly the credentials git was given.
func installCredentialCapturingGit(t *testing.T, dir string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	capturePath := filepath.Join(dir, "git-credentials.txt")
	gitWrapperDir := filepath.Join(dir, "git-wrapper")
	require.NoError(t, os.MkdirAll(gitWrapperDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitWrapperDir, "git"), []byte(`#!/bin/sh
set -eu
`+gitfake.CredentialHelperRunner+`
out="${KENN_FORGE_TEST_GIT_CAPTURE:?}"
helper=""
i=0
count="${GIT_CONFIG_COUNT:-0}"
while [ "$i" -lt "$count" ]; do
	eval "key=\${GIT_CONFIG_KEY_$i:-}"
	eval "value=\${GIT_CONFIG_VALUE_$i:-}"
	if [ "$key" = "credential.helper" ]; then
		helper="$value"
	fi
	i=$((i + 1))
done
if [ -n "$helper" ]; then
	run_credential_helper "$helper" get >> "$out"
	echo "---" >> "$out"
fi
exec "$KENN_FORGE_TEST_REAL_GIT" "$@"
`), 0o755))
	t.Setenv("PATH", gitWrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KENN_FORGE_TEST_GIT_CAPTURE", capturePath)
	t.Setenv("KENN_FORGE_TEST_REAL_GIT", realGit)
	return capturePath
}

func readCapturedCredentials(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return parseCapturedCredentials(string(data))
}

func parseCapturedCredentials(raw string) []string {
	var tokens []string
	for line := range strings.SplitSeq(strings.TrimSpace(raw), "\n") {
		if token, ok := strings.CutPrefix(line, "password="); ok {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func TestAPICIRefreshWarnsAndPreservesCIWhenProviderFails(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		DefaultBranch:      "main",
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref:   ref,
		CiErr: errors.New("gitlab pipeline API unavailable"),
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	repoID, err := database.UpsertRepo(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:           "Keep stale CI visible",
		Author:          "ada",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "head-sha",
		CIStatus:        "pending",
		CIChecksJSON:    `[{"name":"pipeline","status":"in_progress","conclusion":""}]`,
		CIHadPending:    true,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitLab,
			PlatformHost: ref.Host,
			Owner:        ref.Owner,
			Name:         ref.Name,
			RepoPath:     ref.RepoPath,
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.RefreshPullCiOnHostWithResponse(ctx, &generated.RefreshPullCiOnHostRequestOptions{PathParams: &generated.RefreshPullCiOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.JSON200)
	assert.Equal("pending", resp.JSON200.MergeRequest.CIStatus)
	assert.JSONEq(
		`[{"name":"pipeline","status":"in_progress","conclusion":""}]`,
		resp.JSON200.MergeRequest.CIChecksJSON,
	)
	require.NotNil(resp.JSON200.Warnings)
	require.Len(resp.JSON200.Warnings, 1)
	assert.Contains(resp.JSON200.Warnings[0], "Could not refresh CI checks")

	stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("pending", stored.CIStatus)
	assert.JSONEq(
		`[{"name":"pipeline","status":"in_progress","conclusion":""}]`,
		stored.CIChecksJSON,
	)
}

func TestAPISyncRefreshesStaleCachedChecksWhenAggregateCIChanges(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	fetchedAt := now
	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		DefaultBranch:      "main",
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref: ref,
		MergeRequests: []platform.MergeRequest{{
			Repo:           ref,
			PlatformID:     7001,
			Number:         7,
			URL:            "https://gitlab.example.com/group/project/-/merge_requests/7",
			Title:          "Refresh changed CI",
			Author:         "ada",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			HeadSHA:        "head-sha",
			BaseSHA:        "base-sha",
			CIStatus:       "pending",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		CiChecks: map[string][]platform.CICheck{
			"head-sha": {{
				Name:   "pipeline",
				Status: "in_progress",
			}},
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	repoID, err := database.UpsertRepo(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:           "Refresh changed CI",
		Author:          "ada",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "head-sha",
		PlatformBaseSHA: "base-sha",
		CIStatus:        "failure",
		CIChecksJSON:    `[{"name":"pipeline","status":"completed","conclusion":"failure"}]`,
		CIHadPending:    false,
		DetailFetchedAt: &fetchedAt,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitLab,
			PlatformHost: ref.Host,
			Owner:        ref.Owner,
			Name:         ref.Name,
			RepoPath:     ref.RepoPath,
		}},
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{
			ghclient.RateBucketKey("gitlab", ref.Host, "host"): ghclient.NewSyncBudget(100),
		},
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.JSON200)
	assert.Equal("pending", resp.JSON200.MergeRequest.CIStatus)
	assert.JSONEq(
		`[{"name":"pipeline","status":"in_progress","conclusion":"","url":"","app":""}]`,
		resp.JSON200.MergeRequest.CIChecksJSON,
	)

	stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(stored)
	assert.NotNil(stored.DetailFetchedAt)
	assert.True(stored.CIHadPending)
	assert.JSONEq(
		`[{"name":"pipeline","status":"in_progress","conclusion":"","url":"","app":""}]`,
		stored.CIChecksJSON,
	)
}

func TestAPISyncRefreshesCachedPendingChecksThroughDetailDrain(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	fetchedAt := now
	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4343,
		PlatformExternalID: "gid://gitlab/Project/4343",
		DefaultBranch:      "main",
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref: ref,
		MergeRequests: []platform.MergeRequest{{
			Repo:           ref,
			PlatformID:     7008,
			Number:         8,
			URL:            "https://gitlab.example.com/group/project/-/merge_requests/8",
			Title:          "Refresh cached pending CI",
			Author:         "ada",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			HeadSHA:        "pending-head",
			BaseSHA:        "base-sha",
			CIStatus:       "pending",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		CiChecks: map[string][]platform.CICheck{
			"pending-head": {{
				Name:       "pipeline",
				Status:     "completed",
				Conclusion: "success",
			}},
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	repoID, err := database.UpsertRepo(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7008,
		Number:          8,
		URL:             "https://gitlab.example.com/group/project/-/merge_requests/8",
		Title:           "Refresh cached pending CI",
		Author:          "ada",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "pending-head",
		PlatformBaseSHA: "base-sha",
		CIStatus:        "pending",
		CIChecksJSON:    `[{"name":"pipeline","status":"in_progress","conclusion":""}]`,
		CIHadPending:    false,
		DetailFetchedAt: &fetchedAt,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitLab,
			PlatformHost: ref.Host,
			Owner:        ref.Owner,
			Name:         ref.Name,
			RepoPath:     ref.RepoPath,
		}},
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{
			ghclient.RateBucketKey("gitlab", ref.Host, "host"): ghclient.NewSyncBudget(100),
		},
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(8)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.JSON200)
	assert.Equal("success", resp.JSON200.MergeRequest.CIStatus)
	assert.JSONEq(
		`[{"name":"pipeline","status":"completed","conclusion":"success","url":"","app":""}]`,
		resp.JSON200.MergeRequest.CIChecksJSON,
	)

	stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 8)
	require.NoError(err)
	require.NotNil(stored)
	assert.NotNil(stored.DetailFetchedAt)
	assert.False(stored.CIHadPending)
	assert.JSONEq(
		`[{"name":"pipeline","status":"completed","conclusion":"success","url":"","app":""}]`,
		stored.CIChecksJSON,
	)
}

func TestAPIEditPRContentRejectsNilProviderPayload(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	mock := &serverfake.MockGH{
		EditPullRequestFn: func(
			context.Context,
			string,
			string,
			int,
			platformgithub.EditPullRequestOpts,
		) (*gh.PullRequest, error) {
			return nil, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	title := "Updated title"
	resp, err := client.HTTP.EditPrContentWithResponse(t.Context(), &generated.EditPrContentRequestOptions{PathParams: &generated.EditPrContentPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.EditPrContentBody{Title: &title}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	mr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("Test PR #1", mr.Title)
	assert.Equal("test body", mr.Body)
}

func TestAPITriggerSyncStopsDetailDrainAfterDisabledIndexResult(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	disabledErr := platform.RepositoryFeatureDisabled(
		platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
		errors.New("repository issues disabled"),
	)

	var issueListCalls atomic.Int32
	var issueDetailCalls atomic.Int32
	mock := &serverfake.MockGH{
		ListOpenIssuesFn: func(context.Context, string, string) ([]*gh.Issue, error) {
			issueListCalls.Add(1)
			return nil, disabledErr
		},
		GetIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
			issueDetailCalls.Add(1)
			return nil, disabledErr
		},
	}
	repo := ghclient.RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	repoID, err := database.UpsertRepo(
		ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	for _, number := range []int{1, 2} {
		_, err = database.UpsertIssue(ctx, &db.Issue{
			RepoID: repoID, PlatformID: int64(7000 + number), Number: number,
			URL:   fmt.Sprintf("https://github.com/acme/widget/issues/%d", number),
			Title: fmt.Sprintf("stale issue %d", number), Author: "ada", State: "open",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock}, database, nil,
		[]ghclient.RepoRef{repo}, time.Minute, nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(100)},
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	assert.Equal(int32(1), issueListCalls.Load())
	assert.Zero(int(issueDetailCalls.Load()))

	done := make(chan struct{}, 1)
	syncer.SetOnStatusChange(func(status *ghclient.SyncStatus) {
		if !status.Running {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/sync", nil)
	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.Fail("expected explicit global sync to complete")
	}

	assert.Equal(int32(2), issueListCalls.Load(),
		"explicit sync must bypass the cooldown that existed at run start")
	assert.Zero(int(issueDetailCalls.Load()),
		"the disabled index result must suppress detail work in the same explicit run")
}

func TestAPISyncPRDoesNotOverwriteNewerStateChange(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	staleUpdatedAt := time.Date(2026, 4, 12, 1, 0, 0, 0, time.UTC)
	closedAt := staleUpdatedAt.Add(time.Hour)
	syncStarted := make(chan struct{}, 1)
	releaseSync := make(chan struct{})
	mock := &serverfake.MockGH{
		// The user's close commits the provider's edit response, whose
		// updated_at is newer than the in-flight stale sync's snapshot;
		// the monotonic snapshot guard then rejects the stale sync.
		EditPullRequestFn: func(
			_ context.Context, _, _ string, number int, opts platformgithub.EditPullRequestOpts,
		) (*gh.PullRequest, error) {
			state := "closed"
			if opts.State != nil {
				state = *opts.State
			}
			return serverfake.ProviderStatePR(
				number, state, time.Now().UTC().Add(time.Hour),
				&closedAt, nil, "abc123",
			), nil
		},
		GetPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			syncStarted <- struct{}{}
			<-releaseSync

			id := int64(101)
			state := "open"
			title := "stale sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			createdAt := gh.Timestamp{Time: staleUpdatedAt.Add(-time.Hour)}
			updatedAt := gh.Timestamp{Time: staleUpdatedAt}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	syncDone := make(chan *generated.SyncPullResp, 1)
	syncErr := make(chan error, 1)
	go func() {
		resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
		if err != nil {
			syncErr <- err
			return
		}
		syncDone <- resp
	}()

	<-syncStarted

	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	closedPR, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.Equal(db.MergeRequestStateClosed, closedPR.State)
	require.NotNil(closedPR.ClosedAt)

	close(releaseSync)

	completed := false
	select {
	case err := <-syncErr:
		require.NoError(err)
		completed = true
	case resp := <-syncDone:
		require.Equal(http.StatusOK, resp.StatusCode)
		completed = true
	case <-time.After(5 * time.Second):
	}
	require.True(completed, "timed out waiting for stale PR sync")

	finalPR, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	assert.Equal(db.MergeRequestStateClosed, finalPR.State)
	assert.NotNil(finalPR.ClosedAt)
	assert.Equal("Test PR #1", finalPR.Title)
	assert.True(finalPR.UpdatedAt.After(staleUpdatedAt))
}

func TestAPISyncPRPreservesCIStatusWhileRefreshingCI(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	ciRefreshStarted := make(chan struct{}, 1)
	releaseCIRefresh := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseCIRefresh) })
	})

	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			id := int64(101)
			state := "open"
			title := "fresh sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			now := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &now,
				UpdatedAt: &now,
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
		ListCheckRunsForRefFn: func(_ context.Context, _, _, ref string) ([]*gh.CheckRun, error) {
			require.Equal("abc123", ref)
			ciRefreshStarted <- struct{}{}
			<-releaseCIRefresh
			name := "tests"
			status := "completed"
			conclusion := "success"
			return []*gh.CheckRun{{
				Name:       &name,
				Status:     &status,
				Conclusion: &conclusion,
			}}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRHeadSHA("abc123"))
	repo, err := database.GetRepoByIdentity(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	existingChecksJSON := `[{"name":"tests","status":"completed","conclusion":"success"}]`
	require.NoError(database.UpdateMRCIStatus(
		t.Context(), repo.ID, 1, "success", existingChecksJSON,
	))
	client := servertest.SetupTestClient(t, srv)

	syncDone := make(chan *generated.SyncPullResp, 1)
	syncErr := make(chan error, 1)
	go func() {
		resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
		if err != nil {
			syncErr <- err
			return
		}
		syncDone <- resp
	}()

	select {
	case <-ciRefreshStarted:
	case <-time.After(2 * time.Second):
		require.Fail("CI refresh did not start")
	}

	detailResp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode)
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.MergeRequest)
	assert.Equal("success", detailResp.JSON200.MergeRequest.CIStatus)
	assert.JSONEq(existingChecksJSON, detailResp.JSON200.MergeRequest.CIChecksJSON)

	releaseOnce.Do(func() { close(releaseCIRefresh) })
	select {
	case err := <-syncErr:
		require.NoError(err)
	case resp := <-syncDone:
		require.Equal(http.StatusOK, resp.StatusCode)
	case <-time.After(5 * time.Second):
		require.Fail("timed out waiting for PR sync")
	}
}

func TestAPISyncPRBypassesPullRequestETagForCIRefresh(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	headSHA := "abc123"
	success := "success"
	getPRCalls := 0
	conditionalCalls := 0
	ciCalls := 0
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _ string, _ string, number int) (*gh.PullRequest, error) {
			getPRCalls++
			state := "open"
			title := "manual sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			now := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &now,
				UpdatedAt: &now,
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
		GetPullRequestIfChangedFn: func(_ context.Context, _ string, _ string, _ int, etag string) (*gh.PullRequest, string, bool, error) {
			conditionalCalls++
			require.Equal(`"etag-v1"`, etag)
			return nil, etag, true, nil
		},
		ListCheckRunsForRefFn: func(_ context.Context, _, _ string, ref string) ([]*gh.CheckRun, error) {
			ciCalls++
			require.Equal(headSHA, ref)
			name := "tests"
			status := "completed"
			conclusion := "success"
			return []*gh.CheckRun{{
				Name:       &name,
				Status:     &status,
				Conclusion: &conclusion,
			}}, nil
		},
		GetCombinedStatusFn: func(_ context.Context, _, _, ref string) (*gh.CombinedStatus, error) {
			require.Equal(headSHA, ref)
			return &gh.CombinedStatus{State: &success}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1,
		serverfake.WithSeedPRHeadSHA(headSHA),
		serverfake.WithSeedPRCI("failure", `[{"name":"tests","status":"completed","conclusion":"failure"}]`),
	)
	repo, err := database.GetRepoByIdentity(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpsertHTTPEtag(
		t.Context(), "github", "github.com", "acme", "widget",
		"pull_request", 1, `"etag-v1"`,
	))

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/pulls/gh/acme/widget/1/sync", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	stored, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repo.ID, 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("success", stored.CIStatus)
	assert.Equal(1, getPRCalls)
	assert.Zero(conditionalCalls)
	assert.Equal(1, ciCalls)
}

// When the head SHA changes, previously-recorded CI is tied to the old
// commit and must not be carried forward. If the in-flight CI refresh
// then fails, the detail must show "no CI" rather than stale checks
// attached to a different commit.
func TestAPISyncPRClearsCIWhenHeadSHAChanges(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(101)
			state := "open"
			title := "fresh sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			newHeadSHA := "newhead"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			now := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &now,
				UpdatedAt: &now,
				Head:      &gh.PullRequestBranch{SHA: &newHeadSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
		ListCheckRunsForRefFn: func(_ context.Context, _, _, _ string) ([]*gh.CheckRun, error) {
			return nil, errors.New("simulated CI refresh failure")
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRHeadSHA("oldhead"))
	repo, err := database.GetRepoByIdentity(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	existingChecksJSON := `[{"name":"tests","status":"completed","conclusion":"success"}]`
	require.NoError(database.UpdateMRCIStatus(
		t.Context(), repo.ID, 1, "success", existingChecksJSON,
	))
	// Mark the prior CI snapshot as having had pending checks so the
	// post-sync assertion below distinguishes "cleared" from "default
	// false". UpsertMergeRequest preserves ci_had_pending across
	// upserts, so without an explicit clear it would survive a
	// head-SHA change.
	require.NoError(database.UpdateMRDetailFetched(
		t.Context(), "github", "github.com", "acme", "widget", 1, true,
	))
	client := servertest.SetupTestClient(t, srv)

	syncResp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode)

	detailResp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode)
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.MergeRequest)
	assert.Empty(detailResp.JSON200.MergeRequest.CIStatus)
	assert.Empty(detailResp.JSON200.MergeRequest.CIChecksJSON)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repo.ID, 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.False(mr.CIHadPending)
}

func TestAPIEnqueuePRSyncReturnsBeforeGitHubFetchCompletes(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	syncStarted := make(chan struct{}, 1)
	releaseSync := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseSync) })
	})

	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			syncStarted <- struct{}{}
			<-releaseSync

			id := int64(101)
			state := "open"
			title := "fresh async sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			now := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &now,
				UpdatedAt: &now,
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	resp, err := client.HTTP.EnqueuePrSyncWithResponse(ctx, &generated.EnqueuePrSyncRequestOptions{PathParams: &generated.EnqueuePrSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode)

	select {
	case <-syncStarted:
	case <-time.After(2 * time.Second):
		require.Fail("background sync did not start")
	}
	releaseOnce.Do(func() { close(releaseSync) })
}

func TestAPIListPullsSearchByNumber(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()

	serverfake.SeedPR(t, database, "acme", "widget", 12, serverfake.WithSeedPRTitle("add feature"))
	prID := serverfake.SeedPR(t, database, "acme", "widget", 278, serverfake.WithSeedPRTitle("fix bug"))
	serverfake.SeedPR(t, database, "acme", "widget", 290, serverfake.WithSeedPRTitle("another change"))
	serverfake.SeedPR(t, database, "tools", "worker", 301, serverfake.WithSeedPRTitle("repair bug"))
	serverfake.SeedPR(t, database, "docs", "reader", 302, serverfake.WithSeedPRTitle("can't reproduce"))
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NoError(database.ReplaceMergeRequestLabels(ctx, repo.ID, prID, []db.Label{{
		PlatformID: 200,
		Name:       "needs-review",
		Color:      "fbca04",
		UpdatedAt:  time.Now().UTC(),
	}}))

	client := servertest.SetupTestClient(t, srv)

	pullNumbers := func(params *generated.ListPullsQuery) []int {
		t.Helper()
		resp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{Query: params})
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode)
		require.NotNil(resp.JSON200)
		nums := make([]int, 0, len(*resp.JSON200))
		for _, pr := range *resp.JSON200 {
			nums = append(nums, int(pr.Number))
		}
		return nums
	}

	q := "278"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	q = "#278"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	// Title still matches.
	q = "fix"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	q = "fix widget"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	q = "work bug"
	assert.ElementsMatch([]int{301}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	q = "needs-review bug"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	q = "can't"
	assert.ElementsMatch([]int{302}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	q = "needs-review"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	// Substring of number matches multiple.
	q = "2"
	assert.ElementsMatch([]int{12, 278, 290, 302}, pullNumbers(&generated.ListPullsQuery{Q: &q}))
}

func TestAPIListPullsCasefoldsRepoNames(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServerWithRepos(t, &serverfake.MockGH{}, []ghclient.RepoRef{
		{Owner: "org", Name: "foo", PlatformHost: "github.com"},
	})

	serverfake.SeedPR(t, database, "Org", "Foo", 1)
	serverfake.SeedPR(t, database, "org", "foo", 1)

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.ListPullsWithResponse(t.Context(), &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("org", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("foo", (*resp.JSON200)[0].RepoName)
}

func TestAPIListPullsFiltersProviderQualifiedHostedNestedRepoPath(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServerWithRepos(t, &serverfake.MockGH{}, []ghclient.RepoRef{
		{Owner: "Group/SubGroup", Name: "Project.Special", PlatformHost: "ghe.example.com"},
		{Owner: "other", Name: "repo", PlatformHost: "ghe.example.com"},
	})

	serverfake.SeedPROnHost(t, database, "ghe.example.com", "Group/SubGroup", "Project.Special", 1)
	serverfake.SeedPROnHost(t, database, "ghe.example.com", "other", "repo", 2)

	client := servertest.SetupTestClient(t, srv)
	repo := "github|ghe.example.com/Group/SubGroup/Project.Special"
	resp, err := client.HTTP.ListPullsWithResponse(t.Context(), &generated.ListPullsRequestOptions{Query: &generated.ListPullsQuery{
		Repo: &repo,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("group/subgroup", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("project.special", (*resp.JSON200)[0].RepoName)
}

func TestAPIListPullsAcceptsProviderQualifiedRepoFilter(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)
	ctx := t.Context()

	githubRepo, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "github-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	giteaRepo, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitea",
		PlatformHost:   "github.com",
		PlatformRepoID: "gitea-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	serverfake.SeedPRForRepo(t, database, githubRepo, "github.com", "acme", "widget", 1)
	serverfake.SeedPRForRepo(t, database, giteaRepo, "github.com", "acme", "widget", 2)

	repo := "gitea|github.com/acme/widget"
	resp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{Query: &generated.ListPullsQuery{Repo: &repo}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("gitea", (*resp.JSON200)[0].Repo.Provider)
	assert.Equal("github.com", (*resp.JSON200)[0].PlatformHost)
	assert.Equal("acme", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("widget", (*resp.JSON200)[0].RepoName)
	assert.EqualValues(2, (*resp.JSON200)[0].Number)
}

func TestAPIMarkPRDraftPersistsDraftFlag(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	var gotOwner string
	var gotRepo string
	var gotNumber int
	mock := &serverfake.MockGH{
		ConvertToDraftFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			gotOwner = owner
			gotRepo = repo
			gotNumber = number
			id := int64(1001)
			title := "Draft PR"
			state := "open"
			url := "https://github.com/acme/widget/pull/1"
			author := "octocat"
			draft := true
			now := gh.Timestamp{Time: time.Now().UTC().Add(time.Minute)}
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				Draft:     &draft,
				CreatedAt: &now,
				UpdatedAt: &now,
				User:      &gh.User{Login: &author},
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	before, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(before)
	before.CommentCount = 7
	before.CIStatus = "success"
	before.CIChecksJSON = `[{"name":"ci","status":"success"}]`
	before.ReviewDecision = "APPROVED"
	before.AssigneesJSON = `["alice"]`
	before.ReviewersJSON = `["bob"]`
	before.MergeableState = "clean"
	_, err = database.UpsertMergeRequest(t.Context(), before)
	require.NoError(err)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "draft"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	assert.Equal("acme", gotOwner)
	assert.Equal("widget", gotRepo)
	assert.Equal(1, gotNumber)
	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal(db.MergeRequestStateOpen, pr.State)
	assert.True(pr.IsDraft)
	assert.Nil(pr.ClosedAt)
	assert.Equal(7, pr.CommentCount)
	assert.Equal("success", pr.CIStatus)
	assert.JSONEq(`[{"name":"ci","status":"success"}]`, pr.CIChecksJSON)
	assert.Equal("APPROVED", pr.ReviewDecision)
	assert.Equal([]string{"alice"}, pr.Assignees)
	assert.Equal([]string{"bob"}, pr.RequestedReviewers)
	assert.Equal("clean", pr.MergeableState)
}

func TestResolveItem_PR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget"}}
	srv, database := servertest.SetupTestServerWithRepos(t, &serverfake.MockGH{}, repos)
	serverfake.SeedPR(t, database, "acme", "widget", 42)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(t.Context(), &generated.ResolveRepoItemRequestOptions{PathParams: &generated.ResolveRepoItemPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(42)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Equal("pr", resp.JSON200.ItemType)
	require.EqualValues(42, resp.JSON200.Number)
	require.True(resp.JSON200.RepoTracked)
}

func TestProviderPullRouteResolvesEscapedGitLabRepoPath(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	repoPath := "Group/SubGroup/SubGroup 2/My_Project.v2"
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com:8443",
		PlatformRepoID: "gid://gitlab/Project/12000",
		Owner:          "Group/SubGroup/SubGroup 2",
		Name:           "My_Project.v2",
		RepoPath:       repoPath,
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     12000,
		Number:         12,
		URL:            "https://gitlab.example.com/Group/SubGroup/SubGroup%202/My_Project.v2/-/merge_requests/12",
		Title:          "Nested GitLab MR",
		Author:         "testuser",
		State:          "open",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	path := "/api/v1/host/gitlab.example.com:8443/pulls/gl/" +
		"Group%2FSubGroup%2FSubGroup%202/My_Project.v2/12"
	rr := testutil.DoJSON(t, srv, http.MethodGet, path, nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body generated.MergeRequestDetailResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal("gitlab", body.Repo.Provider)
	assert.Equal("gitlab.example.com:8443", body.Repo.PlatformHost)
	assert.Equal(repoPath, body.Repo.RepoPath)
	assert.Equal("Group/SubGroup/SubGroup 2", body.Repo.Owner)
	assert.Equal("My_Project.v2", body.Repo.Name)
	assert.Equal(int64(12), body.MergeRequest.Number)
}

func TestAPIGitealikeLockedPRPersistsThroughServer(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	transport := &lockedGitealikeTransport{
		repo: gitealike.RepositoryDTO{
			ID:            101,
			Owner:         gitealike.UserDTO{UserName: "forgejo"},
			Name:          "tea",
			FullName:      "forgejo/tea",
			HTMLURL:       "https://codeberg.test/forgejo/tea",
			CloneURL:      "https://codeberg.test/forgejo/tea.git",
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		pull: gitealike.PullRequestDTO{
			ID:       201,
			Index:    7,
			HTMLURL:  "https://codeberg.test/forgejo/tea/pulls/7",
			Title:    "Locked tea",
			User:     gitealike.UserDTO{UserName: "alice"},
			State:    "open",
			Draft:    false,
			IsLocked: true,
			Head:     gitealike.BranchDTO{Ref: "feature", SHA: "abc123"},
			Base:     gitealike.BranchDTO{Ref: "main", SHA: "def456"},
			Created:  base,
			Updated:  base.Add(time.Minute),
		},
	}
	provider := gitealike.NewProvider(platform.KindForgejo, "codeberg.test", transport)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindForgejo,
			PlatformHost: "codeberg.test",
			Owner:        "forgejo",
			Name:         "tea",
			RepoPath:     "forgejo/tea",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)
	require.NoError(syncer.SyncMR(ctx, "forgejo", "tea", 7))

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "forgejo",
		PlatformHost: "codeberg.test",
		RepoPath:     "forgejo/tea",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.False(mr.IsDraft)
	assert.True(mr.IsLocked)

	pullResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "codeberg.test", Provider: "forgejo", Owner: "forgejo", Name: "tea", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode, string(pullResp.Body))
	require.NotNil(pullResp.JSON200)
	assert.False(pullResp.JSON200.MergeRequest.IsDraft)
	assert.True(pullResp.JSON200.MergeRequest.IsLocked)
}

func TestAPIGitealikeDraftPRFieldsPersistThroughServer(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	merged := base.Add(2 * time.Hour)
	closed := base.Add(3 * time.Hour)
	transport := &lockedGitealikeTransport{
		repo: gitealike.RepositoryDTO{
			ID:            102,
			Owner:         gitealike.UserDTO{UserName: "gitea"},
			Name:          "tea",
			FullName:      "gitea/tea",
			HTMLURL:       "https://gitea.test/gitea/tea",
			CloneURL:      "https://gitea.test/gitea/tea.git",
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		pull: gitealike.PullRequestDTO{
			ID:      202,
			Index:   8,
			HTMLURL: "https://gitea.test/gitea/tea/pulls/8",
			Title:   "Draft tea",
			User:    gitealike.UserDTO{UserName: "bob"},
			State:   "closed",
			Draft:   true,
			Head:    gitealike.BranchDTO{Ref: "feature", SHA: "abc456"},
			Base:    gitealike.BranchDTO{Ref: "main", SHA: "def789"},
			Labels: []gitealike.LabelDTO{{
				ID:    301,
				Name:  "bug",
				Color: "cc0000",
			}},
			Created:  base,
			Updated:  base.Add(time.Minute),
			Merged:   true,
			MergedAt: &merged,
			Closed:   &closed,
		},
		statuses: []gitealike.StatusDTO{{
			ID:        401,
			Context:   "build",
			State:     "success",
			TargetURL: "javascript:alert(1)",
			Created:   base.Add(time.Minute),
			Updated:   base.Add(time.Minute),
		}},
	}
	provider := gitealike.NewProvider(platform.KindGitea, "gitea.test", transport)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitea,
			PlatformHost: "gitea.test",
			Owner:        "gitea",
			Name:         "tea",
			RepoPath:     "gitea/tea",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)
	require.NoError(syncer.SyncMR(ctx, "gitea", "tea", 8))

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.test",
		RepoPath:     "gitea/tea",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 8)
	require.NoError(err)
	require.NotNil(mr)
	assert.True(mr.IsDraft)
	assert.Equal("feature", mr.HeadBranch)
	assert.Equal("main", mr.BaseBranch)
	assert.Equal("abc456", mr.PlatformHeadSHA)
	assert.Equal("def789", mr.PlatformBaseSHA)
	assert.Equal("success", mr.CIStatus)
	assert.NotEmpty(mr.CIChecksJSON)
	assert.NotContains(mr.CIChecksJSON, "javascript:")
	require.NotNil(mr.MergedAt)
	require.NotNil(mr.ClosedAt)
	require.Len(mr.Labels, 1)
	assert.Equal("bug", mr.Labels[0].Name)

	pullResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "gitea", Name: "tea", Number: int64(8)}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode, string(pullResp.Body))
	require.NotNil(pullResp.JSON200)
	apiMR := pullResp.JSON200.MergeRequest
	assert.True(apiMR.IsDraft)
	assert.Equal("feature", apiMR.HeadBranch)
	assert.Equal("main", apiMR.BaseBranch)
	assert.Equal("success", apiMR.CIStatus)
	assert.NotContains(apiMR.CIChecksJSON, "javascript:")
	require.NotNil(apiMR.MergedAt)
	require.NotNil(apiMR.ClosedAt)
	require.NotNil(apiMR.Labels)
	require.Len(apiMR.Labels, 1)
	assert.Equal("bug", apiMR.Labels[0].Name)
}

type lockedGitealikeTransport struct {
	repo     gitealike.RepositoryDTO
	pull     gitealike.PullRequestDTO
	statuses []gitealike.StatusDTO
}

func (t *lockedGitealikeTransport) GetRepository(
	context.Context,
	string,
	string,
) (gitealike.RepositoryDTO, error) {
	return t.repo, nil
}

func (t *lockedGitealikeTransport) ListUserRepositories(
	context.Context,
	string,
	gitealike.PageOptions,
) ([]gitealike.RepositoryDTO, gitealike.Page, error) {
	return []gitealike.RepositoryDTO{t.repo}, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListOrgRepositories(
	context.Context,
	string,
	gitealike.PageOptions,
) ([]gitealike.RepositoryDTO, gitealike.Page, error) {
	return []gitealike.RepositoryDTO{t.repo}, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListOpenPullRequests(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.PullRequestDTO, gitealike.Page, error) {
	return []gitealike.PullRequestDTO{t.pull}, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) GetPullRequest(
	context.Context,
	platform.RepoRef,
	int,
) (gitealike.PullRequestDTO, error) {
	return t.pull, nil
}

func (t *lockedGitealikeTransport) ListPullRequestComments(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommentDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListPullRequestReviews(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.ReviewDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListPullRequestCommits(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommitDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListOpenIssues(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.IssueDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) GetIssue(
	context.Context,
	platform.RepoRef,
	int,
) (gitealike.IssueDTO, error) {
	return gitealike.IssueDTO{}, platform.ErrNotFound
}

func (t *lockedGitealikeTransport) ListIssueComments(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommentDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListReleases(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.ReleaseDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListTags(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.TagDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListStatuses(
	context.Context,
	platform.RepoRef,
	string,
	gitealike.PageOptions,
) ([]gitealike.StatusDTO, gitealike.Page, error) {
	return t.statuses, gitealike.Page{}, nil
}

func TestAPIGetFilesAndDiffMarkGeneratedFilesE2E(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	serverfake.AcquireRootWorkspaceGitSlot(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	database := dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	clones := gitclone.New(bareDir, nil)
	bare, err := clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(ctx, "repo-acme-widget"),
		"github", "github.com", "acme", "widget",
	)
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(bare), 0o755))

	work := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", bare)
	gitfixture.Run(t, dir, "clone", bare, work)
	gitfixture.Run(t, work, "config", "user.email", "test@test.com")
	gitfixture.Run(t, work, "config", "user.name", "Test")

	require.NoError(os.WriteFile(
		filepath.Join(work, "base.txt"),
		[]byte("base\n"), 0o644,
	))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "base commit")
	gitfixture.Run(t, work, "push", "origin", "main")
	mergeBase := gitfixture.SHA(t, work, "HEAD")

	gitfixture.Run(t, work, "checkout", "-b", "feature")
	require.NoError(os.WriteFile(
		filepath.Join(work, ".gitattributes"),
		[]byte("dist/** linguist-generated\nbun.lock -linguist-generated\n"), 0o644,
	))
	require.NoError(os.MkdirAll(filepath.Join(work, "dist"), 0o755))
	require.NoError(os.WriteFile(
		filepath.Join(work, "dist", "api.ts"),
		[]byte("export const generated = true;\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "bun.lock"),
		[]byte("# lock\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "src.ts"),
		[]byte("export const source = true;\n"), 0o644,
	))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "feature commit")
	gitfixture.Run(t, work, "push", "origin", "feature")
	headSHA := gitfixture.SHA(t, work, "HEAD")

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, nil, serverfake.DefaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	serverfake.SeedPR(t, database, "acme", "widget", 1)
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 1, headSHA, mergeBase, mergeBase))

	filesResp, err := client.HTTP.GetPullFilesWithResponse(ctx, &generated.GetPullFilesRequestOptions{PathParams: &generated.GetPullFilesPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, filesResp.StatusCode, string(filesResp.Body))
	require.NotNil(filesResp.JSON200)
	require.NotNil(filesResp.JSON200.Files)
	assert.True(testutil.RequireWorkspaceDiffFile(t, filesResp.JSON200.Files, "dist/api.ts").IsGenerated)
	assert.False(testutil.RequireWorkspaceDiffFile(t, filesResp.JSON200.Files, "bun.lock").IsGenerated)
	assert.False(testutil.RequireWorkspaceDiffFile(t, filesResp.JSON200.Files, "src.ts").IsGenerated)

	diffResp, err := client.HTTP.GetPullDiffWithResponse(ctx, &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, diffResp.StatusCode, string(diffResp.Body))
	require.NotNil(diffResp.JSON200)
	require.NotNil(diffResp.JSON200.Files)
	assert.True(testutil.RequireWorkspaceDiffFile(t, diffResp.JSON200.Files, "dist/api.ts").IsGenerated)
	assert.False(testutil.RequireWorkspaceDiffFile(t, diffResp.JSON200.Files, "bun.lock").IsGenerated)
	assert.False(testutil.RequireWorkspaceDiffFile(t, diffResp.JSON200.Files, "src.ts").IsGenerated)
}

func TestAPIGetPullDetailLoaded(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	// Before detail fetch: detail_loaded=false.
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	assert.False(resp.JSON200.DetailLoaded)
	assert.Nil(resp.JSON200.DetailFetchedAt)

	// Insert a second PR with DetailFetchedAt set.
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      2000,
		Number:          2,
		URL:             "https://github.com/acme/widget/pull/2",
		Title:           "PR with detail",
		Author:          "testuser",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &now,
	})
	require.NoError(err)

	resp2, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(2)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp2.StatusCode)
	require.NotNil(resp2.JSON200)
	assert.True(resp2.JSON200.DetailLoaded)
	require.NotNil(resp2.JSON200.DetailFetchedAt)
	serverfake.AssertRFC3339UTC(t, *resp2.JSON200.DetailFetchedAt, now)
}

func setupTestServerWithClones(t *testing.T) (
	client *apiclient.Client,
	database *db.DB,
	mergeBase string,
	headSHA string,
	commitSHAs []string,
) {
	t.Helper()

	client, database, mergeBase, headSHA, commitSHAs, _ = servertest.SetupTestServerWithClonesAndServer(t)
	return client, database, mergeBase, headSHA, commitSHAs
}

func TestAPIGetCommits(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	resp, err := client.HTTP.GetPullCommitsWithResponse(t.Context(), &generated.GetPullCommitsRequestOptions{PathParams: &generated.GetPullCommitsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	assert.Len(resp.JSON200.Commits, 5)
	assert.Equal(commitSHAs[0], resp.JSON200.Commits[0].Sha)
	assert.Equal("commit 5", resp.JSON200.Commits[0].Message)
	require.NotNil(resp.JSON200.Commits[0].Stats)
	assert.Equal(int64(1), resp.JSON200.Commits[0].Stats.Additions)
	assert.Zero(resp.JSON200.Commits[0].Stats.Deletions)
	assert.Equal(time.UTC, resp.JSON200.Commits[0].AuthoredAt.Location())
}

func TestAPIGetCommits_NotFound(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	client, _, _, _, _ := setupTestServerWithClones(t)

	resp, err := client.HTTP.GetPullCommitsWithResponse(t.Context(), &generated.GetPullCommitsRequestOptions{PathParams: &generated.GetPullCommitsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(999)}})
	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestAPIGetDiff_SingleCommit(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullDiffQuery{Commit: &commitSHAs[2]}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(resp.JSON200.Files, 1)
}

func TestAPIGetDiffReportsSyncedDiffHeadSHA(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	client, _, _, headSHA, _ := setupTestServerWithClones(t)
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.DiffHeadSha)
	assert.Equal(headSHA, *resp.JSON200.DiffHeadSha)
}

func TestAPIGetFilePreview_ReturnsHeadContent(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	_, _, _, _, _, srv := servertest.SetupTestServerWithClonesAndServer(t)
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/pulls/gh/acme/widget/1/file-preview?path=file5.txt",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	assert.Contains(rr.Body.String(), `"path":"file5.txt"`)
	assert.Contains(rr.Body.String(), `"media_type":"text/plain; charset=utf-8"`)
	assert.Contains(rr.Body.String(), `"encoding":"base64"`)
	assert.Contains(rr.Body.String(), `"content":"Y29udGVudCA1Cg=="`)
}

func TestAPIGetFilePreview_ReturnsDeletedFileContent(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	serverfake.AcquireRootWorkspaceGitSlot(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	database := dbtest.Open(t)

	serverfake.SeedPR(t, database, "acme", "widgets", 1)
	diffRepo, err := testutil.SetupDiffRepo(ctx, dir, database)
	require.NoError(err)

	mock := &serverfake.MockGH{}
	repos := []ghclient.RepoRef{{
		Owner: "acme", Name: "widgets", PlatformHost: "github.com",
	}}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		Clones: diffRepo.Manager,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	path := "config.yaml"
	resp, err := client.HTTP.GetPullFilePreviewWithResponse(ctx, &generated.GetPullFilePreviewRequestOptions{PathParams: &generated.GetPullFilePreviewPath{Provider: "gh", Owner: "acme", Name: "widgets", Number: int64(1)}, Query: &generated.GetPullFilePreviewQuery{Path: &path}})

	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	assert.Equal(path, resp.JSON200.Path)
	decoded, err := base64.StdEncoding.DecodeString(resp.JSON200.Content)
	require.NoError(err)
	assert.Contains(string(decoded), "wal_mode: true")
}

func TestAPIGetFilePreview_ReturnsRequestedDiffSideContent(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	serverfake.AcquireRootWorkspaceGitSlot(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	database := dbtest.Open(t)

	serverfake.SeedPR(t, database, "acme", "widgets", 1)
	diffRepo, err := testutil.SetupDiffRepo(ctx, dir, database)
	require.NoError(err)

	mock := &serverfake.MockGH{}
	repos := []ghclient.RepoRef{{
		Owner: "acme", Name: "widgets", PlatformHost: "github.com",
	}}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		Clones: diffRepo.Manager,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	path := "internal/handler.go"
	oldSide := generated.GetPullFilePreviewQuerySideOld
	oldResp, err := client.HTTP.GetPullFilePreviewWithResponse(ctx, &generated.GetPullFilePreviewRequestOptions{PathParams: &generated.GetPullFilePreviewPath{Provider: "gh", Owner: "acme", Name: "widgets", Number: int64(1)}, Query: &generated.GetPullFilePreviewQuery{Path: &path, Side: &oldSide}})
	require.NoError(err)
	require.Equal(http.StatusOK, oldResp.StatusCode)
	require.NotNil(oldResp.JSON200)
	oldDecoded, err := base64.StdEncoding.DecodeString(oldResp.JSON200.Content)
	require.NoError(err)

	newSide := generated.GetPullFilePreviewQuerySideNew
	newResp, err := client.HTTP.GetPullFilePreviewWithResponse(ctx, &generated.GetPullFilePreviewRequestOptions{PathParams: &generated.GetPullFilePreviewPath{Provider: "gh", Owner: "acme", Name: "widgets", Number: int64(1)}, Query: &generated.GetPullFilePreviewQuery{Path: &path, Side: &newSide}})
	require.NoError(err)
	require.Equal(http.StatusOK, newResp.StatusCode)
	require.NotNil(newResp.JSON200)
	newDecoded, err := base64.StdEncoding.DecodeString(newResp.JSON200.Content)
	require.NoError(err)

	assert.Contains(string(oldDecoded), `log.Println("handling request")`)
	assert.NotContains(string(oldDecoded), "slog.Info")
	assert.Contains(string(newDecoded), `slog.Info("handling request"`)
	assert.NotContains(string(newDecoded), "log.Println")
}

func TestAPIGetDiff_Range(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	from := commitSHAs[4] // commit 1 (oldest)
	to := commitSHAs[2]   // commit 3
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullDiffQuery{From: &from, To: &to}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(resp.JSON200.Files, 3)
}

func TestAPIGetDiff_InvalidScope(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	from := commitSHAs[0]
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullDiffQuery{Commit: &commitSHAs[0], From: &from}})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPIGetDiff_UnknownSHA(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	client, _, _, _, _ := setupTestServerWithClones(t)
	bogus := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullDiffQuery{Commit: &bogus}})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPIGetDiff_ReversedRange(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	from := commitSHAs[0] // newest
	to := commitSHAs[4]   // oldest
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullDiffQuery{From: &from, To: &to}})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPIGetDiff_FromWithoutTo(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	from := commitSHAs[0]
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullDiffQuery{From: &from}})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPIGetDiff_RootCommit(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	serverfake.AcquireRootWorkspaceGitSlot(t)
	require := require.New(t)

	dir := t.TempDir()
	database := dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	require.NoError(os.MkdirAll(bareDir, 0o755))
	clones := gitclone.New(bareDir, nil)
	bare, err := clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(t.Context(), "repo-acme-rootrepo"),
		"github", "github.com", "acme", "rootrepo",
	)
	require.NoError(err)
	tmpWork := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", bare)
	gitfixture.Run(t, dir, "clone", bare, tmpWork)
	gitfixture.Run(t, tmpWork, "config", "user.email", "test@test.com")
	gitfixture.Run(t, tmpWork, "config", "user.name", "Test")

	require.NoError(os.WriteFile(filepath.Join(tmpWork, "root.txt"), []byte("root\n"), 0o644))
	gitfixture.Run(t, tmpWork, "add", ".")
	gitfixture.Run(t, tmpWork, "commit", "-m", "root commit")
	rootSHA := gitfixture.SHA(t, tmpWork, "HEAD")

	require.NoError(os.WriteFile(filepath.Join(tmpWork, "second.txt"), []byte("second\n"), 0o644))
	gitfixture.Run(t, tmpWork, "add", ".")
	gitfixture.Run(t, tmpWork, "commit", "-m", "second commit")
	gitfixture.Run(t, tmpWork, "push", "origin", "main")
	headSHA := gitfixture.SHA(t, tmpWork, "HEAD")

	mock := &serverfake.MockGH{}
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "rootrepo", PlatformHost: "github.com"}}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, repos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})

	serverfake.SeedPR(t, database, "acme", "rootrepo", 1)
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "rootrepo"))
	require.NoError(err)
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 1, headSHA, "4b825dc642cb6eb9a060e54bf8d69288fbee4904", "4b825dc642cb6eb9a060e54bf8d69288fbee4904"))

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "rootrepo", Number: int64(1)}, Query: &generated.GetPullDiffQuery{Commit: &rootSHA}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
}

func seedStackedPRMergeable(
	t *testing.T, database *db.DB,
	owner, name string, number int,
	head, base string, state db.MergeRequestState, ci, review, mergeableState string,
) int64 {
	return serverfake.SeedStackedPRState(t, database, owner, name, number, head, base, state, ci, review, false, mergeableState)
}

func TestAPIListStacks(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)

	serverfake.SeedStackedPR(t, database, "acme", "widget", 10, "feat/auth", "main", db.MergeRequestStateOpen, "success", "APPROVED")
	serverfake.SeedStackedPR(t, database, "acme", "widget", 11, "feat/auth-retry", "feat/auth", db.MergeRequestStateOpen, "success", "APPROVED")
	serverfake.SeedStackedPR(t, database, "acme", "widget", 12, "feat/auth-ui", "feat/auth-retry", db.MergeRequestStateOpen, "pending", "")
	serverfake.RunStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.ListStacksWithResponse(t.Context(), &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	var stks []generated.StackResponse
	require.NoError(json.Unmarshal(resp.Body, &stks))
	assert.Len(stks, 1)
	assert.Equal("auth", stks[0].Name)
	require.NotNil(stks[0].Members)
	assert.Len(stks[0].Members, 3)
	assert.Equal(int64(10), stks[0].Members[0].Number)
}

func TestAPIListStacks_RepoFilter(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	repos := []ghclient.RepoRef{
		{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"},
		{Platform: "github", Owner: "acme", Name: "tools", PlatformHost: "github.com"},
	}
	srv, database := servertest.SetupTestServerWithRepos(t, &serverfake.MockGH{}, repos)
	client := servertest.SetupTestClient(t, srv)
	ctx := t.Context()

	serverfake.SeedStackedPR(t, database, "acme", "widget", 10, "feat/a", "main", db.MergeRequestStateOpen, "", "")
	serverfake.SeedStackedPR(t, database, "acme", "widget", 11, "feat/b", "feat/a", db.MergeRequestStateOpen, "", "")
	serverfake.RunStackDetection(t, database, "acme", "widget")

	serverfake.SeedStackedPR(t, database, "acme", "tools", 20, "feat/c", "main", db.MergeRequestStateOpen, "", "")
	serverfake.SeedStackedPR(t, database, "acme", "tools", 21, "feat/d", "feat/c", db.MergeRequestStateOpen, "", "")
	serverfake.RunStackDetection(t, database, "acme", "tools")

	respAll, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{}})
	require.NoError(err)
	var allStks []generated.StackResponse
	require.NoError(json.Unmarshal(respAll.Body, &allStks))
	assert.Len(allStks, 2)

	repo := "acme/widget"
	resp, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{Repo: &repo}})
	require.NoError(err)
	assert.Equal(http.StatusOK, resp.StatusCode)
	var filtered []generated.StackResponse
	require.NoError(json.Unmarshal(resp.Body, &filtered))
	assert.Len(filtered, 1)
	assert.Equal("widget", filtered[0].RepoName)

	bad := "noslash"
	resp2, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{Repo: &bad}})
	require.Error(err)
	require.NotNil(resp2)
	assert.Equal(http.StatusBadRequest, resp2.StatusCode)
	assert.Contains(string(resp2.Body), "invalid repo filter")
}

func TestAPIGetStackForPR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)
	ctx := t.Context()

	// Failing base with an open descendant is blocked.
	serverfake.SeedStackedPR(t, database, "acme", "widget", 10, "feat/api-base", "main", db.MergeRequestStateOpen, "failure", "")
	serverfake.SeedStackedPR(t, database, "acme", "widget", 11, "feat/api-retry", "feat/api-base", db.MergeRequestStateOpen, "success", "APPROVED")
	serverfake.RunStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	assert.Equal("api", resp.JSON200.StackName)
	assert.Equal(int64(2), resp.JSON200.Size)
	assert.Equal("blocked", resp.JSON200.Health)

	serverfake.SeedPR(t, database, "acme", "widget", 99)
	resp2, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(99)}})
	require.Error(err)
	require.NotNil(resp2)
	assert.Equal(http.StatusNotFound, resp2.StatusCode)
}

func TestAPIGetPullDetailIncludesStackContext(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)
	ctx := t.Context()

	serverfake.SeedStackedPR(t, database, "acme", "widget", 10, "feat/api-base", "main", db.MergeRequestStateOpen, "failure", "")
	serverfake.SeedStackedPR(t, database, "acme", "widget", 11, "feat/api-retry", "feat/api-base", db.MergeRequestStateOpen, "success", "APPROVED")
	serverfake.RunStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(11)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Stack)
	assert.Equal("api", resp.JSON200.Stack.StackName)
	assert.Equal(int64(2), resp.JSON200.Stack.Position)
	assert.Equal(int64(2), resp.JSON200.Stack.Size)
	assert.Equal("blocked", resp.JSON200.Stack.Health)
	require.NotNil(resp.JSON200.Stack.Members)
	assert.Len(resp.JSON200.Stack.Members, 2)

	serverfake.SeedPR(t, database, "acme", "widget", 99)
	unstacked, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(99)}})
	require.NoError(err)
	require.Equal(http.StatusOK, unstacked.StatusCode)
	require.NotNil(unstacked.JSON200)
	assert.Nil(unstacked.JSON200.Stack)
}

func TestAPIStackBaseConflictMarksDownstreamPRsDirty(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)
	ctx := t.Context()

	seedStackedPRMergeable(
		t, database, "acme", "widget", 10,
		"feat/api-base", "main", db.MergeRequestStateOpen, "success", "APPROVED", "dirty",
	)
	serverfake.SeedStackedPR(t, database, "acme", "widget", 11, "feat/api-retry", "feat/api-base", db.MergeRequestStateOpen, "success", "APPROVED")
	serverfake.RunStackDetection(t, database, "acme", "widget")

	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	assert.Empty(serverfake.RequireMR(t, database, repo.ID, 11).MergeableState)

	listResp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{Query: &generated.ListPullsQuery{}})
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode, string(listResp.Body))
	require.NotNil(listResp.JSON200)
	require.Len(*listResp.JSON200, 2)
	assert.Equal("dirty", (*listResp.JSON200)[0].MergeableState)
	assert.Equal("dirty", (*listResp.JSON200)[1].MergeableState)

	stackResp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(11)}})
	require.NoError(err)
	require.Equal(http.StatusOK, stackResp.StatusCode, string(stackResp.Body))
	require.NotNil(stackResp.JSON200)
	require.NotNil(stackResp.JSON200.Members)
	assert.Equal("blocked", stackResp.JSON200.Health)
	assert.Equal("dirty", stackResp.JSON200.Members[0].MergeableState)
	assert.Equal("dirty", stackResp.JSON200.Members[1].MergeableState)
	require.NotNil(stackResp.JSON200.Members[1].BlockedBy)
	assert.Equal(int64(10), *stackResp.JSON200.Members[1].BlockedBy)

	detailResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(11)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
	require.NotNil(detailResp.JSON200)
	assert.Equal("dirty", detailResp.JSON200.MergeRequest.MergeableState)
	assert.Empty(serverfake.RequireMR(t, database, repo.ID, 11).MergeableState)
}

func TestAPIListStacks_DraftNotAllGreen(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)

	// Both draft, green CI + approved — must not be all_green.
	serverfake.SeedStackedPRDraft(t, database, "acme", "widget", 10, "feat/a", "main", db.MergeRequestStateOpen, "success", "APPROVED", true)
	serverfake.SeedStackedPRDraft(t, database, "acme", "widget", 11, "feat/b", "feat/a", db.MergeRequestStateOpen, "success", "APPROVED", true)
	serverfake.RunStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.ListStacksWithResponse(t.Context(), &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	var stks []generated.StackResponse
	require.NoError(json.Unmarshal(resp.Body, &stks))
	require.Len(stks, 1)
	assert.NotEqual("all_green", stks[0].Health, "all-draft stack must not be all_green")
	assert.NotEqual("base_ready", stks[0].Health, "draft base must not be base_ready")
}

func TestAPIStacks_GitLabUnknownForkHeadSyncsButSkipsStackEdges(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	repoRef := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	makeMR := func(platformID int64, number int, head, base, headRepoCloneURL string) platform.MergeRequest {
		return platform.MergeRequest{
			Repo:               repoRef,
			PlatformID:         platformID,
			PlatformExternalID: fmt.Sprintf("gid://gitlab/MergeRequest/%d", platformID),
			Number:             number,
			URL:                fmt.Sprintf("https://gitlab.example.com/group/project/-/merge_requests/%d", number),
			Title:              fmt.Sprintf("MR !%d: %s", number, head),
			Author:             "ada",
			State:              "open",
			HeadBranch:         head,
			BaseBranch:         base,
			HeadSHA:            fmt.Sprintf("sha%d", number),
			BaseSHA:            "basesha",
			HeadRepoCloneURL:   headRepoCloneURL,
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref: repoRef,
		MergeRequests: []platform.MergeRequest{
			makeMR(9001, 90, "feature/fork-ui", "feature/auth", ""),
			makeMR(1001, 100, "feature/auth", "main", repoRef.CloneURL),
			makeMR(1011, 101, "feature/auth-ui", "feature/auth", repoRef.CloneURL),
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{{
			Platform:           platform.KindGitLab,
			Owner:              "group",
			Name:               "project",
			PlatformHost:       "gitlab.example.com",
			RepoPath:           "group/project",
			PlatformRepoID:     4242,
			PlatformExternalID: "gid://gitlab/Project/4242",
			WebURL:             "https://gitlab.example.com/group/project",
			CloneURL:           repoRef.CloneURL,
			DefaultBranch:      "main",
		}}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.SetOnSyncCompleted(stacks.SyncCompletedHook(ctx, database, nil))
	syncer.RunOnce(ctx)

	pullsResp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{Query: &generated.ListPullsQuery{}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullsResp.StatusCode, string(pullsResp.Body))
	require.NotNil(pullsResp.JSON200)
	pullNumbers := make([]int64, 0, len(*pullsResp.JSON200))
	for _, pull := range *pullsResp.JSON200 {
		pullNumbers = append(pullNumbers, pull.Number)
	}
	assert.ElementsMatch([]int64{90, 100, 101}, pullNumbers)

	forkResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gl", Owner: "group", Name: "project", Number: int64(90)}})
	require.NoError(err)
	require.Equal(http.StatusOK, forkResp.StatusCode, string(forkResp.Body))
	require.NotNil(forkResp.JSON200)
	assert.Empty(forkResp.JSON200.MergeRequest.HeadRepoCloneURL)
	assert.Nil(forkResp.JSON200.Stack)

	forkStackResp, err := client.HTTP.GetPullStackOnHostWithResponse(ctx, &generated.GetPullStackOnHostRequestOptions{PathParams: &generated.GetPullStackOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gl", Owner: "group", Name: "project", Number: int64(90)}})
	require.Error(err)
	require.NotNil(forkStackResp)
	assert.Equal(http.StatusNotFound, forkStackResp.StatusCode, string(forkStackResp.Body))

	tipStackResp, err := client.HTTP.GetPullStackOnHostWithResponse(ctx, &generated.GetPullStackOnHostRequestOptions{PathParams: &generated.GetPullStackOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gl", Owner: "group", Name: "project", Number: int64(101)}})
	require.NoError(err)
	require.Equal(http.StatusOK, tipStackResp.StatusCode, string(tipStackResp.Body))
	require.NotNil(tipStackResp.JSON200)
	require.NotNil(tipStackResp.JSON200.Members)
	assert.Equal(int64(2), tipStackResp.JSON200.Size)
	assert.Equal([]int64{100, 101}, serverfake.StackMemberNumbers(tipStackResp.JSON200.Members))

	stacksResp, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{}})
	require.NoError(err)
	require.Equal(http.StatusOK, stacksResp.StatusCode, string(stacksResp.Body))
	require.NotNil(stacksResp.JSON200)
	require.Len(*stacksResp.JSON200, 1)
	require.NotNil((*stacksResp.JSON200)[0].Members)
	assert.Equal([]int64{100, 101}, serverfake.StackMemberNumbers((*stacksResp.JSON200)[0].Members))
}

func TestAPIGetStackForPR_SingleFailingIsInProgress(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)

	// 2-PR chain where tip is failing but has no descendants.
	// Per blocked semantics, this is partial_merge when base is merged.
	serverfake.SeedStackedPR(t, database, "acme", "widget", 10, "feat/base", "main", db.MergeRequestStateMerged, "success", "APPROVED")
	serverfake.SeedStackedPR(t, database, "acme", "widget", 11, "feat/tip", "feat/base", db.MergeRequestStateOpen, "failure", "")
	serverfake.RunStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullStackWithResponse(t.Context(), &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(11)}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotNil(t, resp.JSON200)
	assert.Equal("partial_merge", resp.JSON200.Health,
		"failing tip with merged base and no open descendant is partial_merge, not blocked")
}

func TestAPIGetStackForPR_BaseBranchNotMain(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)

	// Base PR targets "master" not "main" — API must return real base_branch.
	serverfake.SeedStackedPR(t, database, "acme", "widget", 10, "feat/base", "master", db.MergeRequestStateOpen, "success", "APPROVED")
	serverfake.SeedStackedPR(t, database, "acme", "widget", 11, "feat/tip", "feat/base", db.MergeRequestStateOpen, "pending", "")
	serverfake.RunStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullStackWithResponse(t.Context(), &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Members)
	assert.Len(resp.JSON200.Members, 2)
	assert.Equal("master", resp.JSON200.Members[0].BaseBranch)
	assert.Equal("feat/base", resp.JSON200.Members[1].BaseBranch)
}

func TestCICheckDedupLatestRunWinsE2E(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	older := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	newer := older.Add(10 * time.Minute)
	prID := int64(1001)
	prNumber := 1
	prTitle := "check dedupe"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/1"
	prBody := ""
	prAuthor := "alice"
	headRef := "feature/check-dedupe"
	baseRef := "main"
	headSHA := "abc123"
	baseSHA := "def456"
	headCloneURL := "https://github.com/acme/widget.git"
	checkName := "build"
	checkStatus := "completed"
	oldConclusion := "failure"
	newConclusion := "success"
	oldCheckURL := "https://github.com/acme/widget/actions/runs/1"
	newCheckURL := "https://github.com/acme/widget/actions/runs/2"
	combinedTotal := 1
	combinedState := "success"

	pr := &gh.PullRequest{
		ID:        &prID,
		Number:    &prNumber,
		Title:     &prTitle,
		State:     &prState,
		HTMLURL:   &prURL,
		Body:      &prBody,
		User:      &gh.User{Login: &prAuthor},
		CreatedAt: &gh.Timestamp{Time: older},
		UpdatedAt: &gh.Timestamp{Time: newer},
		Head: &gh.PullRequestBranch{
			Ref: &headRef,
			SHA: &headSHA,
			Repo: &gh.Repository{
				CloneURL: &headCloneURL,
			},
		},
		Base: &gh.PullRequestBranch{
			Ref: &baseRef,
			SHA: &baseSHA,
		},
	}

	mock := &serverfake.MockGH{
		GetPullRequestFn: func(
			_ context.Context, _, _ string, _ int,
		) (*gh.PullRequest, error) {
			return pr, nil
		},
		ListCheckRunsForRefFn: func(
			_ context.Context, owner, repo, ref string,
		) ([]*gh.CheckRun, error) {
			require.Equal("acme", owner)
			require.Equal("widget", repo)
			require.Equal(headSHA, ref)
			return []*gh.CheckRun{
				{
					ID:          new(int64(10)),
					Name:        &checkName,
					Status:      &checkStatus,
					Conclusion:  &oldConclusion,
					CompletedAt: &gh.Timestamp{Time: older},
					HTMLURL:     &oldCheckURL,
				},
				{
					ID:          new(int64(11)),
					Name:        &checkName,
					Status:      &checkStatus,
					Conclusion:  &newConclusion,
					CompletedAt: &gh.Timestamp{Time: newer},
					HTMLURL:     &newCheckURL,
				},
			}, nil
		},
		GetCombinedStatusFn: func(
			_ context.Context, owner, repo, ref string,
		) (*gh.CombinedStatus, error) {
			require.Equal("acme", owner)
			require.Equal("widget", repo)
			require.Equal(headSHA, ref)
			return &gh.CombinedStatus{
				TotalCount: &combinedTotal,
				State:      &combinedState,
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	client := servertest.SetupTestClient(t, srv)
	serverfake.SeedPR(
		t, database, "acme", "widget", prNumber,
		serverfake.WithSeedPRHeadSHA(headSHA),
		serverfake.WithSeedPRTimes(older, older, older),
	)

	resp, err := client.HTTP.SyncPullWithResponse(context.Background(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.MergeRequest)
	require.Equal("success", resp.JSON200.MergeRequest.CIStatus)

	var checks []db.CICheck
	require.NoError(json.Unmarshal(
		[]byte(resp.JSON200.MergeRequest.CIChecksJSON),
		&checks,
	))
	require.Len(checks, 1)
	assert.Equal("build", checks[0].Name)
	assert.Equal("completed", checks[0].Status)
	assert.Equal("success", checks[0].Conclusion)
	assert.Equal(newCheckURL, checks[0].URL)
}

func gitLocalRemoteURL(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	slashPath := filepath.ToSlash(path)
	if !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
	return strings.TrimSpace(string(out))
}

func TestAPIEditPRTitleAndBody(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"title": "updated title", "body": "updated body"})

	require.Equal(http.StatusOK, rr.Code)

	mr, err := database.GetMergeRequest(
		t.Context(), "github", "github.com", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal("updated title", mr.Title)
	require.Equal("updated body", mr.Body)
}

func TestAPIEditPRTitleOnly(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"title": "new title"})

	require.Equal(http.StatusOK, rr.Code)

	mr, err := database.GetMergeRequest(
		t.Context(), "github", "github.com", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal("new title", mr.Title)
	require.Equal("test body", mr.Body)
}

func TestAPIEditPRBodyOnly(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"body": "new body"})

	require.Equal(http.StatusOK, rr.Code)

	mr, err := database.GetMergeRequest(
		t.Context(), "github", "github.com", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal("Test PR #1", mr.Title)
	require.Equal("new body", mr.Body)
}

func TestAPIEditPRClearBody(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"body": ""})

	require.Equal(http.StatusOK, rr.Code)

	mr, err := database.GetMergeRequest(
		t.Context(), "github", "github.com", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal("Test PR #1", mr.Title)
	require.Empty(mr.Body)
}

func TestAPIEditPRNoFields400(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]any{})

	require.Equal(http.StatusBadRequest, rr.Code)
}

func TestAPIEditPRBlankTitle400(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"title": "   "})

	require.Equal(http.StatusBadRequest, rr.Code)
}

func TestAPIEditPRPreservesDerivedFields(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1)

	ctx := t.Context()

	// Seed non-default derived fields so we can detect clobbering.
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NoError(database.UpdateMRDerivedFields(ctx, repo.ID, 1, db.MRDerivedFields{
		ReviewDecision: "APPROVED",
		CommentCount:   7,
	}))
	require.NoError(database.UpdateMRCIStatus(ctx, repo.ID, 1, "success", "[]"))

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"title": "changed title"})

	require.Equal(http.StatusOK, rr.Code)

	after, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.Equal("changed title", after.Title)
	require.Equal(7, after.CommentCount)
	require.Equal("success", after.CIStatus)
	require.Equal("APPROVED", after.ReviewDecision)
	require.Equal(db.MergeRequestStateOpen, after.State)
}
