package server

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/serverfake"
)

func TestAPIEnqueuePRSyncRefreshesChildrenOnParent304(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	var downloads, conditionalCalls atomic.Int32
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
			downloads.Add(1)
			return nil, errors.New("unexpected full PR download")
		},
		GetPullRequestIfChangedFn: func(_ context.Context, owner, repo string, number int, etag string) (*gh.PullRequest, string, bool, error) {
			conditionalCalls.Add(1)
			assert.Equal("acme", owner)
			assert.Equal("widget", repo)
			assert.Equal(1, number)
			assert.Equal(`"page-v1"`, etag)
			return nil, etag, true, nil
		},
		ListIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			assert.Equal(1, number)
			return []*gh.IssueComment{{ID: new(int64(6)), Body: new("New comment"), CreatedAt: &gh.Timestamp{Time: time.Now().UTC()}}}, nil
		},
		ListCheckRunsForRefFn: func(_ context.Context, _, _, head string) ([]*gh.CheckRun, error) {
			assert.Equal("head-sha", head)
			return []*gh.CheckRun{{Name: new("tests"), Status: new("completed"), Conclusion: new("failure")}}, nil
		},
		ListWorkflowRunsForHeadFn: func(_ context.Context, _, _, head string) ([]*gh.WorkflowRun, error) {
			assert.Equal("head-sha", head)
			return []*gh.WorkflowRun{{ID: new(int64(77)), HeadSHA: new("head-sha"), Event: new("pull_request"), PullRequests: []*gh.PullRequest{{Number: new(1)}}}}, nil
		},
	}
	srv, database, _ := setupTestServerWithMock(t, mock)
	baseline := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	prID := serverfake.SeedPR(t, database, "acme", "widget", 1, func(pr *db.MergeRequest) {
		pr.PlatformHeadSHA = "head-sha"
		pr.PlatformBaseSHA = "base-sha"
		pr.CIStatus = "success"
		pr.DetailFetchedAt = &baseline
	})
	pr, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NoError(database.UpsertHTTPEtag(ctx, "github", "github.com", "acme", "widget", "pull_request", 1, `"page-v1"`))
	beforeDiff, err := database.GetDiffSHAsByRepoID(ctx, pr.RepoID, 1)
	require.NoError(err)
	require.NotNil(beforeDiff)
	client := setupTestClient(t, srv)
	resp, err := client.HTTP.EnqueuePrSyncWithResponse(ctx, &generated.EnqueuePrSyncRequestOptions{PathParams: &generated.EnqueuePrSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: 1}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode)
	require.Eventually(func() bool {
		srv.detailSyncMu.Lock()
		defer srv.detailSyncMu.Unlock()
		_, inFlight := srv.detailSyncInFlight["pr:github:github.com:acme/widget#1"]
		return !inFlight
	}, 10*time.Second, time.Millisecond)

	assert.Equal(int32(1), conditionalCalls.Load())
	assert.Zero(downloads.Load())
	stored, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	assert.Equal("failure", stored.CIStatus)
	assert.True(stored.WorkflowApprovalRequired)
	require.NotNil(stored.DetailFetchedAt)
	assert.True(stored.DetailFetchedAt.After(baseline))
	events, err := database.ListMREvents(ctx, prID)
	require.NoError(err)
	var comments []string
	for _, event := range events {
		if event.EventType == "issue_comment" {
			comments = append(comments, event.Body)
		}
	}
	assert.Equal([]string{"New comment"}, comments)
	afterDiff, err := database.GetDiffSHAsByRepoID(ctx, pr.RepoID, 1)
	require.NoError(err)
	assert.Equal(beforeDiff, afterDiff)
}
