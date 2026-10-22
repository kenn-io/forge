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
