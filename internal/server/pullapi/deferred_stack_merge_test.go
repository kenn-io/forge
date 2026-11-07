package pullapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// nativeStackDeferFixture is a native stack of #6 below #7 on a provider whose
// repository allows auto-merge. #7's CI is pending as stored and passes on
// the provider. A deferred merge of #7 lands #6 with it.
type nativeStackDeferFixture struct {
	*autoMergeFixture
	stackID int64
}

type nativeStackDeferOptions struct {
	// bottom is #6 as stored; its PlatformHeadSHA is "bottom-head".
	bottom db.MergeRequest
	// bottomChecks are the checks the provider reports for #6.
	bottomChecks []platform.CICheck
	// targetChecks replaces the passed checks the provider reports for #7.
	targetChecks []platform.CICheck
	// holdCI holds the worker's first CI read until it is closed.
	holdCI  chan struct{}
	maxWait time.Duration
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
	if opts.targetChecks != nil {
		targetChecks = opts.targetChecks
	}
	inner := &deferredMergeTestProvider{
		ref: ref,
		mergeRequests: []platform.MergeRequest{
			{
				Repo: ref, PlatformID: 7001, Number: 7, Title: "Defer merge", Author: "ada", State: "open",
				HeadBranch: "feature", BaseBranch: "main",
				HeadSHA: "head-sha", BaseSHA: "base-sha", CIStatus: "success",
				CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
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
		mergeCh: make(chan deferredMergeTestMergeCall, 1),
	}
	if opts.holdCI != nil {
		inner.ciStarted, inner.ciRelease = make(chan struct{}), opts.holdCI
	}
	provider := &autoMergeTestProvider{
		deferredMergeTestProvider: inner,
		allowed:                   true,
		enableCh:                  make(chan deferredMergeTestMergeCall, 2),
	}
	srv, database, repoID, client := newDeferredMergeRouteServer(
		t, provider, ref, now,
		[]db.CICheck{{App: "GitLab", Name: "pipeline", Status: "in_progress"}},
		deferredMergeTestOptions{deferredMergeMaxWait: opts.maxWait},
	)
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
	return &nativeStackDeferFixture{
		autoMergeFixture: &autoMergeFixture{
			provider: provider, srv: srv, database: database, repoID: repoID, client: client, ref: ref,
		},
		stackID: stackID,
	}
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

func passedChecksJSON(t *testing.T) string {
	t.Helper()
	return mustDeferredMergeChecksJSON(t, []db.CICheck{
		{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"},
	})
}

var passedChecks = []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"}}

func TestDeferMergeInNativeStackRunsWorkerInsteadOfProviderAutoMerge(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		bottom:       db.MergeRequest{CIStatus: "success", CIChecksJSON: passedChecksJSON(t)},
		bottomChecks: passedChecks,
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))

	completed := f.completion(t)
	assert.Equal(t, "merged", completed.Status, completed.Error)
	select {
	case call := <-f.provider.mergeCh:
		assert.Equal(t, 7, call.Number)
		assert.Equal(t, "head-sha", call.ExpectedHeadSHA)
	default:
		require.FailNow(t, "the worker did not merge the stack")
	}
	f.assertNoAutoMergeRequest(t)
}

// A member below that waiting cannot make ready refuses the merge up front.
func TestDeferMergeInNativeStackRejectsAMemberBelowThatIsNotReady(t *testing.T) {
	for _, tt := range []struct {
		name   string
		bottom db.MergeRequest
		want   string
	}{
		{
			name: "failed CI",
			bottom: db.MergeRequest{CIStatus: "failure", CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{
				{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "failure"},
			})},
			want: "CI failed on #6",
		},
		{
			name:   "draft",
			bottom: db.MergeRequest{IsDraft: true, CIStatus: "success", CIChecksJSON: passedChecksJSON(t)},
			want:   "#6 is a draft",
		},
		{
			name:   "conflicts",
			bottom: db.MergeRequest{MergeableState: "dirty", CIStatus: "success", CIChecksJSON: passedChecksJSON(t)},
			want:   "#6 has merge conflicts",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newNativeStackDeferFixture(t, nativeStackDeferOptions{bottom: tt.bottom})

			resp := f.deferTarget(t)
			require.Equal(t, http.StatusConflict, resp.StatusCode, string(resp.Body))
			assert.Contains(t, string(resp.Body), `"reason":"stack_member_not_ready"`)
			assert.Contains(t, string(resp.Body), `"blocking_number":6`)
			assert.Contains(t, string(resp.Body), tt.want)
			assert.False(t, f.workerQueued(t))
			f.assertNoAutoMergeRequest(t)
		})
	}
}

// The target's own checks pass at once, so only the member below keeps the
// worker waiting.
func TestDeferMergeInNativeStackWaitsForCIOnAMemberBelow(t *testing.T) {
	running := []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "in_progress"}}
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		bottom: db.MergeRequest{CIStatus: "pending", CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{
			{App: "GitLab", Name: "pipeline", Status: "in_progress"},
		})},
		bottomChecks: running,
		maxWait:      100 * time.Millisecond, //nolint:kennlint // deadlinetest: the member below must still be running when the wait expires
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))

	completed := f.completion(t)
	assert.Equal(t, "failed", completed.Status)
	assert.Contains(t, completed.Error, "timed out waiting for pending CI checks")
	f.assertNoMerge(t)
}

// A member below that fails while the target still runs ends the wait, as a
// failure on the target does.
func TestDeferMergeInNativeStackFailsWhenAMemberBelowFailsWhileTheTargetRuns(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		targetChecks: []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "in_progress"}},
		bottom:       db.MergeRequest{CIStatus: "success", CIChecksJSON: passedChecksJSON(t)},
		bottomChecks: []platform.CICheck{
			{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "failure"},
		},
		maxWait: 200 * time.Millisecond, //nolint:kennlint // deadlinetest: without the check below, the wait would end only by timing out
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

	completed := f.completion(t)
	assert.Equal(t, "merged", completed.Status, completed.Error)
}

// A member below that was ready when the merge was queued is checked again
// before the stack merge lands it.
func TestDeferMergeInNativeStackFailsWhenAMemberBelowFailsBeforeTheMerge(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		bottom: db.MergeRequest{CIStatus: "success", CIChecksJSON: passedChecksJSON(t)},
		bottomChecks: []platform.CICheck{
			{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "failure"},
		},
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))

	completed := f.completion(t)
	assert.Equal(t, "failed", completed.Status)
	assert.Contains(t, completed.Error, "CI failed on #6")
	f.assertNoMerge(t)
}

// The members checked before the merge are the ones recorded when it was
// queued, so a stack that Forge stops recording as native still has them
// checked.
func TestDeferMergeInNativeStackChecksTheQueuedMembersWhenTheStackStopsBeingNative(t *testing.T) {
	hold := make(chan struct{})
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{
		bottom: db.MergeRequest{CIStatus: "success", CIChecksJSON: passedChecksJSON(t)},
		bottomChecks: []platform.CICheck{
			{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "failure"},
		},
		holdCI: hold,
	})

	resp := f.deferTarget(t)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(resp.Body))
	select {
	case <-f.provider.ciStarted:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the worker did not read CI")
	}
	_, err := f.database.UpsertStack(t.Context(), f.repoID, 6, "feature", false)
	require.NoError(t, err)
	close(hold)

	completed := f.completion(t)
	assert.Equal(t, "failed", completed.Status)
	assert.Contains(t, completed.Error, "CI failed on #6")
	f.assertNoMerge(t)
}

// The stack lands on the bottom member's base, so that branch's queue exempts
// the merge from the rules, not the queue state of the target's own base.
func TestDeferredMergeRulesUseTheBottomMembersQueueInANativeStack(t *testing.T) {
	f := newNativeStackDeferFixture(t, nativeStackDeferOptions{})
	ctx := t.Context()
	_, err := f.database.UpsertMergeQueueState(ctx, f.repoID, db.MergeQueueState{
		Number: 6, BaseBranch: "main", Enabled: true, MergeMethod: "squash",
		ObservedAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	repo, err := f.database.GetRepoByID(ctx, f.repoID)
	require.NoError(t, err)

	rules, queue := f.srv.handler.deferredMergeRules(ctx, *repo, platform.MergeRequest{
		Number: 7, BaseBranch: "bottom", MergeableState: "blocked",
	})

	assert.Equal(t, "blocked", rules)
	assert.True(t, queue, "the queue on the bottom member's base exempts the stack merge")
}
