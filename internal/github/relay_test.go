package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/activityrelay"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

func TestRelayStatusRecentActivity(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	budget := NewSyncBudget(1)
	budget.Spend(1) // Received events must be visible even while refreshes wait for budget.
	repo := RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", PlatformExternalID: "R_project", Owner: "team", Name: "project"}
	syncer := NewSyncer(nil, database, nil, []RepoRef{repo}, time.Minute, nil, map[string]*SyncBudget{"github.com": budget})
	assert.Nil(syncer.Status().Relay)
	store, err := activityrelay.Open(filepath.Join(t.TempDir(), "relay.db"))
	require.NoError(err)
	t.Cleanup(func() { require.NoError(store.Close()) })
	_, handler := activityrelay.Handlers(store, nil)
	var unavailable atomic.Bool
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(feed.Close)
	require.NoError(syncer.PollRelay(ctx, feed.URL, feed.Client()))
	require.NotNil(syncer.Status().Relay)
	assert.Empty(syncer.Status().Relay.Recent)
	var hints []activityrelay.Hint
	for number := 1; number <= 25; number++ {
		hints = append(hints, activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_project", Target: activityrelay.Issue, Number: number})
	}
	hints = append(hints, activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_untracked", Target: activityrelay.Issue, Number: 99})
	require.NoError(store.Append(ctx, hints))
	require.NoError(syncer.PollRelay(ctx, feed.URL, feed.Client()))
	status := syncer.Status().Relay
	require.Len(status.Recent, 20)
	assert.Equal(25, status.Recent[0].Number)
	assert.Equal(6, status.Recent[19].Number)
	assert.Equal("team/project", status.Recent[0].Repository)
	assert.False(status.Unavailable)
	assert.False(status.LastPollAt.IsZero())
	assert.False(status.Recent[0].ReceivedAt.IsZero())
	syncer.publishStatus(&SyncStatus{Running: true})
	assert.Equal(status, syncer.Status().Relay, "ordinary sync must preserve relay activity")
	unavailable.Store(true)
	require.Error(syncer.PollRelay(ctx, feed.URL, feed.Client()))
	assert.True(syncer.Status().Relay.Unavailable)
	assert.Equal(status.Recent, syncer.Status().Relay.Recent)
	assert.False(status.Unavailable, "published snapshots must remain immutable")
	unavailable.Store(false)
	require.NoError(syncer.PollRelay(ctx, feed.URL, feed.Client()))
	assert.False(syncer.Status().Relay.Unavailable)
	assert.Len(syncer.Status().Relay.Recent, 20, "repeated polls must not repeat events")
}

func TestRelayTargetedChecksAndBudgetRecovery(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	assert := assert.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: "R_test_project", Owner: "team", Name: "project",
	})
	require.NoError(err)
	for _, number := range []int{7, 8} {
		_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID: repoID, Number: number, PlatformID: int64(number), PlatformExternalID: "pr-" + strconv.Itoa(number),
			Title: "Test PR", State: "open", PlatformHeadSHA: "abcdef", CIStatus: "pending",
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		})
		require.NoError(err)
	}
	provider := &mockClient{
		checkRuns: []*gh.CheckRun{{Name: new("ci"), Status: new("completed"), Conclusion: new("success")}},
		ciStatus:  &gh.CombinedStatus{State: new("success")},
	}
	budget := NewSyncBudget(100)
	// A cached catalog ref need not carry GitHub's numeric REST ID.
	repo := RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", PlatformExternalID: "R_test_project", Owner: "team", Name: "project"}
	syncer := NewSyncer(map[string]Client{"github.com": provider}, database, nil, []RepoRef{repo}, time.Minute, nil, map[string]*SyncBudget{"github.com": budget})
	store, err := activityrelay.Open(filepath.Join(t.TempDir(), "relay.db"))
	require.NoError(err)
	t.Cleanup(func() { require.NoError(store.Close()) })
	_, feed := activityrelay.Handlers(store, nil)
	server := httptest.NewServer(feed)
	t.Cleanup(server.Close)
	head, err := store.Read(ctx, "", 100)
	require.NoError(err)
	require.NoError(database.SaveRelayPage(ctx, server.URL, head.NextCursor, nil))
	hint := activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_test_project", Target: activityrelay.PullRequestChecks, Number: 7}
	require.NoError(store.Append(ctx, []activityrelay.Hint{hint, hint}))
	budget.Spend(100)
	require.NoError(syncer.PollRelay(ctx, server.URL, server.Client()))
	pending, err := database.PendingRelayHints(ctx, server.URL)
	require.NoError(err)
	assert.Equal([]activityrelay.Hint{hint}, pending)
	assert.Zero(provider.getCombinedCalls.Load())
	budget.Reset()
	var notified int
	syncer.SetOnRelayRefresh(func(_ context.Context, id int64, target string, number int) {
		assert.Equal(repoID, id)
		assert.Equal(activityrelay.PullRequestChecks, target)
		notified = number
	})
	require.NoError(syncer.PollRelay(ctx, server.URL, server.Client()))
	assert.Equal(7, notified)
	assert.Equal(int32(1), provider.getCombinedCalls.Load())
	updated, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	assert.Equal("success", updated.CIStatus)
	unrelated, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 8)
	require.NoError(err)
	assert.Equal("pending", unrelated.CIStatus)
	pending, err = database.PendingRelayHints(ctx, server.URL)
	require.NoError(err)
	assert.Empty(pending)

	syncer.DisableSync()
	require.NoError(store.Append(ctx, []activityrelay.Hint{hint}))
	require.NoError(syncer.PollRelay(ctx, server.URL, server.Client()))
	assert.Equal(int32(1), provider.getCombinedCalls.Load())
}

func TestRelayInitialAndExpiredCursorReconciliation(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	assert := assert.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	budget := NewSyncBudget(1)
	budget.Spend(1)
	repos := []RepoRef{
		{Platform: platform.KindGitHub, PlatformHost: "github.com", PlatformExternalID: "R_test_project", Owner: "team", Name: "project"},
		{Platform: platform.KindGitHub, PlatformHost: "github.com", PlatformExternalID: "R_archived", Archived: true},
		{Platform: platform.KindGitLab, PlatformHost: "gitlab.example.com", PlatformExternalID: "123"},
	}
	syncer := NewSyncer(map[string]Client{"github.com": &mockClient{}}, database, nil, repos, time.Minute, nil, map[string]*SyncBudget{"github.com": budget})
	store, err := activityrelay.Open(filepath.Join(t.TempDir(), "relay.db"))
	require.NoError(err)
	t.Cleanup(func() { require.NoError(store.Close()) })
	_, handler := activityrelay.Handlers(store, nil)
	feed := httptest.NewServer(handler)
	t.Cleanup(feed.Close)
	require.NoError(syncer.PollRelay(ctx, feed.URL, feed.Client()))
	want := activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_test_project", Target: activityrelay.Repository}
	pending, err := database.PendingRelayHints(ctx, feed.URL)
	require.NoError(err)
	assert.Equal([]activityrelay.Hint{want}, pending)
	require.NoError(database.CompleteRelayHint(ctx, feed.URL, want))
	before, err := database.RelayCursor(ctx, feed.URL)
	require.NoError(err)
	require.NoError(store.Append(ctx, []activityrelay.Hint{want}))
	require.NoError(store.Prune(ctx, time.Now().Add(time.Hour)))
	require.NoError(syncer.PollRelay(ctx, feed.URL, feed.Client()))
	after, err := database.RelayCursor(ctx, feed.URL)
	require.NoError(err)
	assert.NotEqual(before, after)
	pending, err = database.PendingRelayHints(ctx, feed.URL)
	require.NoError(err)
	assert.Equal([]activityrelay.Hint{want}, pending)
}

func TestRelayDisabledIssueRetainsWorkDuringCooldown(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	ref := RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "team", Name: "project", PlatformExternalID: "repo-team-project"}
	_, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", ref.Owner, ref.Name))
	require.NoError(err)
	provider := &partialFailureMock{}
	var calls int
	provider.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) {
		calls++
		return nil, platform.RepositoryFeatureDisabled(platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues, errors.New("issues disabled"))
	}
	syncer := NewSyncer(map[string]Client{"github.com": provider}, database, nil, []RepoRef{ref}, time.Minute, nil, testBudget(1000))
	now := time.Now().UTC()
	syncer.now = func() time.Time { return now }
	store, err := activityrelay.Open(filepath.Join(t.TempDir(), "relay.db"))
	require.NoError(err)
	t.Cleanup(func() { require.NoError(store.Close()) })
	_, handler := activityrelay.Handlers(store, nil)
	feed := httptest.NewServer(handler)
	t.Cleanup(feed.Close)
	head, err := store.Read(ctx, "", 100)
	require.NoError(err)
	require.NoError(database.SaveRelayPage(ctx, feed.URL, head.NextCursor, nil))
	hint := activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: ref.PlatformExternalID, Target: activityrelay.Issue, Number: 9}
	require.NoError(store.Append(ctx, []activityrelay.Hint{hint}))
	first := syncer.PollRelay(ctx, feed.URL, feed.Client())
	second := syncer.PollRelay(ctx, feed.URL, feed.Client())
	assert.Equal(1, calls, "disabled features must not be retried every relay poll")
	require.NoError(first)
	require.NoError(second)
	pending, err := database.PendingRelayHints(ctx, feed.URL)
	require.NoError(err)
	assert.Equal([]activityrelay.Hint{hint}, pending)
	now = now.Add(24*time.Hour + time.Second)
	require.NoError(syncer.PollRelay(ctx, feed.URL, feed.Client()))
	assert.Equal(2, calls)
}
