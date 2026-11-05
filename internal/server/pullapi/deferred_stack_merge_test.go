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
	maxWait      time.Duration
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
	passed := []platform.CICheck{{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"}}
	provider := &autoMergeTestProvider{
		deferredMergeTestProvider: &deferredMergeTestProvider{
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
				"head-sha":    passed,
				"bottom-head": opts.bottomChecks,
			},
			mergeCh: make(chan deferredMergeTestMergeCall, 1),
		},
		allowed:  true,
		enableCh: make(chan deferredMergeTestMergeCall, 2),
	}
	srv, database, repoID, client := newDeferredMergeRouteServer(
		t, provider, ref, now,
		[]db.CICheck{{App: "GitLab", Name: "pipeline", Status: "in_progress"}},
		deferredMergeTestOptions{deferredMergeMaxWait: opts.maxWait},
	)
	if opts.targetPassed {
		_, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID: repoID, PlatformID: 7001, Number: 7, Title: "Defer merge", Author: "ada",
			State: db.MergeRequestStateOpen, HeadBranch: "feature", BaseBranch: "main",
			PlatformHeadSHA: "head-sha", PlatformBaseSHA: "base-sha",
			CIStatus: "success", CIChecksJSON: mustDeferredMergeChecksJSON(t, []db.CICheck{
				{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"},
			}),
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
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
