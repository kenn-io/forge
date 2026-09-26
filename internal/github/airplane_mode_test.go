package github

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/require"
)

func TestAirplaneModeSkipsScheduledSyncButAllowsManualSync(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	var detailCalls atomic.Int32
	client := &mockClient{getPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
		detailCalls.Add(1)
		return nil, nil
	}}
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	syncer := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	syncer.SetWatchedMRs([]WatchedMR{{Owner: repo.Owner, Name: repo.Name, PlatformHost: repo.PlatformHost, Number: 1}})
	syncer.SetAirplaneMode(true)
	syncer.RunOnce(t.Context())
	syncer.SyncWatchedMRs(t.Context())
	require.False(client.listOpenPRsCalled.Load())
	require.Zero(detailCalls.Load())

	finished := make(chan struct{})
	syncer.SetOnSyncCompleted(func([]RepoSyncResult) { close(finished) })
	require.True(syncer.TriggerRun(t.Context()))
	select {
	case <-finished:
	case <-t.Context().Done():
		require.FailNow("manual sync did not complete")
	}
	require.True(client.listOpenPRsCalled.Load())
	// Direct item refresh remains available even while scheduled work is paused.
	_ = syncer.SyncMR(t.Context(), repo.Owner, repo.Name, 1)
	require.Positive(detailCalls.Load())
}

func TestAirplaneModePausesAndResumesArchive(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		runner := &pacedArchiveRunner{}
		syncer, stop := startPacedArchiveLoop(t, runner)
		defer stop()
		synctest.Wait()
		syncer.SetAirplaneMode(true)
		runner.reset()
		syncer.WakeArchive()
		time.Sleep(time.Hour)
		synctest.Wait()
		require.Empty(runner.recorded())
		syncer.SetAirplaneMode(false)
		syncer.WakeArchive()
		synctest.Wait()
		require.NotEmpty(runner.recorded())
	})
}

func TestAirplaneModeDropsQueuedAutomaticSyncAndKeepsManualScope(t *testing.T) {
	t.Parallel()

	for _, queuedManual := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "manual"}[queuedManual], func(t *testing.T) {
			require := require.New(t)
			repos := []RepoRef{
				{Owner: "acme", Name: "selected", PlatformHost: "github.com"},
				{Owner: "acme", Name: "unrelated", PlatformHost: "github.com"},
			}
			var selectedCalls, unrelatedCalls atomic.Int32
			var syncer *Syncer
			client := &mockClient{listOpenPRsFn: func(ctx context.Context, _, name string) ([]*gh.PullRequest, error) {
				if name == "unrelated" {
					unrelatedCalls.Add(1)
				} else if selectedCalls.Add(1) == 1 {
					syncer.RunOnce(ctx)
					if queuedManual {
						syncer.TriggerRunForRepos(ctx, repos[:1])
					}
					syncer.SetAirplaneMode(true)
				}
				return nil, nil
			}}
			syncer = NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, repos, time.Hour, nil, nil)
			t.Cleanup(syncer.Stop)
			syncer.runOnce(t.Context(), true, nil, repos[:1])
			wantCalls := int32(1)
			if queuedManual {
				wantCalls++
			}
			require.Eventually(func() bool {
				return !syncer.running.Load()
			}, 5*time.Second, time.Millisecond)
			require.Equal(wantCalls, selectedCalls.Load())
			require.Zero(unrelatedCalls.Load())
		})
	}
}
