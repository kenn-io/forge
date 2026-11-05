package pullapi

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// nativeStackDeferFixture is a native stack of #6 below #7 on a provider whose
// repository allows auto-merge. A deferred merge of #7 lands #6 with it.
type nativeStackDeferFixture struct {
	*autoMergeFixture
}

type nativeStackDeferOptions struct {
	// bottom is #6 as stored; its PlatformHeadSHA is "bottom-head".
	bottom db.MergeRequest
	// bottomChecks are the checks the provider reports for #6.
	bottomChecks []platform.CICheck
	// targetPassed stores #7 with CI already passed instead of pending.
	targetPassed bool
	// targetNoCI stores #7 without CI, and the provider reports none for it.
	targetNoCI bool
	// targetChecks replaces the passed checks the provider reports for #7.
	targetChecks []platform.CICheck
	// bottomQueue gives #6's base branch an enabled merge queue, and the
	// provider reports #7 blocked, as a branch that requires a queue does.
	bottomQueue bool
	maxWait     time.Duration
}

func newNativeStackDeferFixture(t *testing.T, opts nativeStackDeferOptions) *nativeStackDeferFixture {
	t.Helper()
	ctx := t.Context()
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
	targetChecks := []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"}}
	switch {
	case opts.targetNoCI:
		targetChecks = []platform.CICheck{}
	case opts.targetChecks != nil:
		targetChecks = opts.targetChecks
	}
	targetMergeableState := ""
	if opts.bottomQueue {
		targetMergeableState = "blocked"
	}
	provider := &autoMergeTestProvider{
		deferredMergeTestProvider: &deferredMergeTestProvider{
			ref: ref,
			mergeRequests: []platform.MergeRequest{
				{
					Repo: ref, PlatformID: 7001, Number: 7, Title: "Defer merge", Author: "ada", State: "open",
					HeadBranch: "feature", BaseBranch: "main",
					HeadSHA: "head-sha", BaseSHA: "base-sha", CIStatus: "success",
					MergeableState: targetMergeableState,
					CreatedAt:      now, UpdatedAt: now, LastActivityAt: now,
				},
				{
					Repo: ref, PlatformID: 6001, Number: 6, Title: "Bottom", Author: "ada", State: "open",
					HeadBranch: "bottom", BaseBranch: "main",
					HeadSHA: "bottom-head", BaseSHA: "base-sha",
					CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
				},
			},
			ciChecks: map[string][]platform.CICheck{
				"head-sha":    targetChecks,
				"bottom-head": opts.bottomChecks,
			},
			mergeCh:    make(chan deferredMergeTestMergeCall, 1),
			mergeQueue: opts.bottomQueue,
			enqueueCh:  make(chan int, 1),
		},
		allowed:  true,
		enableCh: make(chan deferredMergeTestMergeCall, 2),
	}
	srv, database, repoID, client := newDeferredMergeRouteServer(
		t, provider, ref, now,
		[]db.CICheck{{App: "GitLab", Name: "pipeline", Status: "in_progress"}},
		deferredMergeTestOptions{deferredMergeMaxWait: opts.maxWait},
	)
	if opts.targetPassed || opts.targetNoCI {
		target := &db.MergeRequest{
			RepoID: repoID, PlatformID: 7001, Number: 7, Title: "Defer merge", Author: "ada",
			State: db.MergeRequestStateOpen, HeadBranch: "feature", BaseBranch: "main",
			PlatformHeadSHA: "head-sha", PlatformBaseSHA: "base-sha",
			CIStatus: "success", CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{
				{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"},
			}),
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		}
		if opts.targetNoCI {
			target.CIStatus, target.CIChecksJSON = "", ""
		}
		_, err := database.UpsertMergeRequest(ctx, target)
		require.NoError(t, err)
	}
	if opts.bottomQueue {
		_, err := database.UpsertMergeQueueState(ctx, repoID, db.MergeQueueState{
			Number: 6, BaseBranch: "main", Enabled: true, MergeMethod: "squash", ObservedAt: now,
		})
		require.NoError(t, err)
	}
	bottom := opts.bottom
	bottom.RepoID, bottom.PlatformID, bottom.Number = repoID, 6001, 6
	bottom.Title, bottom.Author = "Bottom", "ada"
	if bottom.State == "" {
		bottom.State = db.MergeRequestStateOpen
	}
	bottom.HeadBranch, bottom.BaseBranch = "bottom", "main"
	bottom.PlatformHeadSHA, bottom.PlatformBaseSHA = "bottom-head", "base-sha"
	bottom.CreatedAt, bottom.UpdatedAt, bottom.LastActivityAt = now, now, now
	bottomID, err := database.UpsertMergeRequest(ctx, &bottom)
	require.NoError(t, err)
	target, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(t, err)
	stackID, err := database.UpsertStack(ctx, repoID, 6, "feature", true)
	require.NoError(t, err)
	require.NoError(t, database.ReplaceStackMembers(ctx, stackID, []db.StackMember{
		{StackID: stackID, MergeRequestID: bottomID, Position: 1},
		{StackID: stackID, MergeRequestID: target.ID, Position: 2},
	}))
	return &nativeStackDeferFixture{&autoMergeFixture{
		provider: provider, srv: srv, database: database, repoID: repoID, client: client, ref: ref,
	}}
}

func (f *nativeStackDeferFixture) deferTarget(t *testing.T) *generated.DeferMergePullOnHostResp {
	t.Helper()
	head := "head-sha"
	return f.deferMerge(t, generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head})
}

func (f *nativeStackDeferFixture) assertNoAutoMergeRequest(t *testing.T) {
	t.Helper()
	select {
	case call := <-f.provider.enableCh:
		assert.Failf(t, "provider auto-merge was requested", "call: %+v", call)
	default:
	}
}

func (f *nativeStackDeferFixture) assertNoMerge(t *testing.T) {
	t.Helper()
	select {
	case call := <-f.provider.mergeCh:
		assert.Failf(t, "unexpected merge", "merge call: %+v", call)
	default:
	}
}

// requireMerge waits for the worker to merge #7, which lands the stack.
func (f *nativeStackDeferFixture) requireMerge(t *testing.T) {
	t.Helper()
	events, _ := f.srv.Hub().Subscribe(t.Context(), false)
	for {
		select {
		case call := <-f.provider.mergeCh:
			assert.Equal(t, 7, call.Number)
			return
		case ev := <-events:
			if payload, ok := ev.Event.Data.(DeferredMergeCompletedPayload); ok && payload.Status == "failed" {
				require.FailNow(t, "the deferred merge failed", payload.Error)
			}
		case <-time.After(5 * time.Second):
			require.FailNow(t, "the worker did not merge the stack")
		}
	}
}

// completion waits for the worker's deferred_merge_completed event.
func (f *nativeStackDeferFixture) completion(t *testing.T) DeferredMergeCompletedPayload {
	t.Helper()
	events, _ := f.srv.Hub().Subscribe(t.Context(), false)
	for {
		select {
		case ev := <-events:
			if payload, ok := ev.Event.Data.(DeferredMergeCompletedPayload); ok {
				return payload
			}
		case <-time.After(5 * time.Second):
			require.FailNow(t, "timed out waiting for the deferred merge to finish")
		}
	}
}

func TestDeferMergeInNativeStackRunsWorkerInsteadOfProviderAutoMerge(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		bottom: db.MergeRequest{CIStatus: "success", CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{
			{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"},
		})},
		bottomChecks: []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"}},
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))

	select {
	case call := <-f.provider.mergeCh:
		assert.Equal(t, 7, call.Number)
		assert.Equal(t, "head-sha", call.ExpectedHeadSHA)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the worker did not merge the stack")
	}
	f.assertNoAutoMergeRequest(t)
}

func TestDeferMergeInNativeStackRejectsAMemberBelowWithFailedCI(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		bottom: db.MergeRequest{CIStatus: "failure", CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{
			{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "failure"},
		})},
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(resp.Body))
	assert.Contains(t, string(resp.Body), `"reason":"ci_failed"`)
	assert.Contains(t, string(resp.Body), `"blocking_number":6`)
	assert.Contains(t, string(resp.Body), "CI failed on #6")
	assert.False(t, f.workerQueued(t))
	f.assertNoAutoMergeRequest(t)
}

func TestDeferMergeInNativeStackRejectsADraftMemberBelow(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		bottom: db.MergeRequest{IsDraft: true, CIStatus: "pending"},
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(resp.Body))
	assert.Contains(t, string(resp.Body), `"reason":"stack_member_not_ready"`)
	assert.Contains(t, string(resp.Body), "#6 is a draft")
	assert.False(t, f.workerQueued(t))
}

// The target's own checks have passed, so only the member below gives the
// worker something to wait for.
func TestDeferMergeInNativeStackWaitsForAMemberBelow(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		targetPassed: true,
		bottom: db.MergeRequest{CIStatus: "pending", CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{
			{App: "GitLab", Name: "pipeline", Status: "in_progress"},
		})},
		bottomChecks: []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "in_progress"}},
		maxWait:      50 * time.Millisecond, //nolint:kennlint // deadlinetest: the member below must still be running when the wait expires
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))

	completed := f.completion(t)
	assert.Equal(t, "failed", completed.Status)
	assert.Contains(t, completed.Error, "timed out waiting for pending CI checks")
	f.assertNoMerge(t)
	f.assertNoAutoMergeRequest(t)
}

func TestDeferMergeInNativeStackFailsWhenAMemberBelowFailsWhileWaiting(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		bottom: db.MergeRequest{CIStatus: "pending", CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{
			{App: "GitLab", Name: "pipeline", Status: "in_progress"},
		})},
		bottomChecks: []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "failure"}},
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))

	completed := f.completion(t)
	assert.Equal(t, "failed", completed.Status)
	assert.Contains(t, completed.Error, "CI failed on #6")
	f.assertNoMerge(t)
}

// A check that was pending on the member below when the merge was queued must
// pass; a refresh that no longer reports it keeps the worker waiting.
func TestDeferMergeInNativeStackWaitsForACheckThatDisappearsBelow(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		targetPassed: true,
		bottom: db.MergeRequest{CIStatus: "pending", CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{
			{App: "GitLab", Name: "pipeline", Status: "in_progress"},
			{App: "GitLab", Name: "lint", Status: "in_progress"},
		})},
		bottomChecks: []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"}},
		maxWait:      50 * time.Millisecond, //nolint:kennlint // deadlinetest: the missing check must still be outstanding when the wait expires
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))

	completed := f.completion(t)
	assert.Equal(t, "failed", completed.Status)
	assert.Contains(t, completed.Error, "timed out waiting for pending CI checks")
	f.assertNoMerge(t)
}

// A member below that fails while the target still runs CI ends the wait, as a
// failure on the target does, so a later rerun cannot merge the stack.
func TestDeferMergeInNativeStackFailsWhenAMemberBelowFailsWhileTheTargetRuns(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		targetChecks: []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "in_progress"}},
		bottom: db.MergeRequest{CIStatus: "pending", CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{
			{App: "GitLab", Name: "pipeline", Status: "in_progress"},
		})},
		bottomChecks: []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "failure"}},
		maxWait:      200 * time.Millisecond, //nolint:kennlint // deadlinetest: without the check below, the wait would end only by timing out
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))

	completed := f.completion(t)
	assert.Equal(t, "failed", completed.Status)
	assert.Contains(t, completed.Error, "CI failed on #6")
	f.assertNoMerge(t)
}

func TestDeferMergeInNativeStackMergesWithAMemberBelowThatHasNoCI(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))

	f.requireMerge(t)
}

func TestDeferMergeInNativeStackMergesATargetThatHasNoCI(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		targetNoCI: true,
		bottom: db.MergeRequest{CIStatus: "pending", CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{
			{App: "GitLab", Name: "pipeline", Status: "in_progress"},
		})},
		bottomChecks: []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"}},
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))

	f.requireMerge(t)
}

// The stack lands on the bottom member's base, so that branch's queue exempts
// the merge from the rules, not the queue state of the target's own base.
func TestDeferMergeInNativeStackEnqueuesOnTheBottomMembersQueue(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		targetPassed: true,
		bottomQueue:  true,
		bottom: db.MergeRequest{CIStatus: "pending", CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{
			{App: "GitLab", Name: "pipeline", Status: "in_progress"},
		})},
		bottomChecks: []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"}},
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))

	events, _ := f.srv.Hub().Subscribe(t.Context(), false)
	for {
		select {
		case number := <-f.provider.enqueueCh:
			assert.Equal(t, 7, number)
			f.assertNoMerge(t)
			return
		case ev := <-events:
			if payload, ok := ev.Event.Data.(DeferredMergeCompletedPayload); ok && payload.Status == "failed" {
				require.FailNow(t, "the deferred merge failed", payload.Error)
			}
		case <-time.After(5 * time.Second):
			require.FailNow(t, "the worker did not add the stack to the merge queue")
		}
	}
}

// emptyAfterFirstCIProvider reports pending CI on its first read and none
// after it.
type emptyAfterFirstCIProvider struct {
	*deferredMergeTestProvider
	reads atomic.Int32
}

var _ platform.CIReader = (*emptyAfterFirstCIProvider)(nil)

func (p *emptyAfterFirstCIProvider) ListCIChecks(
	ctx context.Context, ref platform.RepoRef, sha string,
) ([]platform.CICheck, error) {
	if p.reads.Add(1) == 1 {
		return []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "in_progress"}}, nil
	}
	return []platform.CICheck{}, nil
}

// A pull request stored without CI whose admission refresh finds a pending
// check must wait for that check: a later refresh with no CI fails closed
// instead of counting as a pull request that never had CI.
func TestDeferMergeWaitsForAPendingCheckFoundAtAdmissionOnAPullRequestStoredWithoutCI(t *testing.T) {
	ctx := t.Context()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	ref := platform.RepoRef{
		Platform: platform.KindGitLab, Host: "gitlab.example.com", Owner: "group", Name: "project",
		RepoPath: "group/project", Key: platform.RepositoryIDKey(4242), DefaultBranch: "main",
	}
	provider := &emptyAfterFirstCIProvider{deferredMergeTestProvider: &deferredMergeTestProvider{
		ref: ref,
		mergeRequests: []platform.MergeRequest{{
			Repo: ref, PlatformID: 7001, Number: 7, Title: "Defer merge", Author: "ada", State: "open",
			HeadBranch: "feature", BaseBranch: "main", HeadSHA: "head-sha", BaseSHA: "base-sha",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		}},
		mergeCh: make(chan deferredMergeTestMergeCall, 1),
	}}
	srv, database, repoID, client := newDeferredMergeRouteServer(t, provider, ref, now, nil)
	require.NoError(t, database.UpdateMRCIStatus(ctx, repoID, 7, "", ""))
	events, _ := srv.Hub().Subscribe(ctx, false)

	head := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(ctx, &generated.DeferMergePullOnHostRequestOptions{
		PathParams: &generated.DeferMergePullOnHostPath{
			PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: 7,
		},
		Body: &generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &head},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))
	require.NotNil(t, resp.JSON202)
	require.EqualValues(t, 1, resp.JSON202.PendingChecks, "admission found the pending check")

	for {
		select {
		case call := <-provider.mergeCh:
			require.Failf(t, "merged before the pending check passed", "merge call: %+v", call)
		case ev := <-events:
			if payload, ok := ev.Event.Data.(DeferredMergeCompletedPayload); ok {
				assert.Equal(t, "failed", payload.Status)
				assert.Contains(t, payload.Error, "aggregate CI status is unavailable")
				return
			}
		case <-time.After(5 * time.Second):
			require.FailNow(t, "timed out waiting for the deferred merge to finish")
		}
	}
}
