package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/kata"
)

func TestKataSnapshotCoordinatorCoalescesAndCachesAuthority(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	root, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	var loads atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	coordinator := newKataSnapshotCoordinator(root, kataSnapshotCoordinatorDeps{
		cache: newKataSnapshotCache(),
		resolveDaemon: func(id string) (kata.Daemon, *ProblemError) {
			return kata.Daemon{ID: id, URL: "http://" + id + ".example"}, nil
		},
		newLoader: func(_ context.Context, daemon kata.Daemon) (kataAuthoritySnapshotLoader, error) {
			return kataAuthoritySnapshotLoaderFunc(func(context.Context, kataAuthorityRequest) (kataAuthoritySnapshot, error) {
				if loads.Add(1) == 1 {
					close(started)
					<-release
				}
				return kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: daemon.ID + "-issue"}}}, nil
			}), nil
		},
		newServerInstanceID: func() string { return "server-a" },
	})
	t.Cleanup(coordinator.close)

	results := make(chan kataCoordinatedAuthority, 2)
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			result, err := coordinator.loadAuthority(t.Context(), "work", kataAuthorityRequest{Scope: "global", Authority: "open"})
			results <- result
			errors <- err
		}()
	}
	<-started
	close(release)

	first := <-results
	second := <-results
	require.NoError(<-errors)
	require.NoError(<-errors)
	require.Equal(int64(1), loads.Load())
	require.Equal("server-a", first.ServerInstanceID)
	require.Equal(first.Generation, second.Generation)
	require.Equal(uint64(1), first.Generation)
	require.Equal("work-issue", first.Snapshot.Issues[0].UID)

	cached, err := coordinator.loadAuthority(t.Context(), "work", kataAuthorityRequest{Scope: "global", Authority: "open"})
	require.NoError(err)
	require.Equal(first.Generation, cached.Generation)
	require.Equal(int64(1), loads.Load())

	other, err := coordinator.loadAuthority(t.Context(), "home", kataAuthorityRequest{Scope: "global", Authority: "open"})
	require.NoError(err)
	require.Equal(uint64(2), other.Generation)
	require.Equal("home-issue", other.Snapshot.Issues[0].UID)
	require.Equal(int64(2), loads.Load())
}

func TestKataSnapshotCoordinatorRetriesLoadInvalidatedInFlight(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	root, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cache := newKataSnapshotCache()
	var loads atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	coordinator := newKataSnapshotCoordinator(root, kataSnapshotCoordinatorDeps{
		cache: cache,
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) {
			return kata.Daemon{ID: "work", URL: "http://work.example"}, nil
		},
		newLoader: func(context.Context, kata.Daemon) (kataAuthoritySnapshotLoader, error) {
			return kataAuthoritySnapshotLoaderFunc(func(context.Context, kataAuthorityRequest) (kataAuthoritySnapshot, error) {
				load := loads.Add(1)
				if load == 1 {
					close(started)
					<-release
				}
				return kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "issue-" + string(rune('0'+load))}}}, nil
			}), nil
		},
		newServerInstanceID: func() string { return "server-a" },
	})
	t.Cleanup(coordinator.close)

	resultCh := make(chan kataCoordinatedAuthority, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := coordinator.loadAuthority(t.Context(), "work", kataAuthorityRequest{Scope: "global", Authority: "open"})
		resultCh <- result
		errCh <- err
	}()
	<-started
	require.Equal(uint64(1), cache.invalidateDaemon("work"))
	close(release)

	result := <-resultCh
	require.NoError(<-errCh)
	require.Equal(int64(2), loads.Load())
	require.Equal("issue-2", result.Snapshot.Issues[0].UID)
	require.Equal(uint64(1), result.InvalidationEpoch)
}

func TestKataSnapshotCoordinatorRetriesWhenDaemonTargetRotates(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	root, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	var resolves atomic.Int64
	var loads atomic.Int64
	coordinator := newKataSnapshotCoordinator(root, kataSnapshotCoordinatorDeps{
		cache: newKataSnapshotCache(),
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) {
			if resolves.Add(1) <= 2 {
				return kata.Daemon{ID: "work", URL: "http://target-1.example"}, nil
			}
			return kata.Daemon{ID: "work", URL: "http://target-2.example"}, nil
		},
		newLoader: func(_ context.Context, daemon kata.Daemon) (kataAuthoritySnapshotLoader, error) {
			return kataAuthoritySnapshotLoaderFunc(func(context.Context, kataAuthorityRequest) (kataAuthoritySnapshot, error) {
				loads.Add(1)
				return kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: daemon.URL}}}, nil
			}), nil
		},
		newServerInstanceID: func() string { return "server-a" },
	})
	t.Cleanup(coordinator.close)

	result, err := coordinator.loadAuthority(t.Context(), "work", kataAuthorityRequest{Scope: "global", Authority: "open"})
	require.NoError(err)
	require.Equal(int64(2), loads.Load())
	require.Equal("http://target-2.example", result.Snapshot.Issues[0].UID)
	require.Equal(kataDaemonTargetFingerprint(kata.Daemon{ID: "work", URL: "http://target-2.example"}), result.Key.DaemonFingerprint)
}

type kataAuthoritySnapshotLoaderFunc func(context.Context, kataAuthorityRequest) (kataAuthoritySnapshot, error)

func (f kataAuthoritySnapshotLoaderFunc) Load(ctx context.Context, request kataAuthorityRequest) (kataAuthoritySnapshot, error) {
	return f(ctx, request)
}

func TestKataSnapshotCoordinatorCallerCancellationDoesNotCancelSharedLoad(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	root, cancelRoot := context.WithCancel(t.Context())
	t.Cleanup(cancelRoot)
	started := make(chan struct{})
	release := make(chan struct{})
	coordinator := newKataSnapshotCoordinator(root, kataSnapshotCoordinatorDeps{
		cache: newKataSnapshotCache(),
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) {
			return kata.Daemon{ID: "work", URL: "http://work.example"}, nil
		},
		newLoader: func(context.Context, kata.Daemon) (kataAuthoritySnapshotLoader, error) {
			return kataAuthoritySnapshotLoaderFunc(func(ctx context.Context, _ kataAuthorityRequest) (kataAuthoritySnapshot, error) {
				close(started)
				select {
				case <-release:
					return kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "issue-a"}}}, nil
				case <-ctx.Done():
					return kataAuthoritySnapshot{}, ctx.Err()
				}
			}), nil
		},
		newServerInstanceID: func() string { return "server-a" },
	})
	t.Cleanup(coordinator.close)

	canceledCtx, cancelCaller := context.WithCancel(t.Context())
	firstErr := make(chan error, 1)
	go func() {
		_, err := coordinator.loadAuthority(canceledCtx, "work", kataAuthorityRequest{Scope: "global", Authority: "open"})
		firstErr <- err
	}()
	<-started
	cancelCaller()
	require.ErrorIs(<-firstErr, context.Canceled)

	secondResult := make(chan kataCoordinatedAuthority, 1)
	secondErr := make(chan error, 1)
	go func() {
		result, err := coordinator.loadAuthority(t.Context(), "work", kataAuthorityRequest{Scope: "global", Authority: "open"})
		secondResult <- result
		secondErr <- err
	}()
	close(release)
	require.NoError(<-secondErr)
	require.Equal("issue-a", (<-secondResult).Snapshot.Issues[0].UID)

	select {
	case <-time.After(10 * time.Millisecond):
	case <-root.Done():
		require.Fail("shared root context was canceled by a caller")
	}
}
