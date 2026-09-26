package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cenkalti/backoff/v7"
	gh "github.com/google/go-github/v92/github"
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
		syncer.RunRelay(ctx, RelayOptions{URL: server.URL, Client: server.Client(), Backoff: backoff.NewConstantBackOff(time.Millisecond)})
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
	published := syncer.Status()
	unavailable.Store(true)
	server.CloseClientConnections()
	disconnected := awaitRelayStatus(t, statuses, func(status RelayStatus) bool { return !status.Connected })
	assert.Equal(status.Recent, disconnected.Recent)
	assert.True(published.Relay.Connected, "a published snapshot must not change after a later transition")
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
	syncer.SetAirplaneMode(true)
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
	for _, state := range []db.MergeRequestState{"closed", "merged"} {
		updated.State = state
		_, err = database.UpsertMergeRequest(ctx, updated)
		require.NoError(err)
		require.NoError(syncer.refreshRelayHint(WithSyncBudget(ctx), hint))
	}
	hint.Number = 99
	require.NoError(syncer.refreshRelayHint(WithSyncBudget(ctx), hint))
	updated.State, updated.PlatformHeadSHA = db.MergeRequestStateOpen, ""
	_, err = database.UpsertMergeRequest(ctx, updated)
	require.NoError(err)
	hint.Number = 7
	require.NoError(syncer.refreshRelayHint(WithSyncBudget(ctx), hint))
	assert.Equal(int32(1), provider.getCombinedCalls.Load(), "closed, unknown, and headless PRs do not spend CI budget")
}

func TestRelayWorkflowNotificationBypassesBackgroundReserve(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	repo := RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", PlatformExternalID: "R_project", Owner: "team", Name: "project"}
	repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: repo.PlatformHost, PlatformRepoID: repo.PlatformExternalID, Owner: repo.Owner, Name: repo.Name,
	})
	require.NoError(err)
	quota := NewQuotaRegistry()
	quota.UpdateSnapshot(HostIdentity(repo.PlatformHost), QuotaResourceREST, Rate{
		Limit: 5000, Remaining: RateReserveBuffer, Reset: time.Now().UTC().Add(time.Hour),
	})
	syncer := NewSyncer(nil, database, nil, []RepoRef{repo}, time.Minute, nil, nil)
	syncer.SetQuotaRegistry(quota)
	require.True(syncer.backgroundReserveExhausted(repo, QuotaResourceREST, false))
	var notified []string
	syncer.SetOnRelayRefresh(func(_ context.Context, id int64, target string, _ int) {
		assert.Equal(repoID, id)
		notified = append(notified, target)
	})
	for _, target := range []string{activityrelay.RepositoryRefs, activityrelay.WorkflowRuns} {
		require.NoError(syncer.refreshRelayHint(WithSyncBudget(t.Context()), activityrelay.Hint{
			Provider: "github", Host: repo.PlatformHost, RepositoryID: repo.PlatformExternalID, Target: target,
		}))
	}
	assert.Equal([]string{activityrelay.WorkflowRuns}, notified, "only the notification bypasses the background quota gate")
}

func TestRelayChecksRefreshImmediatelyAndKeepEventsDuringRefresh(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		database := openTestDB(t)
		repo := RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", PlatformExternalID: "R_project", Owner: "team", Name: "project"}
		repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{Platform: "github", PlatformHost: "github.com", PlatformRepoID: "R_project", Owner: "team", Name: "project"})
		require.NoError(err)
		_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID: repoID, Number: 7, PlatformID: 7, PlatformExternalID: "pr-7", State: "open", PlatformHeadSHA: "abcdef", CIStatus: "pending",
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		})
		require.NoError(err)
		started := make(chan time.Time, 2)
		finish := make(chan struct{}, 2)
		provider := &mockClient{ciStatus: &gh.CombinedStatus{State: new("success")}}
		provider.listCheckRunsForRefFn = func(ctx context.Context, _, _, _ string) ([]*gh.CheckRun, error) {
			started <- time.Now()
			select {
			case <-finish:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return []*gh.CheckRun{{Name: new("ci"), Status: new("completed"), Conclusion: new("success")}}, nil
		}
		syncer := NewSyncer(map[string]Client{"github.com": provider}, database, nil, []RepoRef{repo}, time.Minute, nil, nil)
		refreshed := make(chan struct{}, 2)
		syncer.SetOnRelayRefresh(func(context.Context, int64, string, int) { refreshed <- struct{}{} })
		queue := &relayQueue{signal: make(chan struct{}, 1)}
		go syncer.drainRelayQueue(ctx, queue)
		hint := activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_project", Target: activityrelay.PullRequestChecks, Number: 7}
		begin := time.Now()
		queue.push(hint)
		first := <-started
		assert.Equal(begin, first, "Forge must not add another delay after the relay batch")
		queue.push(hint) // A completion arriving during the provider request must survive it.
		finish <- struct{}{}
		<-refreshed
		second := <-started
		assert.Equal(first, second, "a new hint received during a request stays ready to run")
		finish <- struct{}{}
		<-refreshed
		time.Sleep(time.Minute)
		synctest.Wait()
		assert.Empty(started, "no refresh runs without another hint")
		assert.Equal(int32(2), provider.getCombinedCalls.Load())
		updated, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
		require.NoError(err)
		assert.Equal("success", updated.CIStatus)
	})
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

func TestRelayQueueReservesRoomForWorkflowUpdates(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	queue := &relayQueue{signal: make(chan struct{}, 1)}
	for number := 1; number <= 1025; number++ {
		queue.push(activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_project", Target: activityrelay.PullRequestChecks, Number: number})
	}
	var expected []activityrelay.Hint
	for i := range 257 {
		hint := activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_project_" + strconv.Itoa(i), Target: activityrelay.WorkflowRuns}
		queue.push(hint)
		queue.push(hint)
		if i < 256 {
			expected = append(expected, hint)
		}
	}
	var workflows []activityrelay.Hint
	var checks int
	for hint, ok := queue.pop(); ok; hint, ok = queue.pop() {
		if hint.Target == activityrelay.WorkflowRuns {
			workflows = append(workflows, hint)
		} else {
			checks++
		}
	}
	require.Equal(expected, workflows, "workflow updates have bounded, coalesced space beyond a full check queue")
	require.Equal(1024, checks)
}

func TestRelayRepositoryHintsRespectBudgetAdmission(t *testing.T) {
	t.Parallel()
	for _, target := range []string{activityrelay.Repository, activityrelay.RepositoryRefs} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			assert := assert.New(t)
			ctx := WithSyncBudget(t.Context())
			database := openTestDB(t)
			repo := RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", PlatformExternalID: "R_test_project", Owner: "team", Name: "project"}
			_, err := database.UpsertRepo(ctx, db.RepoIdentity{
				Platform: "github", PlatformHost: "github.com", PlatformRepoID: repo.PlatformExternalID, Owner: repo.Owner, Name: repo.Name,
			})
			require.NoError(err)
			budget := NewSyncBudgetWithEssentialReserve(100)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v3/repos/team/project" {
					_, _ = w.Write([]byte(`{"id":1,"node_id":"R_test_project","name":"project","owner":{"login":"team"},"default_branch":"main","has_issues":true}`))
					return
				}
				_, _ = w.Write([]byte(`[]`))
			}))
			t.Cleanup(server.Close)
			client, err := NewClient(testTokenSource("token"), "github.com", nil, budget, WithBaseURLForTesting(server.URL))
			require.NoError(err)
			syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, []RepoRef{repo}, time.Minute, nil, map[string]*SyncBudget{"github.com": budget})
			var refreshed int
			syncer.SetOnRelayRefresh(func(context.Context, int64, string, int) { refreshed++ })
			hint := activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: repo.PlatformExternalID, Target: target}

			// Both the reserve alone and less than the conservative refresh cost
			// must leave the hint to ordinary syncing, without any provider I/O.
			for _, spent := range []int{90, 71} {
				budget.Reset()
				budget.Spend(spent)
				require.NoError(syncer.refreshRelayHint(ctx, hint))
				assert.Zero(requests.Load())
				assert.Equal(spent, budget.Spent())
				assert.Zero(refreshed)
			}

			budget.Reset()
			budget.Spend(70)
			require.NoError(syncer.refreshRelayHint(ctx, hint))
			assert.Positive(requests.Load(), "an affordable hint must refresh the provider")
			assert.Equal(1, refreshed)
		})
	}
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
