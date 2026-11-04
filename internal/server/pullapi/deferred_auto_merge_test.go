package pullapi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// autoMergeTestProvider adds provider auto-merge to the deferred merge
// provider: an accepted request shows on its merge request like GitHub's.
type autoMergeTestProvider struct {
	*deferredMergeTestProvider
	allowed      bool
	mergeableNow bool
	stateErr     error
	enableErr    error
	enableCh     chan deferredMergeTestMergeCall
	// enableRelease, when set, holds each request inside the provider call.
	enableRelease chan struct{}
}

func (p *autoMergeTestProvider) AutoMergeState(
	context.Context, platform.RepoRef, int,
) (platform.AutoMergeState, error) {
	if p.stateErr != nil {
		return platform.AutoMergeState{}, p.stateErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return platform.AutoMergeState{
		RepositoryAllows: p.allowed, MergeableNow: p.mergeableNow, Enabled: p.mergeRequests[0].AutoMergeEnabled,
	}, nil
}

func (p *autoMergeTestProvider) EnableAutoMerge(
	_ context.Context, _ platform.RepoRef, number int,
	commitTitle, commitMessage, method, expectedHeadSHA string,
) error {
	p.enableCh <- deferredMergeTestMergeCall{
		Number: number, CommitTitle: commitTitle, CommitMessage: commitMessage,
		Method: method, ExpectedHeadSHA: expectedHeadSHA,
	}
	if p.enableRelease != nil {
		<-p.enableRelease
	}
	if p.enableErr != nil {
		return p.enableErr
	}
	p.setAutoMerge(true)
	return nil
}

func (p *autoMergeTestProvider) setAutoMerge(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	mr := &p.mergeRequests[0]
	mr.AutoMergeEnabled = enabled
	mr.UpdatedAt = mr.UpdatedAt.Add(time.Minute)
	mr.LastActivityAt = mr.UpdatedAt
}

type autoMergeFixture struct {
	provider *autoMergeTestProvider
	srv      *deferredMergeRouteServer
	database *db.DB
	repoID   int64
	client   *apiclient.Client
	ref      platform.RepoRef
}

type autoMergeFixtureOptions struct {
	allowed      bool
	mergeableNow bool
	stateErr     error
	enableErr    error
}

func newAutoMergeFixture(t *testing.T, opts autoMergeFixtureOptions) *autoMergeFixture {
	t.Helper()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	ref := platform.RepoRef{
		Platform:      platform.KindGitLab,
		Host:          "gitlab.example.com",
		Owner:         "group",
		Name:          "project",
		RepoPath:      "group/project",
		Key:           platform.RepositoryIDKey(4242),
		DefaultBranch: "main",
	}
	provider := &autoMergeTestProvider{
		deferredMergeTestProvider: &deferredMergeTestProvider{
			ref: ref,
			mergeRequests: []platform.MergeRequest{{
				Repo: ref, PlatformID: 7001, Number: 7,
				URL:   "https://gitlab.example.com/group/project/-/merge_requests/7",
				Title: "Defer merge", Author: "ada", State: "open",
				HeadBranch: "feature", BaseBranch: "main",
				HeadSHA: "head-sha", BaseSHA: "base-sha", CIStatus: "pending",
				CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
			}},
			ciChecks: map[string][]platform.CICheck{
				"head-sha": {{App: "GitLab", Name: "pipeline", Status: "in_progress"}},
			},
			mergeCh: make(chan deferredMergeTestMergeCall, 1),
		},
		allowed:      opts.allowed,
		mergeableNow: opts.mergeableNow,
		stateErr:     opts.stateErr,
		enableErr:    opts.enableErr,
		enableCh:     make(chan deferredMergeTestMergeCall, 2),
	}
	srv, database, repoID, client := newDeferredMergeRouteServer(
		t, provider, ref, now,
		[]db.CICheck{{App: "GitLab", Name: "pipeline", Status: "in_progress"}},
	)
	return &autoMergeFixture{
		provider: provider, srv: srv, database: database, repoID: repoID, client: client, ref: ref,
	}
}

// deferMerge returns the response whatever its status; the generated client
// also reports an error for every non-2xx answer.
func (f *autoMergeFixture) deferMerge(t *testing.T, body generated.MergePRInputBody) *generated.DeferMergePullOnHostResp {
	t.Helper()
	resp, _ := f.client.HTTP.DeferMergePullOnHostWithResponse(t.Context(), &generated.DeferMergePullOnHostRequestOptions{
		PathParams: &generated.DeferMergePullOnHostPath{
			PlatformHost: f.ref.Host, Provider: "gitlab", Owner: f.ref.Owner, Name: f.ref.Name, Number: 7,
		},
		Body: &body,
	})
	require.NotNil(t, resp)
	return resp
}

func (f *autoMergeFixture) pending(t *testing.T) bool {
	t.Helper()
	resp, err := f.client.HTTP.GetPullOnHostWithResponse(t.Context(), &generated.GetPullOnHostRequestOptions{
		PathParams: &generated.GetPullOnHostPath{
			PlatformHost: f.ref.Host, Provider: "gitlab", Owner: f.ref.Owner, Name: f.ref.Name, Number: 7,
		},
	})
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, string(resp.Body))
	return resp.JSON200.DeferredMergePending
}

// enableCall returns the provider's auto-merge request. Every request is made
// before the deferral answers, so a missing one fails instead of blocking.
func (f *autoMergeFixture) enableCall(t *testing.T) deferredMergeTestMergeCall {
	t.Helper()
	select {
	case call := <-f.provider.enableCh:
		return call
	default:
		require.FailNow(t, "the provider was not asked to enable auto-merge")
		return deferredMergeTestMergeCall{}
	}
}

func (f *autoMergeFixture) workerQueued(t *testing.T) bool {
	t.Helper()
	repo, err := f.database.GetRepoByID(t.Context(), f.repoID)
	require.NoError(t, err)
	return f.srv.handler.isDeferredMergePending(*repo, 7)
}

func (f *autoMergeFixture) keptCleanups(t *testing.T) []db.ObservedMergeWorkspaceCleanup {
	t.Helper()
	cleanups, err := f.database.ListMergeWorkspaceCleanups(t.Context())
	require.NoError(t, err)
	return cleanups
}

func TestDeferMergeHandsOffToProviderAutoMerge(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{allowed: true})

	title, message, head := "Ship it", "Body", "head-sha"
	resp := f.deferMerge(t, generated.MergePRInputBody{
		Method: "squash", CommitTitle: title, CommitMessage: message, ExpectedHeadSha: &head,
	})
	require.Equal(202, resp.StatusCode, string(resp.Body))

	require.Equal(deferredMergeTestMergeCall{
		Number: 7, CommitTitle: title, CommitMessage: message, Method: "squash", ExpectedHeadSHA: head,
	}, f.enableCall(t))
	assert.False(t, f.workerQueued(t), "the provider merges; Forge runs no worker")
	assert.True(t, f.pending(t))

	// The provider cancels auto-merge (a push without write access, or a
	// user on the provider), and the next sync reads it off.
	f.provider.setAutoMerge(false)
	require.NoError(f.srv.handler.syncer.SyncMROnProvider(
		t.Context(), f.ref.Platform, f.ref.Host, f.ref.Owner, f.ref.Name, 7,
	))
	assert.False(t, f.pending(t))
}

func TestDeferMergeRetryFindsAutoMergeOnAndKeepsWorkspaceDeletion(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{allowed: true})
	// An earlier request turned auto-merge on, and sync recorded it, but its
	// answer never reached the user.
	f.provider.setAutoMerge(true)
	require.NoError(f.srv.handler.syncer.SyncMROnProvider(
		t.Context(), f.ref.Platform, f.ref.Host, f.ref.Owner, f.ref.Name, 7,
	))

	head, workspace := "head-sha", "ws-1"
	resp := f.deferMerge(t, generated.MergePRInputBody{
		Method: "squash", ExpectedHeadSha: &head, DeleteWorkspaceID: &workspace,
	})
	require.Equal(202, resp.StatusCode, string(resp.Body))

	assert.Empty(t, f.provider.enableCh, "auto-merge is already on")
	assert.False(t, f.workerQueued(t))
	cleanups := f.keptCleanups(t)
	require.Len(cleanups, 1)
	assert.Equal(t, "ws-1", cleanups[0].WorkspaceID)
}

func TestDeferMergeRetryAfterCIFinishedStillKeepsWorkspaceDeletion(t *testing.T) {
	for _, tt := range []struct {
		name       string
		status     string
		conclusion string
	}{
		{name: "passed", status: "success", conclusion: "success"},
		{name: "failed", status: "failure", conclusion: "failure"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			f := newAutoMergeFixture(t, autoMergeFixtureOptions{allowed: true})
			// Auto-merge went on and its answer was lost; CI has finished
			// since while GitHub still waits on a required review.
			f.provider.setAutoMerge(true)
			check := platform.CICheck{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: tt.conclusion}
			f.provider.mu.Lock()
			f.provider.ciChecks["head-sha"] = []platform.CICheck{check}
			f.provider.mu.Unlock()
			require.NoError(f.database.UpdateMRCIStatus(
				t.Context(), f.repoID, 7, tt.status,
				mustDeferredMergeChecksJSON(t, []db.CICheck{{
					App: check.App, Name: check.Name, Status: check.Status, Conclusion: check.Conclusion,
				}}),
			))

			head, workspace := "head-sha", "ws-1"
			resp := f.deferMerge(t, generated.MergePRInputBody{
				Method: "squash", ExpectedHeadSha: &head, DeleteWorkspaceID: &workspace,
			})
			require.Equal(202, resp.StatusCode, string(resp.Body))

			cleanups := f.keptCleanups(t)
			require.Len(cleanups, 1)
			assert.Equal(t, "ws-1", cleanups[0].WorkspaceID)
			assert.True(t, cleanups[0].ViaAutoMerge, "turning auto-merge off at the provider drops it")
			assert.False(t, f.workerQueued(t))
			assert.Empty(t, f.provider.enableCh, "auto-merge is already on")
		})
	}
}

func TestDeferMergeRunsWorkerWhenRepositoryDisallowsAutoMerge(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{allowed: false})

	head := "head-sha"
	resp := f.deferMerge(t, generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head})
	require.Equal(202, resp.StatusCode, string(resp.Body))

	assert.Empty(t, f.provider.enableCh, "nothing is sent to a repository that disallows auto-merge")
	assert.True(t, f.workerQueued(t))
	assert.True(t, f.pending(t))
}

func TestDeferMergeUsesAutoMergeOnAMergeQueueBranchTheSettingDisallows(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{allowed: false})
	_, err := f.database.UpsertMergeQueueState(t.Context(), f.repoID, db.MergeQueueState{
		Number: 7, BaseBranch: "main", Enabled: true, ObservedAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(err)

	head := "head-sha"
	resp := f.deferMerge(t, generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head})
	require.Equal(202, resp.StatusCode, string(resp.Body))

	f.enableCall(t)
	assert.False(t, f.workerQueued(t), "the queue's auto-merge outlives a restart; the worker does not")
}

func TestDeferMergeRunsWorkerForAPullRequestTheProviderCouldMergeNow(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{allowed: true, mergeableNow: true})

	head := "head-sha"
	resp := f.deferMerge(t, generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head})
	require.Equal(202, resp.StatusCode, string(resp.Body))

	assert.Empty(t, f.provider.enableCh, "GitHub refuses auto-merge for a pull request it could merge now")
	assert.True(t, f.workerQueued(t))
}

func TestDeferMergeWithBypassConsentSkipsProviderAutoMerge(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{allowed: true})

	head, bypass := "head-sha", true
	resp := f.deferMerge(t, generated.MergePRInputBody{
		Method: "squash", ExpectedHeadSha: &head, BypassRules: &bypass,
	})
	require.Equal(202, resp.StatusCode, string(resp.Body))

	assert.Empty(t, f.provider.enableCh, "only Forge's worker merges over the rules")
	assert.True(t, f.workerQueued(t))
}

func TestDeferMergeWithBypassConsentRejectsWhileAutoMergeIsOn(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{allowed: true})
	// Auto-merge is on at the provider, though Forge has not synced it.
	f.provider.setAutoMerge(true)

	head, bypass := "head-sha", true
	resp := f.deferMerge(t, generated.MergePRInputBody{
		Method: "squash", ExpectedHeadSha: &head, BypassRules: &bypass,
	})
	require.Equal(409, resp.StatusCode, string(resp.Body))
	require.Contains(string(resp.Body), "already_pending")
	assert.False(t, f.workerQueued(t), "a worker would merge beside the provider's auto-merge")
}

func TestDeferMergeReportsAutoMergeRefusalWithoutStartingWorker(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{
		allowed: true,
		enableErr: &platform.Error{
			Code: platform.ErrCodeConflict, Err: errors.New("pull request is in clean status"),
		},
	})

	head := "head-sha"
	resp := f.deferMerge(t, generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head})
	require.Equal(409, resp.StatusCode, string(resp.Body))

	f.enableCall(t)
	assert.False(t, f.workerQueued(t))
}

func TestDeferMergeFailsWhenProviderAutoMergeAnswerIsLost(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{
		allowed: true, enableErr: errors.New("graphql status 502 Bad Gateway"),
	})

	head := "head-sha"
	resp := f.deferMerge(t, generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head})
	require.Equal(502, resp.StatusCode, string(resp.Body))
	require.Contains(string(resp.Body), "mutationOutcomeUnknown")

	f.enableCall(t)
	assert.False(t, f.workerQueued(t), "GitHub may have enabled auto-merge; a worker would merge alongside it")
}

func TestDeferMergeFailsWhenAutoMergeStateIsUnreadable(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{
		allowed: true, stateErr: errors.New("graphql status 502 Bad Gateway"),
	})

	head := "head-sha"
	resp := f.deferMerge(t, generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head})
	require.Equal(502, resp.StatusCode, string(resp.Body))
	require.Contains(string(resp.Body), `"code":"upstreamError"`, "nothing was sent, so the outcome is known")

	assert.Empty(t, f.provider.enableCh)
	assert.False(t, f.workerQueued(t), "an earlier lost answer may have left auto-merge on")
}

func TestDeferMergeKeepsWaitingWorkerWithoutAskingProvider(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{allowed: true})

	head, bypass := "head-sha", true
	first := f.deferMerge(t, generated.MergePRInputBody{
		Method: "squash", ExpectedHeadSha: &head, BypassRules: &bypass,
	})
	require.Equal(202, first.StatusCode, string(first.Body))

	again := f.deferMerge(t, generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head})
	require.Equal(409, again.StatusCode, string(again.Body))
	require.Contains(string(again.Body), "already_pending")
	assert.Empty(t, f.provider.enableCh, "a waiting worker already owns the merge")
}

func TestDeferralHoldsMergeLockWhileProviderEnablesAutoMerge(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{allowed: true})
	f.provider.enableRelease = make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(f.provider.enableRelease) }) }
	// Runs before the fixture's shutdown, which waits for held requests.
	t.Cleanup(release)

	head := "head-sha"
	normal := make(chan *generated.DeferMergePullOnHostResp, 1)
	go func() {
		resp, _ := f.client.HTTP.DeferMergePullOnHostWithResponse(context.WithoutCancel(t.Context()), &generated.DeferMergePullOnHostRequestOptions{
			PathParams: &generated.DeferMergePullOnHostPath{
				PlatformHost: f.ref.Host, Provider: "gitlab", Owner: f.ref.Owner, Name: f.ref.Name, Number: 7,
			},
			Body: &generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head},
		})
		normal <- resp
	}()
	<-f.provider.enableCh // the deferral is inside the provider's call

	// A deferral with bypass consent must take this lock before it can admit
	// Forge's worker, so it cannot start one while auto-merge is being enabled.
	repo, err := f.database.GetRepoByID(t.Context(), f.repoID)
	require.NoError(err)
	unlock, free := f.srv.handler.mergeLocks.TryLock(deferredMergeKey(*repo, 7))
	if free {
		unlock()
	}
	require.False(free, "the provider call must run under the pull request's merge lock")

	release()
	resp := <-normal
	require.NotNil(resp)
	require.Equal(202, resp.StatusCode, string(resp.Body))

	bypass := true
	consenting := f.deferMerge(t, generated.MergePRInputBody{
		Method: "squash", ExpectedHeadSha: &head, BypassRules: &bypass,
	})
	require.Equal(409, consenting.StatusCode, string(consenting.Body))
	require.Contains(string(consenting.Body), "already_pending")
	assert.False(t, f.workerQueued(t))
}
