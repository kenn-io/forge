package e2etest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient"
	"go.kenn.io/middleman/internal/apiclient/generated"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	platformgithub "go.kenn.io/middleman/internal/platform/github"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestSyncRoutesWithoutProviderSyncerE2E(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	srv := server.New(database, nil, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	client, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)

	status, err := client.HTTP.GetSyncStatusWithResponse(t.Context())
	require.NoError(err)
	require.Equal(http.StatusOK, status.StatusCode(), string(status.Body))
	require.NotNil(status.JSON200)
	assert.False(status.JSON200.Running)
	assert.Nil(status.JSON200.LastRunAt)
	assert.Nil(status.JSON200.LastError)

	rates, err := client.HTTP.GetRateLimitsWithResponse(t.Context())
	require.NoError(err)
	require.Equal(http.StatusOK, rates.StatusCode(), string(rates.Body))
	require.NotNil(rates.JSON200)
	assert.Empty(rates.JSON200.Hosts)

	trigger, err := client.HTTP.TriggerSyncWithResponse(
		t.Context(),
		nil,
		func(_ context.Context, req *http.Request) error {
			req.Header.Set("Content-Type", "application/json")
			return nil
		},
	)
	require.NoError(err)
	require.Equal(http.StatusServiceUnavailable, trigger.StatusCode(), string(trigger.Body))
	require.NotNil(trigger.ApplicationproblemJSONDefault)
	assert.Equal(generated.ServiceUnavailable, trigger.ApplicationproblemJSONDefault.Code)
	require.NotNil(trigger.ApplicationproblemJSONDefault.Detail)
	assert.Equal("syncer not configured", *trigger.ApplicationproblemJSONDefault.Detail)
}

func TestSyncListNotModifiedDoesNotChangeRateLimitBudgetE2E(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var pulls304 atomic.Int32
	var issues304 atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/repos/acme/widget/pulls", func(w http.ResponseWriter, r *http.Request) {
		writeGitHubListResponse(w, r, `"pulls-v1"`, &pulls304)
	})
	mux.HandleFunc("/api/v3/repos/acme/widget/issues", func(w http.ResponseWriter, r *http.Request) {
		writeGitHubListResponse(w, r, `"issues-v1"`, &issues304)
	})
	githubAPI := httptest.NewServer(mux)
	defer githubAPI.Close()

	database := dbtest.Open(t)
	restTracker := ghclient.NewRateTracker(database, "github.com", "rest")
	budget := ghclient.NewSyncBudget(2)
	client, err := ghclient.NewClient(
		staticTokenSource("token"),
		"github.com",
		restTracker,
		budget,
		ghclient.WithBaseURLForTesting(githubAPI.URL),
	)
	require.NoError(err)

	registry, err := platform.NewRegistry(gitHubIndexListProvider{
		host:   "github.com",
		client: client,
	})
	require.NoError(err)
	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget",
			PlatformHost:       "github.com",
			PlatformRepoID:     101,
			PlatformExternalID: "R_101",
		}},
		time.Minute,
		map[string]*ghclient.RateTracker{"github.com": restTracker},
		map[string]*ghclient.SyncBudget{"github.com": budget},
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	middleman := httptest.NewServer(srv)
	defer middleman.Close()

	api, err := apiclient.NewWithHTTPClient(middleman.URL, middleman.Client())
	require.NoError(err)

	syncLastRunAt := func() *time.Time {
		t.Helper()
		resp, err := api.HTTP.GetSyncStatusWithResponse(t.Context())
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode(), string(resp.Body))
		require.NotNil(resp.JSON200)
		return resp.JSON200.LastRunAt
	}
	triggerSync := func() {
		t.Helper()
		before := syncLastRunAt()
		resp, err := api.HTTP.TriggerSyncWithResponse(
			t.Context(),
			nil,
			func(_ context.Context, req *http.Request) error {
				req.Header.Set("Content-Type", "application/json")
				return nil
			},
		)
		require.NoError(err)
		require.Equal(http.StatusAccepted, resp.StatusCode(), string(resp.Body))
		require.Eventually(func() bool {
			resp, err := api.HTTP.GetSyncStatusWithResponse(t.Context())
			if err != nil || resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
				return false
			}
			if resp.JSON200.Running || resp.JSON200.LastRunAt == nil {
				return false
			}
			return before == nil || resp.JSON200.LastRunAt.After(*before)
		}, 5*time.Second, 10*time.Millisecond)
	}
	budgetSpent := func() int64 {
		t.Helper()
		resp, err := api.HTTP.GetRateLimitsWithResponse(t.Context())
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode(), string(resp.Body))
		require.NotNil(resp.JSON200)
		host, ok := resp.JSON200.Hosts["github.com"]
		require.True(ok)
		return host.BudgetSpent
	}

	triggerSync()
	firstSpent := budgetSpent()
	require.Equal(int64(2), firstSpent)

	triggerSync()
	assert.Equal(int32(1), pulls304.Load())
	assert.Equal(int32(1), issues304.Load())
	assert.Equal(firstSpent, budgetSpent())
}

type gitHubIndexListProvider struct {
	host   string
	client ghclient.Client
}

func (p gitHubIndexListProvider) Platform() platform.Kind {
	return platform.KindGitHub
}

func (p gitHubIndexListProvider) Host() string {
	return p.host
}

func (p gitHubIndexListProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ReadMergeRequests: true,
		ReadIssues:        true,
	}
}

func (p gitHubIndexListProvider) ListOpenMergeRequests(
	ctx context.Context,
	ref platform.RepoRef,
) ([]platform.MergeRequest, error) {
	pulls, err := p.client.ListOpenPullRequests(ctx, ref.Owner, ref.Name)
	if err != nil {
		return nil, err
	}
	out := make([]platform.MergeRequest, 0, len(pulls))
	for _, pull := range pulls {
		normalized, err := platformgithub.NormalizePullRequest(ref, pull)
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}

func (p gitHubIndexListProvider) GetMergeRequest(
	context.Context,
	platform.RepoRef,
	int,
) (platform.MergeRequest, error) {
	return platform.MergeRequest{}, platform.UnsupportedCapability(
		platform.KindGitHub,
		p.host,
		"read_merge_request_detail",
	)
}

func (p gitHubIndexListProvider) ListMergeRequestEvents(
	context.Context,
	platform.RepoRef,
	int,
) ([]platform.MergeRequestEvent, error) {
	return nil, platform.UnsupportedCapability(
		platform.KindGitHub,
		p.host,
		"read_merge_request_events",
	)
}

func (p gitHubIndexListProvider) ListOpenIssues(
	ctx context.Context,
	ref platform.RepoRef,
) ([]platform.Issue, error) {
	issues, err := p.client.ListOpenIssues(ctx, ref.Owner, ref.Name)
	if err != nil {
		return nil, err
	}
	out := make([]platform.Issue, 0, len(issues))
	for _, issue := range issues {
		normalized, err := platformgithub.NormalizeIssue(ref, issue)
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}

func (p gitHubIndexListProvider) GetIssue(
	context.Context,
	platform.RepoRef,
	int,
) (platform.Issue, error) {
	return platform.Issue{}, platform.UnsupportedCapability(
		platform.KindGitHub,
		p.host,
		"read_issue_detail",
	)
}

func (p gitHubIndexListProvider) ListIssueEvents(
	context.Context,
	platform.RepoRef,
	int,
) ([]platform.IssueEvent, error) {
	return nil, platform.UnsupportedCapability(
		platform.KindGitHub,
		p.host,
		"read_issue_events",
	)
}

func writeGitHubListResponse(
	w http.ResponseWriter,
	r *http.Request,
	etag string,
	notModified *atomic.Int32,
) {
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", "4990")
	w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
	if r.Header.Get("If-None-Match") == etag {
		notModified.Add(1)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	_, _ = w.Write([]byte(`[]`))
}
