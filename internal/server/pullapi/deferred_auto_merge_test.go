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
	enableErr error
	enableCh  chan deferredMergeTestMergeCall
	// enableRelease, when set, holds each request inside the provider call.
	enableRelease chan struct{}
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

func newAutoMergeFixture(t *testing.T, enableErr error) *autoMergeFixture {
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
		enableErr: enableErr,
		enableCh:  make(chan deferredMergeTestMergeCall, 2),
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

func (f *autoMergeFixture) workerQueued(t *testing.T) bool {
	t.Helper()
	repo, err := f.database.GetRepoByID(t.Context(), f.repoID)
	require.NoError(t, err)
	return f.srv.handler.isDeferredMergePending(*repo, 7)
}

func TestDeferMergeHandsOffToProviderAutoMerge(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, nil)

	title, message, head := "Ship it", "Body", "head-sha"
	resp := f.deferMerge(t, generated.MergePRInputBody{
		Method: "squash", CommitTitle: title, CommitMessage: message, ExpectedHeadSha: &head,
	})
	require.Equal(202, resp.StatusCode, string(resp.Body))

	require.Equal(deferredMergeTestMergeCall{
		Number: 7, CommitTitle: title, CommitMessage: message, Method: "squash", ExpectedHeadSHA: head,
	}, <-f.provider.enableCh)
	assert.False(t, f.workerQueued(t), "the provider merges; Forge runs no worker")
	assert.True(t, f.pending(t))

	again := f.deferMerge(t, generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head})
	require.Equal(409, again.StatusCode, string(again.Body))
	require.Contains(string(again.Body), "already_pending")
	assert.Empty(t, f.provider.enableCh)

	// The provider cancels auto-merge (a push without write access, or a
	// user on the provider), and the next sync reads it off.
	f.provider.setAutoMerge(false)
	require.NoError(f.srv.handler.syncer.SyncMROnProvider(
		t.Context(), f.ref.Platform, f.ref.Host, f.ref.Owner, f.ref.Name, 7,
	))
	assert.False(t, f.pending(t))
}

func TestDeferMergeRunsWorkerWhenProviderRefusesAutoMerge(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, &platform.Error{
		Code: platform.ErrCodeConflict, Err: errors.New("auto-merge is not allowed for this repository"),
	})

	head := "head-sha"
	resp := f.deferMerge(t, generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head})
	require.Equal(202, resp.StatusCode, string(resp.Body))

	<-f.provider.enableCh
	assert.True(t, f.workerQueued(t))
	assert.True(t, f.pending(t))
	mr, err := f.database.GetMergeRequestByRepoIDAndNumber(t.Context(), f.repoID, 7)
	require.NoError(err)
	assert.False(t, mr.AutoMergeEnabled)
}

func TestDeferMergeWithBypassConsentSkipsProviderAutoMerge(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, nil)

	head, bypass := "head-sha", true
	resp := f.deferMerge(t, generated.MergePRInputBody{
		Method: "squash", ExpectedHeadSha: &head, BypassRules: &bypass,
	})
	require.Equal(202, resp.StatusCode, string(resp.Body))

	assert.Empty(t, f.provider.enableCh, "only Forge's worker merges over the rules")
	assert.True(t, f.workerQueued(t))
}

func TestDeferMergeFailsWhenProviderAutoMergeAnswerIsLost(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, errors.New("graphql status 502 Bad Gateway"))

	head := "head-sha"
	resp := f.deferMerge(t, generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head})
	require.Equal(502, resp.StatusCode, string(resp.Body))
	require.Contains(string(resp.Body), "mutationOutcomeUnknown")

	<-f.provider.enableCh
	assert.False(t, f.workerQueued(t), "GitHub may have enabled auto-merge; a worker would merge alongside it")
	assert.False(t, f.pending(t))
}

func TestDeferMergeKeepsWaitingWorkerWithoutAskingProvider(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, nil)

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

func TestConcurrentDeferralsNeverStartBothExecutors(t *testing.T) {
	require := require.New(t)
	f := newAutoMergeFixture(t, nil)
	f.provider.enableRelease = make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(f.provider.enableRelease) }) }
	// Runs before the fixture's shutdown, which waits for held requests.
	t.Cleanup(release)

	head, bypass := "head-sha", true
	normal := make(chan *generated.DeferMergePullOnHostResp, 1)
	go func() {
		normal <- f.deferMerge(t, generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head})
	}()
	<-f.provider.enableCh // the first deferral is inside GitHub's call

	consenting := make(chan *generated.DeferMergePullOnHostResp, 1)
	go func() {
		consenting <- f.deferMerge(t, generated.MergePRInputBody{
			Method: "squash", ExpectedHeadSha: &head, BypassRules: &bypass,
		})
	}()
	select {
	case resp := <-consenting:
		t.Fatalf("a deferral with bypass consent was admitted while auto-merge was being enabled: %d %s", resp.StatusCode, resp.Body)
	case <-time.After(100 * time.Millisecond):
	}
	release()

	require.Equal(202, (<-normal).StatusCode)
	second := <-consenting
	require.Equal(409, second.StatusCode, string(second.Body))
	require.Contains(string(second.Body), "already_pending")
	assert.False(t, f.workerQueued(t))
}
