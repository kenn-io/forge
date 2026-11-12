package github

import (
	"context"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/require"
)

func TestFirstSyncSettlesWithThrottledRepository(t *testing.T) {
	database := openTestDB(t)
	healthy := &mockClient{openPRs: []*gh.PullRequest{buildOpenPR(1, time.Now())}}
	throttled := &mockClient{}
	tracker := NewRateTracker(database, "throttled.example.com", "host", "rest")
	tracker.UpdateFromRate(Rate{Limit: 5000, Remaining: 100, Reset: time.Now().Add(time.Hour)})
	syncer := NewSyncer(
		map[string]Client{"github.com": healthy, "throttled.example.com": throttled},
		database, nil,
		[]RepoRef{
			{Owner: "healthy", Name: "repo", PlatformHost: "github.com"},
			{Owner: "waiting", Name: "repo", PlatformHost: "throttled.example.com"},
		},
		time.Minute, map[string]*RateTracker{"throttled.example.com": tracker}, nil,
	)
	t.Cleanup(syncer.Stop)
	results := make(chan []RepoSyncResult, 1)
	syncer.SetOnSyncCompleted(func(got []RepoSyncResult) { results <- got })
	receipt, ok := syncer.TriggerRunWithPriority(t.Context(), nil)
	require.True(t, ok)
	require.Eventually(t, func() bool { return syncer.Status().FullSync.SettledGeneration == receipt.Generation }, 5*time.Second, time.Millisecond)
	status := syncer.Status()
	require.True(t, healthy.listOpenPRsCalled.Load())
	require.False(t, throttled.listOpenPRsCalled.Load())
	require.Empty(t, status.LastError)
	require.Empty(t, status.FullSync.LastError)
	require.Equal(t, receipt.Generation, status.FullSync.SuccessfulGeneration)
	got := <-results
	require.Empty(t, got[0].Error)
	require.Equal(t, "skipped: rate limit throttled", got[1].Error)
}

func TestFirstSyncSettlesWhenReserveRunsOutDuringPass(t *testing.T) {
	database := openTestDB(t)
	tracker := NewRateTracker(database, "github.com", "host", "rest")
	tracker.UpdateFromRate(Rate{Limit: 5000, Remaining: 5000, Reset: time.Now().Add(time.Hour)})
	var syncer *Syncer
	client := &mockClient{listOpenPRsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
		tracker.UpdateFromRate(Rate{Limit: 5000, Remaining: 100, Reset: time.Now().Add(time.Hour)})
		syncer.invalidateReserveVerdicts("github.com")
		return nil, nil
	}}
	syncer = NewSyncer(map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{{Owner: "first", Name: "repo", PlatformHost: "github.com"}, {Owner: "second", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, map[string]*RateTracker{"github.com": tracker}, nil)
	syncer.SetParallelism(1)
	syncer.SetQuotaRegistry(NewQuotaRegistry())
	t.Cleanup(syncer.Stop)
	results := make(chan []RepoSyncResult, 1)
	syncer.SetOnSyncCompleted(func(got []RepoSyncResult) { results <- got })
	receipt, ok := syncer.TriggerRunWithPriority(t.Context(), nil)
	require.True(t, ok)
	require.Eventually(t, func() bool { return syncer.Status().FullSync.SettledGeneration == receipt.Generation }, 5*time.Second, time.Millisecond)
	status := syncer.Status()
	require.Empty(t, status.LastError)
	require.Equal(t, receipt.Generation, status.FullSync.SuccessfulGeneration)
	got := <-results
	require.Empty(t, got[0].Error)
	require.Equal(t, "skipped: rate limit throttled", got[1].Error)
}

func TestFirstSyncReportsMissingCredentialRoute(t *testing.T) {
	database := openTestDB(t)
	client := &mockClient{}
	router, err := NewHostRouter("github.com", nil)
	require.NoError(t, err)
	syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{{Owner: "missing", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, nil)
	syncer.SetGitHubRouters(map[string]*HostRouter{"github.com": router})
	t.Cleanup(syncer.Stop)
	receipt, ok := syncer.TriggerRunWithPriority(t.Context(), nil)
	require.True(t, ok)
	require.Eventually(t, func() bool { return syncer.Status().FullSync.SettledGeneration == receipt.Generation }, 5*time.Second, time.Millisecond)
	status := syncer.Status().FullSync
	require.Less(t, status.SuccessfulGeneration, receipt.Generation)
	require.Contains(t, status.LastError, "missing/repo")
}
