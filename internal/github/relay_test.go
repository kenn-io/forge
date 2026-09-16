package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

// relayStatuses forwards every published relay status so tests wait on the
// exact transition they care about instead of polling.
func relayStatuses(t *testing.T, syncer *Syncer) <-chan RelayStatus {
	t.Helper()
	statuses := make(chan RelayStatus, 256)
	syncer.SetOnStatusChange(func(status *SyncStatus) {
		if status.Relay != nil {
			statuses <- *status.Relay
		}
	})
	return statuses
}

func awaitRelayStatus(t *testing.T, statuses <-chan RelayStatus, matches func(RelayStatus) bool) RelayStatus {
	t.Helper()
	for {
		select {
		case status := <-statuses:
			if matches(status) {
				return status
			}
		case <-time.After(10 * time.Second):
			require.FailNow(t, "relay status transition did not arrive")
		}
	}
}

func immediateRetry(int) time.Duration { return time.Millisecond }

func TestRelaySubscriptionStatusAndRecentActivity(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	database := openTestDB(t)
	budget := NewSyncBudget(1)
	budget.Spend(1) // Received events must be visible even while refreshes wait for budget.
	repos := []RepoRef{
		{Platform: platform.KindGitHub, PlatformHost: "github.com", PlatformExternalID: "R_project", Owner: "team", Name: "project"},
		{Platform: platform.KindGitHub, PlatformHost: "github.com", PlatformExternalID: "R_archived", Archived: true},
		{Platform: platform.KindGitLab, PlatformHost: "gitlab.example.com", PlatformExternalID: "123"},
	}
	syncer := NewSyncer(nil, database, nil, repos, time.Minute, nil, map[string]*SyncBudget{"github.com": budget})
	assert.Nil(syncer.Status().Relay)
	statuses := relayStatuses(t, syncer)
	feed := new(activityrelay.Broadcaster)
	_, handler := activityrelay.Handlers(feed, nil)
	var unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		syncer.RunRelay(ctx, RelayOptions{URL: server.URL, Client: server.Client(), RetryDelay: immediateRetry})
	}()
	t.Cleanup(func() { cancel(); <-stopped })
	connected := awaitRelayStatus(t, statuses, func(status RelayStatus) bool { return status.Connected })
	assert.Empty(connected.Recent)
	var hints []activityrelay.Hint
	for number := 1; number <= 25; number++ {
		hints = append(hints, activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_project", Target: activityrelay.Issue, Number: number})
	}
	hints = append(hints,
		activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_untracked", Target: activityrelay.Issue, Number: 99},
		activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_archived", Target: activityrelay.Repository},
	)
	feed.Publish(hints)
	status := awaitRelayStatus(t, statuses, func(status RelayStatus) bool {
		return len(status.Recent) == 20 && status.Recent[0].Number == 25
	})
	assert.Equal(6, status.Recent[19].Number)
	assert.Equal("team/project", status.Recent[0].Repository)
	assert.True(status.Connected)
	assert.False(status.Recent[0].ReceivedAt.IsZero())
	assert.Greater(status.Recent[0].ID, status.Recent[1].ID)
	syncer.publishStatus(&SyncStatus{Running: true})
	assert.Equal(status, *syncer.Status().Relay, "ordinary sync must preserve relay activity")
	unavailable.Store(true)
	server.CloseClientConnections()
	disconnected := awaitRelayStatus(t, statuses, func(status RelayStatus) bool { return !status.Connected })
	assert.Equal(status.Recent, disconnected.Recent)
	assert.True(status.Connected, "published snapshots must remain immutable")
	unavailable.Store(false)
	awaitRelayStatus(t, statuses, func(status RelayStatus) bool { return status.Connected })
	assert.Len(syncer.Status().Relay.Recent, 20, "reconnecting must not replay activity")
}

func TestRelayTargetedChecksAndBudgetGate(t *testing.T) {
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
	hint := activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_test_project", Target: activityrelay.PullRequestChecks, Number: 7}
	budget.Spend(100)
	require.NoError(syncer.refreshRelayHint(WithSyncBudget(ctx), hint))
	assert.Zero(provider.getCombinedCalls.Load(), "an exhausted budget drops the hint instead of spending")
	budget.Reset()
	var notified int
	syncer.SetOnRelayRefresh(func(_ context.Context, id int64, target string, number int) {
		assert.Equal(repoID, id)
		assert.Equal(activityrelay.PullRequestChecks, target)
		notified = number
	})
	require.NoError(syncer.refreshRelayHint(WithSyncBudget(ctx), hint))
	assert.Equal(7, notified)
	assert.Equal(int32(1), provider.getCombinedCalls.Load())
	updated, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	assert.Equal("success", updated.CIStatus)
	unrelated, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 8)
	require.NoError(err)
	assert.Equal("pending", unrelated.CIStatus)
}

func TestRelayQueueCoalescesAndSkipsDisabledSync(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	queue := &relayQueue{signal: make(chan struct{}, 1)}
	hint := activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_test_project", Target: activityrelay.Issue, Number: 9}
	other := hint
	other.Number = 10
	queue.push(hint)
	queue.push(hint)
	queue.push(other)
	first, ok := queue.pop()
	require.True(ok)
	second, ok := queue.pop()
	require.True(ok)
	_, ok = queue.pop()
	assert.False(ok, "repeated hints for one target coalesce while waiting")
	assert.Equal([]activityrelay.Hint{hint, other}, []activityrelay.Hint{first, second})

	database := openTestDB(t)
	syncer := NewSyncer(nil, database, nil, []RepoRef{{Platform: platform.KindGitHub, PlatformHost: "github.com", PlatformExternalID: "R_test_project", Owner: "team", Name: "project"}}, time.Minute, nil, nil)
	syncer.DisableSync()
	syncer.receiveRelayHint(hint, 1, queue)
	_, ok = queue.pop()
	assert.False(ok, "hints are ignored while syncing is disabled")
	assert.Nil(syncer.Status().Relay)
}

func TestRelayDisabledIssueRespectsCooldown(t *testing.T) {
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
	hint := activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: ref.PlatformExternalID, Target: activityrelay.Issue, Number: 9}
	require.NoError(syncer.refreshRelayHint(WithSyncBudget(ctx), hint))
	require.NoError(syncer.refreshRelayHint(WithSyncBudget(ctx), hint))
	assert.Equal(1, calls, "disabled features must not be retried for every hint")
	now = now.Add(24*time.Hour + time.Second)
	require.NoError(syncer.refreshRelayHint(WithSyncBudget(ctx), hint))
	assert.Equal(2, calls)
}
