package pullapi

import (
	"net/http"
	"testing"
	"time"

	"go.kenn.io/forge/internal/platformdb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

func newMidStackMergeFixture(
	t *testing.T, native bool,
) (*deferredMergeRouteServer, *deferredMergeTestProvider, *apiclient.Client) {
	t.Helper()
	ctx := t.Context()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	ref := platform.RepoRef{
		Platform:      platform.KindGitLab,
		Host:          "gitlab.example.com",
		Owner:         "group",
		Name:          "project",
		RepoPath:      "group/project",
		Key:           platform.RepositoryIDKey(4242),
		DefaultBranch: "main",
	}
	provider := &deferredMergeTestProvider{
		ref: ref,
		mergeRequests: []platform.MergeRequest{
			{
				Repo: ref, PlatformID: 7001, Number: 1, State: "open",
				HeadBranch: "bottom", BaseBranch: "main",
				HeadSHA: "bottom-head", BaseSHA: "base-sha",
			},
			{
				Repo: ref, PlatformID: 7002, Number: 2, State: "open",
				HeadBranch: "middle", BaseBranch: "bottom",
				HeadSHA: "middle-head", BaseSHA: "bottom-head",
			},
		},
		mergeCh: make(chan deferredMergeTestMergeCall, 1),
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)
	database := dbtest.Open(t)
	repoID, err := reposeed.Seed(ctx, database, platformdb.DBRepoIdentity(ref))
	require.NoError(t, err)
	bottomID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 7001, Number: 1, State: db.MergeRequestStateOpen,
		Title: "Bottom", HeadBranch: "bottom", BaseBranch: "main",
		PlatformHeadSHA: "bottom-head", PlatformBaseSHA: "base-sha",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(t, err)
	middleID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 7002, Number: 2, State: db.MergeRequestStateOpen,
		Title: "Middle", HeadBranch: "middle", BaseBranch: "bottom",
		PlatformHeadSHA: "middle-head", PlatformBaseSHA: "bottom-head",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(t, err)
	stackID, err := database.UpsertStack(ctx, repoID, 1, "feature", native)
	require.NoError(t, err)
	require.NoError(t, database.ReplaceStackMembers(ctx, stackID, []db.StackMember{
		{StackID: stackID, MergeRequestID: bottomID, Position: 1},
		{StackID: stackID, MergeRequestID: middleID, Position: 2},
	}))

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform: platform.KindGitLab, PlatformHost: ref.Host,
			Owner: ref.Owner, Name: ref.Name, RepoPath: ref.RepoPath,
		}},
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{
			ghclient.RateBucketKey("gitlab", ref.Host, "host"): ghclient.NewSyncBudget(100),
		},
	)
	t.Cleanup(syncer.Stop)
	server, client := newDeferredMergeHTTPFixture(t, database, syncer, now, 0)
	return server, provider, client
}

func TestMergePRRejectsMidStackMergeByDefault(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	server, provider, client := newMidStackMergeFixture(t, false)

	resp, err := client.HTTP.MergePullOnHostWithResponse(t.Context(), &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gitlab", Owner: "group", Name: "project", Number: int64(2)}, Body: &generated.MergePRInputBody{
		Method:          "squash",
		ExpectedHeadSha: new("middle-head"),
	}})
	require.Error(err)
	require.NotNil(resp)
	assert.Equal(http.StatusConflict, resp.StatusCode)
	assert.Contains(string(resp.Body), `"reason":"mid_stack_merge_disallowed"`)
	assert.Contains(string(resp.Body), `"blocking_number":1`)
	assert.False(server.handler.allowMidStackMerges())
	select {
	case call := <-provider.mergeCh:
		require.Failf("unexpected merge", "merge call: %+v", call)
	default:
	}
}

func TestMergePRAllowsMidStackMergeFromCommittedConfigSnapshot(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	server, provider, client := newMidStackMergeFixture(t, false)
	server.handler.ApplyConfig(ConfigSnapshot{AllowMidStackMerges: true})

	resp, err := client.HTTP.MergePullOnHostWithResponse(t.Context(), &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gitlab", Owner: "group", Name: "project", Number: int64(2)}, Body: &generated.MergePRInputBody{
		Method:          "squash",
		ExpectedHeadSha: new("middle-head"),
	}})
	require.NoError(err)
	assert.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	select {
	case call := <-provider.mergeCh:
		assert.Equal(2, call.Number)
		assert.Equal("middle-head", call.ExpectedHeadSHA)
	default:
		require.Fail("expected merge call")
	}
}

func TestPullStackReportsNativeMembership(t *testing.T) {
	for _, native := range []bool{false, true} {
		_, _, client := newMidStackMergeFixture(t, native)

		stack, err := client.HTTP.GetPullStackOnHostWithResponse(t.Context(), &generated.GetPullStackOnHostRequestOptions{PathParams: &generated.GetPullStackOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gitlab", Owner: "group", Name: "project", Number: int64(2)}})
		require.NoError(t, err)
		require.NotNil(t, stack.JSON200)
		assert.Equal(t, native, stack.JSON200.Native)

		detail, err := client.HTTP.GetPullOnHostWithResponse(t.Context(), &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gitlab", Owner: "group", Name: "project", Number: int64(2)}})
		require.NoError(t, err)
		require.NotNil(t, detail.JSON200)
		require.NotNil(t, detail.JSON200.Stack)
		assert.Equal(t, native, detail.JSON200.Stack.Native)
	}
}

func TestMergePRMergesNativeStackTopWithMidStackMergesDisabled(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	server, provider, client := newMidStackMergeFixture(t, true)
	require.False(server.handler.allowMidStackMerges())

	resp, err := client.HTTP.MergePullOnHostWithResponse(t.Context(), &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gitlab", Owner: "group", Name: "project", Number: int64(2)}, Body: &generated.MergePRInputBody{
		Method:          "squash",
		ExpectedHeadSha: new("middle-head"),
	}})
	require.NoError(err)
	assert.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	select {
	case call := <-provider.mergeCh:
		assert.Equal(2, call.Number)
	default:
		require.Fail("expected merge call")
	}
}

func TestMergePREnqueuesNativeStackTopOnStackBaseQueue(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	server, provider, client := newMidStackMergeFixture(t, true)
	provider.mergeQueue = true
	provider.enqueueCh = make(chan int, 1)
	repo, err := server.handler.db.GetRepoByIdentity(t.Context(), platformdb.DBRepoIdentity(provider.ref))
	require.NoError(err)
	require.NotNil(repo)
	// The bottom member targets the stack base, whose queue merges the stack;
	// the top member targets the bottom member's branch, which has no queue.
	_, err = server.handler.db.UpsertMergeQueueState(t.Context(), repo.ID, db.MergeQueueState{
		Number: 1, BaseBranch: "main", Enabled: true, MergeMethod: "squash", ObservedAt: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(err)
	_, err = server.handler.db.UpsertMergeQueueState(t.Context(), repo.ID, db.MergeQueueState{
		Number: 2, BaseBranch: "bottom", Enabled: false, ObservedAt: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(err)

	detail, err := client.HTTP.GetPullOnHostWithResponse(t.Context(), &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gitlab", Owner: "group", Name: "project", Number: int64(2)}})
	require.NoError(err)
	require.NotNil(detail.JSON200)
	require.NotNil(detail.JSON200.MergeQueueTarget)
	assert.Equal("main", detail.JSON200.MergeQueueTarget.Branch)
	assert.Equal("squash", detail.JSON200.MergeQueueTarget.MergeMethod)

	resp, err := client.HTTP.MergePullOnHostWithResponse(t.Context(), &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gitlab", Owner: "group", Name: "project", Number: int64(2)}, Body: &generated.MergePRInputBody{
		Method:          "squash",
		ExpectedHeadSha: new("middle-head"),
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	select {
	case number := <-provider.enqueueCh:
		assert.Equal(2, number)
	default:
		require.Fail("expected enqueue call")
	}
	assert.Empty(provider.mergeCh)
}

func TestPullStackReportsMidStackBlockerOnlyForInferredStacks(t *testing.T) {
	for native, want := range map[bool]*int64{false: new(int64(1)), true: nil} {
		_, _, client := newMidStackMergeFixture(t, native)

		detail, err := client.HTTP.GetPullOnHostWithResponse(t.Context(), &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gitlab", Owner: "group", Name: "project", Number: int64(2)}})
		require.NoError(t, err)
		require.NotNil(t, detail.JSON200)
		require.NotNil(t, detail.JSON200.Stack)
		assert.Equal(t, want, detail.JSON200.Stack.MidStackBlocker, "native=%v", native)

		bottom, err := client.HTTP.GetPullOnHostWithResponse(t.Context(), &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gitlab", Owner: "group", Name: "project", Number: int64(1)}})
		require.NoError(t, err)
		require.NotNil(t, bottom.JSON200)
		require.NotNil(t, bottom.JSON200.Stack)
		assert.Nil(t, bottom.JSON200.Stack.MidStackBlocker, "native=%v", native)
	}
}

func TestDeferredMergeEnqueuesNativeStackTopOnStackBaseQueue(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	ref := platform.RepoRef{
		Platform:      platform.KindGitLab,
		Host:          "gitlab.example.com",
		Owner:         "group",
		Name:          "project",
		RepoPath:      "group/project",
		Key:           platform.RepositoryIDKey(4242),
		DefaultBranch: "main",
	}
	// The stack top's own rules stay blocked after CI passes. Only the stack
	// base has a merge queue, so the merge must enqueue rather than fail for
	// want of a bypass.
	provider := &deferredMergeTestProvider{
		ref: ref,
		mergeRequests: []platform.MergeRequest{{
			Repo: ref, PlatformID: 7001, Number: 7, Title: "Defer merge", Author: "ada", State: "open",
			HeadBranch: "feature", BaseBranch: "bottom", HeadSHA: "head-sha", BaseSHA: "base-sha",
			CIStatus: "success", MergeableState: "blocked",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		}},
		ciChecks: map[string][]platform.CICheck{
			"head-sha": {{App: "GitLab", Name: "pipeline", Status: "completed", Conclusion: "success"}},
		},
		mergeCh:    make(chan deferredMergeTestMergeCall, 1),
		mergeQueue: true,
		enqueueCh:  make(chan int, 1),
	}
	srv, database, repoID, client := newDeferredMergeRouteServer(
		t, provider, ref, now,
		[]db.CICheck{{App: "GitLab", Name: "pipeline", Status: "in_progress"}},
	)
	top, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(top)
	top.BaseBranch = "bottom"
	top.UpdatedAt = now.Add(time.Second)
	topID, err := database.UpsertMergeRequest(ctx, top)
	require.NoError(err)
	bottomID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 7000, Number: 6, State: db.MergeRequestStateOpen,
		Title: "Bottom", HeadBranch: "bottom", BaseBranch: "main",
		PlatformHeadSHA: "bottom-head", PlatformBaseSHA: "base-sha",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	stackID, err := database.UpsertStack(ctx, repoID, 6, "feature", true)
	require.NoError(err)
	require.NoError(database.ReplaceStackMembers(ctx, stackID, []db.StackMember{
		{StackID: stackID, MergeRequestID: bottomID, Position: 1},
		{StackID: stackID, MergeRequestID: topID, Position: 2},
	}))
	_, err = database.UpsertMergeQueueState(ctx, repoID, db.MergeQueueState{
		Number: 6, BaseBranch: "main", Enabled: true, MergeMethod: "squash", ObservedAt: now,
	})
	require.NoError(err)
	_, err = database.UpsertMergeQueueState(ctx, repoID, db.MergeQueueState{
		Number: 7, BaseBranch: "bottom", Enabled: false, ObservedAt: now,
	})
	require.NoError(err)
	events, _ := srv.Hub().Subscribe(ctx, false)

	expectedHeadSHA := "head-sha"
	resp, err := client.HTTP.DeferMergePullOnHostWithResponse(ctx, &generated.DeferMergePullOnHostRequestOptions{
		PathParams: &generated.DeferMergePullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)},
		Body:       &generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &expectedHeadSHA},
	})
	require.NoError(err)
	require.Equal(202, resp.StatusCode, string(resp.Body))

	var completed DeferredMergeCompletedPayload
	for completed.Status == "" {
		select {
		case ev := <-events:
			if ev.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := ev.Event.Data.(DeferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-time.After(time.Second):
			require.FailNow("timed out waiting for deferred merge completion")
		}
	}
	require.Equal("queued", completed.Status, completed.Error)
	require.Equal(7, <-provider.enqueueCh)
	require.Empty(provider.mergeCh)
}
