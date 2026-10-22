package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/testutil/serverfake"
	platformgithub "go.kenn.io/forge/platform/github"
)

func TestAPISyncMissingPRRetainsETagForFirstPagePoll(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	var downloads, notModified atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v3/repos/acme/widget/pulls/1", r.URL.Path)
		if r.Header.Get("If-None-Match") == `"page-v1"` {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		downloads.Add(1)
		w.Header().Set("ETag", `"page-v1"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":2002,"number":1,"state":"open","title":"Cold page","html_url":"https://github.com/acme/widget/pull/1","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","head":{"sha":"abc123","ref":"feature"},"base":{"sha":"def456","ref":"main"}}`))
	}))
	t.Cleanup(upstream.Close)
	transport := upstream.Client()
	parentClient, err := platformgithub.NewClient(platformgithub.ClientConfig{
		Read: transport, Write: transport, Notifications: transport,
		Clock: time.Now, APIBase: upstream.URL + "/",
	})
	require.NoError(err)
	mock := &serverfake.MockGH{
		GetPullRequestFn:          parentClient.GetPullRequest,
		GetPullRequestIfChangedFn: parentClient.GetPullRequestIfChanged,
	}
	srv, database, _ := setupTestServerWithMock(t, mock)
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	missing, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 1)
	require.NoError(err)
	require.Nil(missing)
	client := setupTestClient(t, srv)
	synced, err := client.HTTP.SyncPullWithResponse(ctx, &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: 1}})
	require.NoError(err)
	require.Equal(http.StatusOK, synced.StatusCode, string(synced.Body))
	polled, err := client.HTTP.EnqueuePrSyncWithResponse(ctx, &generated.EnqueuePrSyncRequestOptions{PathParams: &generated.EnqueuePrSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: 1}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, polled.StatusCode)
	require.Eventually(func() bool {
		srv.detailSyncMu.Lock()
		defer srv.detailSyncMu.Unlock()
		_, inFlight := srv.detailSyncInFlight["pr:github:github.com:acme/widget#1"]
		return !inFlight
	}, 10*time.Second, time.Millisecond)
	assert.Equal(int32(1), downloads.Load())
	assert.Equal(int32(1), notModified.Load(), "the first page poll sends If-None-Match and receives 304")
}

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
	}
	srv, database, _ := setupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	require.NoError(database.UpsertHTTPEtag(ctx, "github", "github.com", "acme", "widget", "pull_request", 1, `"page-v1"`))
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
}
