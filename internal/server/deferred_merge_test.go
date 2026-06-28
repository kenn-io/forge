package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

type deferredMergeTestProvider struct {
	apiTestGitLabProvider
	mergeCh       chan deferredMergeTestMergeCall
	mergeErr      error
	ciStarted     chan struct{}
	ciStartedOnce sync.Once
	ciRelease     chan struct{}
}

type deferredMergeTestMergeCall struct {
	Number          int
	CommitTitle     string
	CommitMessage   string
	Method          string
	ExpectedHeadSHA string
}

func (p *deferredMergeTestProvider) Capabilities() platform.Capabilities {
	caps := p.apiTestGitLabProvider.Capabilities()
	caps.MergeMutation = true
	return caps
}

func (p *deferredMergeTestProvider) ListCIChecks(
	ctx context.Context,
	ref platform.RepoRef,
	sha string,
) ([]platform.CICheck, error) {
	if p.ciStarted != nil {
		p.ciStartedOnce.Do(func() { close(p.ciStarted) })
	}
	if p.ciRelease != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.ciRelease:
		}
	}
	return p.apiTestGitLabProvider.ListCIChecks(ctx, ref, sha)
}

func (p *deferredMergeTestProvider) MergeMergeRequest(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	commitTitle string,
	commitMessage string,
	method string,
	expectedHeadSHA string,
) (platform.MergeResult, error) {
	if p.mergeCh != nil {
		p.mergeCh <- deferredMergeTestMergeCall{
			Number:          number,
			CommitTitle:     commitTitle,
			CommitMessage:   commitMessage,
			Method:          method,
			ExpectedHeadSHA: expectedHeadSHA,
		}
	}
	if p.mergeErr != nil {
		return platform.MergeResult{}, p.mergeErr
	}
	return platform.MergeResult{Merged: true, SHA: "merge-sha", Message: "merged"}, nil
}

func newDeferredMergeRouteServer(
	t *testing.T,
	provider *deferredMergeTestProvider,
	ref platform.RepoRef,
	now time.Time,
	initialChecks []db.CICheck,
	options ...ServerOptions,
) (*Server, *db.DB, int64, *apiclient.Client) {
	t.Helper()
	ctx := t.Context()
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)
	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(ref))
	require.NoError(t, err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:           "Defer merge",
		Author:          "ada",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "head-sha",
		PlatformBaseSHA: "base-sha",
		CIStatus:        "pending",
		CIChecksJSON:    mustDeferredMergeChecksJSON(t, initialChecks),
		CIHadPending:    true,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(t, err)

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
			ghclient.RateBucketKey("gitlab", ref.Host): ghclient.NewSyncBudget(100),
		},
	)
	t.Cleanup(syncer.Stop)
	opts := ServerOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	srv := New(database, syncer, nil, "/", nil, opts)
	setTestServerNow(t, srv, now)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, database, repoID, setupTestClient(t, srv)
}

func TestDecodeCIChecks(t *testing.T) {
	require := require.New(t)

	none, err := decodeCIChecks("")
	require.NoError(err)
	require.Nil(none, "empty json yields no checks")

	blank, err := decodeCIChecks("   ")
	require.NoError(err)
	require.Nil(blank, "whitespace-only json yields no checks")

	checks, err := decodeCIChecks(
		`[{"name":"build","status":"completed","conclusion":"success",` +
			`"url":"https://ci/1","app":"GitHub Actions"}]`,
	)
	require.NoError(err)
	require.Len(checks, 1)
	require.Equal("build", checks[0].Name)
	require.Equal("completed", checks[0].Status)
	require.Equal("success", checks[0].Conclusion)
	require.Equal("https://ci/1", checks[0].URL)
	require.Equal("GitHub Actions", checks[0].App)

	_, err = decodeCIChecks("not json")
	require.Error(err, "malformed json is an error the caller decides how to handle")
}

func TestPendingDeferredMergeCheckKeysCapturesOnlyPendingChecks(t *testing.T) {
	checksJSON := mustDeferredMergeChecksJSON(t, []db.CICheck{
		{App: "GitHub Actions", Name: "unit", Status: "in_progress"},
		{App: "Buildkite", Name: "integration", Status: "queued"},
		{App: "GitHub Actions", Name: "lint", Status: "completed", Conclusion: "success"},
	})

	keys, err := pendingDeferredMergeCheckKeys(checksJSON)
	require.NoError(t, err)
	require.Equal(t, []deferredMergeCheckKey{
		{App: "GitHub Actions", Name: "unit"},
		{App: "Buildkite", Name: "integration"},
	}, keys)
}

func TestDeferredMergeCheckStateRequiresCapturedChecksToPass(t *testing.T) {
	keys := []deferredMergeCheckKey{
		{App: "GitHub Actions", Name: "unit"},
		{App: "Buildkite", Name: "integration"},
	}

	tests := []struct {
		name            string
		aggregateStatus string
		checks          []db.CICheck
		want            string
	}{
		{
			name:            "missing captured check stays pending",
			aggregateStatus: "success",
			checks: []db.CICheck{{
				App: "GitHub Actions", Name: "unit", Status: "completed", Conclusion: "success",
			}},
			want: "pending",
		},
		{
			name:            "in progress captured check stays pending",
			aggregateStatus: "pending",
			checks: []db.CICheck{
				{App: "GitHub Actions", Name: "unit", Status: "completed", Conclusion: "success"},
				{App: "Buildkite", Name: "integration", Status: "in_progress"},
			},
			want: "pending",
		},
		{
			name:            "captured checks pass with aggregate success",
			aggregateStatus: "success",
			checks: []db.CICheck{
				{App: "GitHub Actions", Name: "unit", Status: "completed", Conclusion: "success"},
				{App: "Buildkite", Name: "integration", Status: "completed", Conclusion: "skipped"},
			},
			want: "passed",
		},
		{
			name: "captured failure blocks merge",
			checks: []db.CICheck{
				{App: "GitHub Actions", Name: "unit", Status: "completed", Conclusion: "success"},
				{App: "Buildkite", Name: "integration", Status: "completed", Conclusion: "failure"},
			},
			want: "failed",
		},
		{
			name: "non captured failure blocks merge",
			checks: []db.CICheck{
				{App: "GitHub Actions", Name: "unit", Status: "completed", Conclusion: "success"},
				{App: "Buildkite", Name: "integration", Status: "completed", Conclusion: "success"},
				{App: "GitHub Actions", Name: "security", Status: "completed", Conclusion: "failure"},
			},
			want: "failed",
		},
		{
			name:            "non captured pending keeps waiting",
			aggregateStatus: "pending",
			checks: []db.CICheck{
				{App: "GitHub Actions", Name: "unit", Status: "completed", Conclusion: "success"},
				{App: "Buildkite", Name: "integration", Status: "completed", Conclusion: "success"},
				{App: "GitHub Actions", Name: "deploy", Status: "in_progress"},
			},
			want: "pending",
		},
		{
			name: "unknown aggregate blocks passing rows",
			checks: []db.CICheck{
				{App: "GitHub Actions", Name: "unit", Status: "completed", Conclusion: "success"},
				{App: "Buildkite", Name: "integration", Status: "completed", Conclusion: "success"},
			},
			want: "unknown",
		},
		{
			name:            "aggregate pending keeps passing rows pending",
			aggregateStatus: "pending",
			checks: []db.CICheck{
				{App: "GitHub Actions", Name: "unit", Status: "completed", Conclusion: "success"},
				{App: "Buildkite", Name: "integration", Status: "completed", Conclusion: "success"},
			},
			want: "pending",
		},
		{
			name:            "aggregate failure blocks passing rows",
			aggregateStatus: "failure",
			checks: []db.CICheck{
				{App: "GitHub Actions", Name: "unit", Status: "completed", Conclusion: "success"},
				{App: "Buildkite", Name: "integration", Status: "completed", Conclusion: "success"},
			},
			want: "failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := deferredMergeCheckState(tt.aggregateStatus, keys, mustDeferredMergeChecksJSON(t, tt.checks))
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDeferMergeEndpointQueuesMergeAndBroadcastsCompletion(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{
			ref: ref,
			mergeRequests: []platform.MergeRequest{{
				Repo:           ref,
				PlatformID:     7001,
				Number:         7,
				URL:            "https://gitlab.example.com/group/project/-/merge_requests/7",
				Title:          "Defer merge",
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
			ciChecks: map[string][]platform.CICheck{
				"head-sha": {{
					App:        "GitLab",
					Name:       "pipeline",
					Status:     "completed",
					Conclusion: "success",
				}},
			},
		},
		mergeCh: make(chan deferredMergeTestMergeCall, 1),
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(ref))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:           "Defer merge",
		Author:          "ada",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "head-sha",
		PlatformBaseSHA: "base-sha",
		CIStatus:        "pending",
		CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{{
			App: "GitLab", Name: "pipeline", Status: "in_progress",
		}}),
		CIHadPending:   true,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
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
			ghclient.RateBucketKey("gitlab", ref.Host): ghclient.NewSyncBudget(100),
		},
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	setTestServerNow(t, srv, now)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)
	events, _ := srv.Hub().Subscribe(ctx, false)
	expectedHeadSHA := "head-sha"

	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{
			CommitTitle:     "Merge title",
			CommitMessage:   "Merge body",
			Method:          "squash",
			ExpectedHeadSha: &expectedHeadSHA,
		},
	)
	require.NoError(err)
	require.Equal(202, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON202)
	require.Equal("queued", resp.JSON202.Status)
	require.Equal(int64(1), resp.JSON202.PendingChecks)

	var mergeCall deferredMergeTestMergeCall
	select {
	case mergeCall = <-provider.mergeCh:
	case <-time.After(time.Second):
		require.FailNow("timed out waiting for deferred merge")
	}
	require.Equal(7, mergeCall.Number)
	require.Equal("squash", mergeCall.Method)
	require.Equal("head-sha", mergeCall.ExpectedHeadSHA)

	var completed deferredMergeCompletedPayload
	for range 4 {
		select {
		case ev := <-events:
			if ev.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := ev.Event.Data.(deferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-time.After(time.Second):
			require.FailNow("timed out waiting for deferred merge completion event")
		}
		if completed.Status != "" {
			break
		}
	}
	require.Equal("merged", completed.Status)
	require.True(completed.Merged)
	require.Equal("merge-sha", completed.SHA)
	require.Equal("2026-06-15T12:00:00Z", completed.CompletedAt)

	stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(stored)
	require.Equal("merged", string(stored.State))
}

func TestDeferMergeEndpointRejectsInvalidMergeMethodBeforeQueueing(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{ref: ref},
		mergeCh:               make(chan deferredMergeTestMergeCall, 1),
	}
	srv, _, _, client := newDeferredMergeRouteServer(t, provider, ref, now, []db.CICheck{{
		App: "GitLab", Name: "pipeline", Status: "in_progress",
	}})

	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "fast-forward"},
	)
	require.NoError(err)
	require.Equal(400, resp.StatusCode(), string(resp.Body))
	require.Contains(string(resp.Body), "invalid merge method")
	srv.deferredMergeMu.Lock()
	require.Empty(srv.deferredMergeInFlight)
	srv.deferredMergeMu.Unlock()
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointRejectsWithoutPendingChecks(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{ref: ref},
		mergeCh:               make(chan deferredMergeTestMergeCall, 1),
	}
	_, database, repoID, client := newDeferredMergeRouteServer(t, provider, ref, now, []db.CICheck{{
		App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success",
	}})
	require.NoError(database.UpdateMRCIStatus(
		ctx,
		repoID,
		7,
		"success",
		mustDeferredMergeChecksJSON(t, []db.CICheck{{
			App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success",
		}}),
	))

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(409, resp.StatusCode(), string(resp.Body))
	require.Contains(string(resp.Body), "no_pending_checks")
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointRejectsMissingBaseSHA(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{ref: ref},
		mergeCh:               make(chan deferredMergeTestMergeCall, 1),
	}
	_, database, repoID, client := newDeferredMergeRouteServer(t, provider, ref, now, []db.CICheck{{
		App: "GitLab", Name: "pipeline", Status: "in_progress",
	}})
	_, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:           "Defer merge",
		Author:          "ada",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "head-sha",
		PlatformBaseSHA: "",
		CIStatus:        "pending",
		CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{{
			App: "GitLab", Name: "pipeline", Status: "in_progress",
		}}),
		CIHadPending:   true,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(409, resp.StatusCode(), string(resp.Body))
	require.Contains(string(resp.Body), "base_unknown")
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointRejectsFailedAggregateCIWithPassingRows(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{ref: ref},
		mergeCh:               make(chan deferredMergeTestMergeCall, 1),
	}
	_, database, repoID, client := newDeferredMergeRouteServer(t, provider, ref, now, []db.CICheck{{
		App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success",
	}})
	require.NoError(database.UpdateMRCIStatus(
		ctx,
		repoID,
		7,
		"failure",
		mustDeferredMergeChecksJSON(t, []db.CICheck{{
			App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success",
		}}),
	))

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(409, resp.StatusCode(), string(resp.Body))
	require.Contains(string(resp.Body), "ci_failed")
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointFailsWhenAggregatePendingRefreshBecomesUnknown(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{
			ref:      ref,
			ciChecks: map[string][]platform.CICheck{"head-sha": {}},
		},
		mergeCh: make(chan deferredMergeTestMergeCall, 1),
	}
	srv, database, repoID, client := newDeferredMergeRouteServer(
		t,
		provider,
		ref,
		now,
		[]db.CICheck{{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"}},
		ServerOptions{deferredMergeMaxWait: 10 * time.Millisecond},
	)
	require.NoError(database.UpdateMRCIStatus(
		ctx,
		repoID,
		7,
		"pending",
		mustDeferredMergeChecksJSON(t, []db.CICheck{{
			App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success",
		}}),
	))
	events, _ := srv.Hub().Subscribe(ctx, false)

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(202, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON202)
	require.Equal(int64(0), resp.JSON202.PendingChecks)

	var completed deferredMergeCompletedPayload
	for range 4 {
		select {
		case ev := <-events:
			if ev.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := ev.Event.Data.(deferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-time.After(time.Second):
			require.FailNow("timed out waiting for aggregate-unknown deferred merge failure")
		}
		if completed.Status != "" {
			break
		}
	}
	require.Equal("failed", completed.Status)
	require.Contains(completed.Error, "aggregate CI status is unavailable")
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointFailsWhenGranularPendingRefreshHasUnknownAggregate(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{
			ref:      ref,
			ciChecks: map[string][]platform.CICheck{"head-sha": {}},
		},
		mergeCh: make(chan deferredMergeTestMergeCall, 1),
	}
	srv, database, repoID, client := newDeferredMergeRouteServer(
		t,
		provider,
		ref,
		now,
		[]db.CICheck{{App: "GitLab", Name: "pipeline", Status: "in_progress"}},
	)
	require.NoError(database.UpdateMRCIStatus(
		ctx,
		repoID,
		7,
		"",
		mustDeferredMergeChecksJSON(t, []db.CICheck{{
			App: "GitLab", Name: "pipeline", Status: "in_progress",
		}}),
	))
	events, _ := srv.Hub().Subscribe(ctx, false)

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(202, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON202)
	require.Equal(int64(1), resp.JSON202.PendingChecks)

	var completed deferredMergeCompletedPayload
	for range 4 {
		select {
		case ev := <-events:
			if ev.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := ev.Event.Data.(deferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-time.After(time.Second):
			require.FailNow("timed out waiting for aggregate-unknown deferred merge failure")
		}
		if completed.Status != "" {
			break
		}
	}
	require.Equal("failed", completed.Status)
	require.Contains(completed.Error, "aggregate CI status is unavailable")
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointRefreshesEmptyPendingSnapshotBeforeRejecting(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{
			ref: ref,
			ciChecks: map[string][]platform.CICheck{
				"head-sha": {{
					App:    "GitLab",
					Name:   "pipeline",
					Status: "in_progress",
				}},
			},
		},
		mergeCh: make(chan deferredMergeTestMergeCall, 1),
	}
	_, database, repoID, client := newDeferredMergeRouteServer(t, provider, ref, now, nil, ServerOptions{
		deferredMergeMaxWait: 10 * time.Millisecond,
	})
	require.NoError(database.UpdateMRCIStatus(ctx, repoID, 7, "", "[]"))

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(202, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON202)
	require.Equal(int64(1), resp.JSON202.PendingChecks)
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDeferMergeEndpointBroadcastsFailureWhenCIRefreshWarns(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{
			ref:   ref,
			ciErr: errors.New("gitlab pipeline API unavailable"),
		},
		mergeCh: make(chan deferredMergeTestMergeCall, 1),
	}
	srv, _, _, client := newDeferredMergeRouteServer(t, provider, ref, now, []db.CICheck{{
		App: "GitLab", Name: "pipeline", Status: "in_progress",
	}})
	events, _ := srv.Hub().Subscribe(ctx, false)

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(202, resp.StatusCode(), string(resp.Body))

	var completed deferredMergeCompletedPayload
	for range 4 {
		select {
		case ev := <-events:
			if ev.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := ev.Event.Data.(deferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-time.After(time.Second):
			require.FailNow("timed out waiting for deferred merge failure event")
		}
		if completed.Status != "" {
			break
		}
	}
	require.Equal("failed", completed.Status)
	require.Contains(completed.Error, "could not refresh CI checks")
	require.Equal("2026-06-15T12:00:00Z", completed.CompletedAt)
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointBroadcastsFailureWhenCurrentChecksFail(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{
			ref: ref,
			ciChecks: map[string][]platform.CICheck{
				"head-sha": {
					{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"},
					{App: "GitLab", Name: "security", Status: "completed", Conclusion: "failure"},
				},
			},
		},
		mergeCh: make(chan deferredMergeTestMergeCall, 1),
	}
	srv, _, _, client := newDeferredMergeRouteServer(t, provider, ref, now, []db.CICheck{{
		App: "GitLab", Name: "pipeline", Status: "in_progress",
	}})
	events, _ := srv.Hub().Subscribe(ctx, false)

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(202, resp.StatusCode(), string(resp.Body))

	var completed deferredMergeCompletedPayload
	for range 4 {
		select {
		case ev := <-events:
			if ev.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := ev.Event.Data.(deferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-time.After(time.Second):
			require.FailNow("timed out waiting for deferred merge failure event")
		}
		if completed.Status != "" {
			break
		}
	}
	require.Equal("failed", completed.Status)
	require.Contains(completed.Error, "check failed")
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointBroadcastsFailureWhenHeadChangesWhileWaiting(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	ciStarted := make(chan struct{})
	ciRelease := make(chan struct{})
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{
			ref: ref,
			ciChecks: map[string][]platform.CICheck{
				"head-sha": {{
					App:        "GitLab",
					Name:       "pipeline",
					Status:     "completed",
					Conclusion: "success",
				}},
			},
		},
		mergeCh:   make(chan deferredMergeTestMergeCall, 1),
		ciStarted: ciStarted,
		ciRelease: ciRelease,
	}
	srv, database, repoID, client := newDeferredMergeRouteServer(t, provider, ref, now, []db.CICheck{{
		App: "GitLab", Name: "pipeline", Status: "in_progress",
	}})
	events, _ := srv.Hub().Subscribe(ctx, false)

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(202, resp.StatusCode(), string(resp.Body))

	select {
	case <-ciStarted:
	case <-time.After(time.Second):
		require.FailNow("timed out waiting for deferred CI refresh to start")
	}
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:           "Defer merge",
		Author:          "ada",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "new-head-sha",
		PlatformBaseSHA: "base-sha",
		CIStatus:        "pending",
		CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{{
			App: "GitLab", Name: "pipeline", Status: "in_progress",
		}}),
		CIHadPending:   true,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	close(ciRelease)

	var completed deferredMergeCompletedPayload
	for range 4 {
		select {
		case ev := <-events:
			if ev.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := ev.Event.Data.(deferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-time.After(time.Second):
			require.FailNow("timed out waiting for deferred merge stale-head event")
		}
		if completed.Status != "" {
			break
		}
	}
	require.Equal("failed", completed.Status)
	require.Contains(completed.Error, "target changed")
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointBroadcastsFailureWhenProviderBaseChangesBeforeMerge(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	ciStarted := make(chan struct{})
	ciRelease := make(chan struct{})
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{
			ref: ref,
			mergeRequests: []platform.MergeRequest{{
				Repo:           ref,
				PlatformID:     7001,
				Number:         7,
				URL:            "https://gitlab.example.com/group/project/-/merge_requests/7",
				Title:          "Defer merge",
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
			ciChecks: map[string][]platform.CICheck{
				"head-sha": {{
					App:        "GitLab",
					Name:       "pipeline",
					Status:     "completed",
					Conclusion: "success",
				}},
			},
		},
		mergeCh:   make(chan deferredMergeTestMergeCall, 1),
		ciStarted: ciStarted,
		ciRelease: ciRelease,
	}
	srv, _, _, client := newDeferredMergeRouteServer(t, provider, ref, now, []db.CICheck{{
		App: "GitLab", Name: "pipeline", Status: "in_progress",
	}})
	events, _ := srv.Hub().Subscribe(ctx, false)

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(202, resp.StatusCode(), string(resp.Body))

	select {
	case <-ciStarted:
	case <-time.After(time.Second):
		require.FailNow("timed out waiting for deferred CI refresh to start")
	}
	provider.mergeRequests[0].BaseSHA = "new-base-sha"
	close(ciRelease)

	var completed deferredMergeCompletedPayload
	for range 4 {
		select {
		case ev := <-events:
			if ev.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := ev.Event.Data.(deferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-time.After(time.Second):
			require.FailNow("timed out waiting for deferred merge stale-base event")
		}
		if completed.Status != "" {
			break
		}
	}
	require.Equal("failed", completed.Status)
	require.Contains(completed.Error, "target changed")
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointBroadcastsFailureWhenPendingChecksTimeOut(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{
			ref: ref,
			ciChecks: map[string][]platform.CICheck{
				"head-sha": {{
					App:    "GitLab",
					Name:   "pipeline",
					Status: "in_progress",
				}},
			},
		},
		mergeCh: make(chan deferredMergeTestMergeCall, 1),
	}
	srv, _, _, client := newDeferredMergeRouteServer(
		t,
		provider,
		ref,
		now,
		[]db.CICheck{{App: "GitLab", Name: "pipeline", Status: "in_progress"}},
		ServerOptions{deferredMergeMaxWait: 10 * time.Millisecond},
	)
	events, _ := srv.Hub().Subscribe(ctx, false)

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(202, resp.StatusCode(), string(resp.Body))

	var completed deferredMergeCompletedPayload
	for range 4 {
		select {
		case ev := <-events:
			if ev.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := ev.Event.Data.(deferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-time.After(time.Second):
			require.FailNow("timed out waiting for deferred merge timeout event")
		}
		if completed.Status != "" {
			break
		}
	}
	require.Equal("failed", completed.Status)
	require.Contains(completed.Error, "timed out waiting for pending CI checks")
	require.Eventually(func() bool {
		srv.deferredMergeMu.Lock()
		defer srv.deferredMergeMu.Unlock()
		return len(srv.deferredMergeInFlight) == 0
	}, time.Second, 10*time.Millisecond)
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointRejectsClosedPullRequest(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{ref: ref},
		mergeCh:               make(chan deferredMergeTestMergeCall, 1),
	}
	_, database, repoID, client := newDeferredMergeRouteServer(t, provider, ref, now, []db.CICheck{{
		App: "GitLab", Name: "pipeline", Status: "in_progress",
	}})
	// Closing a pull request is the only cancel a user has for a queued
	// deferred merge, so queueing one on a closed pull request must be
	// rejected outright rather than spawning a worker that waits on CI.
	_, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:           "Defer merge",
		Author:          "ada",
		State:           "closed",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "head-sha",
		PlatformBaseSHA: "base-sha",
		CIStatus:        "pending",
		CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{{
			App: "GitLab", Name: "pipeline", Status: "in_progress",
		}}),
		CIHadPending:   true,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(409, resp.StatusCode(), string(resp.Body))
	require.Contains(string(resp.Body), "not_open")
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointBroadcastsFailureWhenTargetClosedWhileWaiting(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	ciStarted := make(chan struct{})
	ciRelease := make(chan struct{})
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{
			ref: ref,
			ciChecks: map[string][]platform.CICheck{
				"head-sha": {{
					App:        "GitLab",
					Name:       "pipeline",
					Status:     "completed",
					Conclusion: "success",
				}},
			},
		},
		mergeCh:   make(chan deferredMergeTestMergeCall, 1),
		ciStarted: ciStarted,
		ciRelease: ciRelease,
	}
	srv, database, repoID, client := newDeferredMergeRouteServer(t, provider, ref, now, []db.CICheck{{
		App: "GitLab", Name: "pipeline", Status: "in_progress",
	}})
	events, _ := srv.Hub().Subscribe(ctx, false)

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(202, resp.StatusCode(), string(resp.Body))

	select {
	case <-ciStarted:
	case <-time.After(time.Second):
		require.FailNow("timed out waiting for deferred CI refresh to start")
	}
	// The pull request is closed mid-wait, even though its head and base are
	// unchanged. The worker must abort instead of merging a retracted target.
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:           "Defer merge",
		Author:          "ada",
		State:           "closed",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "head-sha",
		PlatformBaseSHA: "base-sha",
		CIStatus:        "pending",
		CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{{
			App: "GitLab", Name: "pipeline", Status: "in_progress",
		}}),
		CIHadPending:   true,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	close(ciRelease)

	var completed deferredMergeCompletedPayload
	for range 4 {
		select {
		case ev := <-events:
			if ev.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := ev.Event.Data.(deferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-time.After(time.Second):
			require.FailNow("timed out waiting for deferred merge closed-target event")
		}
		if completed.Status != "" {
			break
		}
	}
	require.Equal("failed", completed.Status)
	require.Contains(completed.Error, "no longer open")
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestDeferMergeEndpointBroadcastsFailureWhenProviderClosedBeforeMerge(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
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
	ciStarted := make(chan struct{})
	ciRelease := make(chan struct{})
	provider := &deferredMergeTestProvider{
		apiTestGitLabProvider: apiTestGitLabProvider{
			ref: ref,
			mergeRequests: []platform.MergeRequest{{
				Repo:           ref,
				PlatformID:     7001,
				Number:         7,
				URL:            "https://gitlab.example.com/group/project/-/merge_requests/7",
				Title:          "Defer merge",
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
			ciChecks: map[string][]platform.CICheck{
				"head-sha": {{
					App:        "GitLab",
					Name:       "pipeline",
					Status:     "completed",
					Conclusion: "success",
				}},
			},
		},
		mergeCh:   make(chan deferredMergeTestMergeCall, 1),
		ciStarted: ciStarted,
		ciRelease: ciRelease,
	}
	srv, _, _, client := newDeferredMergeRouteServer(t, provider, ref, now, []db.CICheck{{
		App: "GitLab", Name: "pipeline", Status: "in_progress",
	}})
	events, _ := srv.Hub().Subscribe(ctx, false)

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(
		ctx,
		ref.Host,
		"gitlab",
		ref.Owner,
		ref.Name,
		7,
		generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	)
	require.NoError(err)
	require.Equal(202, resp.StatusCode(), string(resp.Body))

	select {
	case <-ciStarted:
	case <-time.After(time.Second):
		require.FailNow("timed out waiting for deferred CI refresh to start")
	}
	// The local row still says open, but the provider reports the pull request
	// closed (e.g. closed-to-cancel before the next sync). The authoritative
	// pre-merge provider check must block the merge.
	provider.mergeRequests[0].State = "closed"
	close(ciRelease)

	var completed deferredMergeCompletedPayload
	for range 4 {
		select {
		case ev := <-events:
			if ev.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := ev.Event.Data.(deferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-time.After(time.Second):
			require.FailNow("timed out waiting for deferred merge provider-closed event")
		}
		if completed.Status != "" {
			break
		}
	}
	require.Equal("failed", completed.Status)
	require.Contains(completed.Error, "no longer open")
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func mustDeferredMergeChecksJSON(t *testing.T, checks []db.CICheck) string {
	t.Helper()
	raw, err := json.Marshal(checks)
	require.NoError(t, err)
	return string(raw)
}
