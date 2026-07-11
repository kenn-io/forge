package server

import (
	"context"
	"net/http"
	"testing"

	gh "github.com/google/go-github/v88/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
)

func seedMergeStack(t *testing.T, database *db.DB) {
	t.Helper()
	bottomID := seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA("bottom-head"))
	middleID := seedPR(t, database, "acme", "widget", 2, withSeedPRHeadSHA("middle-head"))
	repo, err := database.GetRepoByIdentity(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	require.NotNil(t, repo)
	stackID, err := database.UpsertStack(t.Context(), repo.ID, 1, "feature")
	require.NoError(t, err)
	require.NoError(t, database.ReplaceStackMembers(t.Context(), stackID, []db.StackMember{
		{StackID: stackID, MergeRequestID: bottomID, Position: 1},
		{StackID: stackID, MergeRequestID: middleID, Position: 2},
	}))
}

func TestAPIMergePRRejectsMidStackMergeByDefault(t *testing.T) {
	assert := assert.New(t)
	mergeCalls := 0
	mock := &mockGH{
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			mergeCalls++
			return &gh.PullRequestMergeResult{}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedMergeStack(t, database)
	client := setupTestClient(t, srv)
	resp, err := client.HTTP.MergePullWithResponse(
		t.Context(), "gh", "acme", "widget", 2,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: new("middle-head")},
	)
	require.NoError(t, err)
	assert.Equal(http.StatusConflict, resp.StatusCode())
	assert.Contains(string(resp.Body), `"reason":"mid_stack_merge_disallowed"`)
	assert.Contains(string(resp.Body), `"blocking_number":1`)
	assert.Zero(mergeCalls)
}

func TestAPIMergePRAllowsMidStackMergeWhenConfigured(t *testing.T) {
	mergeCalls := 0
	mock := &mockGH{
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			mergeCalls++
			return &gh.PullRequestMergeResult{Merged: new(true)}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	srv.cfgMu.Lock()
	srv.cfg = &config.Config{PullRequests: config.PullRequests{AllowMidStackMerges: true}}
	srv.cfgMu.Unlock()
	seedMergeStack(t, database)
	client := setupTestClient(t, srv)
	resp, err := client.HTTP.MergePullWithResponse(
		t.Context(), "gh", "acme", "widget", 2,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: new("middle-head")},
	)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode())
	assert.Equal(t, 1, mergeCalls)
}
