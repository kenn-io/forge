package github

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/require"
)

func TestFullSyncReceiptsFollowQueuedWork(t *testing.T) {
	entered := make(chan int, 3)
	release := make(chan struct{}, 3)
	var calls atomic.Int32
	client := &mockClient{listOpenPRsFn: func(ctx context.Context, _, _ string) ([]*gh.PullRequest, error) {
		entered <- int(calls.Add(1))
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	syncer := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Hour, nil, nil)
	t.Cleanup(syncer.Stop)
	first, ok := syncer.TriggerRunWithPriority(t.Context(), nil)
	require.True(t, ok)
	require.Equal(t, 1, <-entered)
	second, ok := syncer.TriggerRunWithPriority(t.Context(), nil)
	require.True(t, ok)
	third, ok := syncer.TriggerRunWithPriority(t.Context(), nil)
	require.True(t, ok)
	require.Equal(t, first.InstanceID, second.InstanceID)
	require.Equal(t, first.Generation+1, second.Generation)
	require.Equal(t, second.Generation+1, third.Generation)
	release <- struct{}{}
	require.Equal(t, 2, <-entered)
	status := syncer.Status()
	require.True(t, status.Running)
	require.Equal(t, first.Generation, status.FullSync.SuccessfulGeneration)
	release <- struct{}{}
	require.Eventually(t, func() bool { return syncer.Status().FullSync.SettledGeneration == third.Generation }, 5*time.Second, time.Millisecond)
	require.Equal(t, third.Generation, syncer.Status().FullSync.SuccessfulGeneration)
}

func TestFullSyncReceiptsSettleOnStop(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	client := &mockClient{listOpenPRsFn: func(ctx context.Context, _, _ string) ([]*gh.PullRequest, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	syncer := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Hour, nil, nil)
	first, ok := syncer.TriggerRunWithPriority(t.Context(), nil)
	require.True(t, ok)
	<-entered
	queued, ok := syncer.TriggerRunWithPriority(t.Context(), nil)
	require.True(t, ok)
	syncer.Stop()
	status := syncer.Status().FullSync
	require.Equal(t, queued.Generation, status.SettledGeneration)
	require.Less(t, status.SuccessfulGeneration, first.Generation)
	require.NotEmpty(t, status.LastError)
	replacement := NewSyncer(nil, openTestDB(t), nil, nil, time.Hour, nil, nil)
	require.NotEqual(t, first.InstanceID, replacement.Status().FullSync.InstanceID)
}

func TestFullSyncReceiptsPreserveSuccessAfterFailure(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int32
	client := &mockClient{listOpenPRsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("provider unavailable")
		}
		return nil, nil
	}}
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	syncer := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil,
		[]RepoRef{repo}, time.Hour, nil, nil)
	t.Cleanup(syncer.Stop)
	first, ok := syncer.TriggerRunWithPriority(t.Context(), nil)
	require.True(t, ok)
	require.Eventually(t, func() bool {
		return !syncer.Status().Running && syncer.Status().FullSync.SuccessfulGeneration == first.Generation
	}, 5*time.Second, time.Millisecond)
	fail.Store(true)
	second, ok := syncer.TriggerRunWithPriority(t.Context(), nil)
	require.True(t, ok)
	require.Eventually(t, func() bool {
		return !syncer.Status().Running && syncer.Status().FullSync.SettledGeneration == second.Generation
	}, 5*time.Second, time.Millisecond)
	require.Equal(t, first.Generation, syncer.Status().FullSync.SuccessfulGeneration)
	require.Contains(t, syncer.Status().FullSync.LastError, "provider unavailable")
	fail.Store(false)
	require.True(t, syncer.TriggerRunForRepos(t.Context(), []RepoRef{repo}))
	require.Eventually(t, func() bool { return calls.Load() == 3 && !syncer.Status().Running }, 5*time.Second, time.Millisecond)
	require.Equal(t, second.Generation, syncer.Status().FullSync.SettledGeneration)
	require.Equal(t, first.Generation, syncer.Status().FullSync.SuccessfulGeneration)
	require.Contains(t, syncer.Status().FullSync.LastError, "provider unavailable")
}

func TestFullSyncReceiptsPromoteQueuedScopedWork(t *testing.T) {
	entered := make(chan int, 3)
	release := make(chan struct{}, 3)
	var calls atomic.Int32
	client := &mockClient{listOpenPRsFn: func(ctx context.Context, _, _ string) ([]*gh.PullRequest, error) {
		entered <- int(calls.Add(1))
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	syncer := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Hour, nil, nil)
	t.Cleanup(syncer.Stop)
	require.True(t, syncer.TriggerRunForRepos(t.Context(), []RepoRef{repo}))
	require.Equal(t, 1, <-entered)
	require.True(t, syncer.TriggerRunForRepos(t.Context(), []RepoRef{repo}))
	receipt, ok := syncer.TriggerRunWithPriority(t.Context(), nil)
	require.True(t, ok)
	release <- struct{}{}
	require.Equal(t, 2, <-entered)
	require.Zero(t, syncer.Status().FullSync.SettledGeneration)
	release <- struct{}{}
	require.Eventually(t, func() bool { return syncer.Status().FullSync.SuccessfulGeneration == receipt.Generation }, 5*time.Second, time.Millisecond)
}
