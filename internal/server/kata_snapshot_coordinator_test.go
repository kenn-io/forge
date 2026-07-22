package server

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	katagenerated "go.kenn.io/kata/pkg/client/generated"
	"go.kenn.io/middleman/internal/kata"
)

func TestKataSnapshotCoordinatorInvalidatesAllEnrichmentReads(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	authorityCache := newKataSnapshotCache()
	enrichmentCache := newKataSnapshotEnrichmentCacheWithConfig(time.Minute, 8, authorityCache.daemonEpoch)
	coordinator := newKataSnapshotCoordinator(t.Context(), kataSnapshotCoordinatorDeps{
		cache:           authorityCache,
		enrichmentCache: enrichmentCache,
		newServerInstanceID: func() string {
			return "server-a"
		},
	})
	t.Cleanup(coordinator.close)
	var detailLoads atomic.Int64
	var eventLoads atomic.Int64
	var graphLoads atomic.Int64
	loadAll := func(epoch uint64) {
		t.Helper()
		response := testKataShowIssueResponse("issue-member")
		_, err := enrichmentCache.issueDetail(t.Context(), kataIssueDetailCacheKey{
			DaemonID: "local", DaemonEpoch: epoch, IssueUID: "issue-member",
		}, func(context.Context) (kataCachedIssueDetail, error) {
			detailLoads.Add(1)
			return kataCachedIssueDetail{Body: response.JSON200, Issue: response.JSON200.Issue}, nil
		})
		require.NoError(err)
		_, err = enrichmentCache.projectEvents(t.Context(), kataProjectEventsCacheKey{
			DaemonID: "local", DaemonEpoch: epoch, ProjectID: 7,
		}, func(context.Context) ([]katagenerated.EventEnvelope, error) {
			eventLoads.Add(1)
			return []katagenerated.EventEnvelope{testKataEvent(1, nil, time.Unix(1, 0))}, nil
		})
		require.NoError(err)
		_, err = enrichmentCache.graph(t.Context(), kataGraphCacheKey{
			DaemonID: "local", DaemonEpoch: epoch, SourceUID: "issue-source", Depth: "full",
		}, func(context.Context) (*katagenerated.ReachableGraphResponseBody, error) {
			graphLoads.Add(1)
			return testKataGraphResponse("issue-source", "issue-linked").JSON200, nil
		})
		require.NoError(err)
	}

	loadAll(0)
	loadAll(0)
	require.Equal(uint64(1), coordinator.invalidateDaemon("local"))
	loadAll(1)

	require.Equal(int64(2), detailLoads.Load())
	require.Equal(int64(2), eventLoads.Load())
	require.Equal(int64(2), graphLoads.Load())
}

func TestKataSnapshotCoordinatorRejectsEnrichmentCompletedAfterTargetRotation(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	root, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	authorityCache := newKataSnapshotCache()
	enrichmentCache := newKataSnapshotEnrichmentCacheWithConfig(time.Minute, 8, authorityCache.daemonEpoch)
	var daemonURL atomic.Value
	daemonURL.Store("http://target-a.example")
	coordinator := newKataSnapshotCoordinator(root, kataSnapshotCoordinatorDeps{
		cache:           authorityCache,
		enrichmentCache: enrichmentCache,
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) {
			return kata.Daemon{ID: "work", URL: daemonURL.Load().(string)}, nil
		},
		newLoader: func(_ context.Context, daemon kata.Daemon) (kataAuthoritySnapshotLoader, error) {
			return kataAuthoritySnapshotLoaderFunc(func(context.Context, kataAuthorityRequest) (kataAuthoritySnapshot, error) {
				return kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: daemon.URL}}}, nil
			}), nil
		},
		newServerInstanceID: func() string { return "server-a" },
	})
	t.Cleanup(coordinator.close)

	firstAuthority, err := coordinator.loadAuthority(t.Context(), "work", kataAuthorityRequest{Scope: "global", Authority: "open"})
	require.NoError(err)
	require.Equal(uint64(0), firstAuthority.InvalidationEpoch)
	started := make(chan struct{})
	release := make(chan struct{})
	oldErr := make(chan error, 1)
	go func() {
		_, loadErr := enrichmentCache.projectEvents(t.Context(), kataProjectEventsCacheKey{
			DaemonID: "work", DaemonEpoch: firstAuthority.InvalidationEpoch, ProjectID: 7,
		}, func(context.Context) ([]katagenerated.EventEnvelope, error) {
			close(started)
			<-release
			return []katagenerated.EventEnvelope{testKataEvent(1, nil, time.Unix(1, 0))}, nil
		})
		oldErr <- loadErr
	}()
	<-started

	daemonURL.Store("http://target-b.example")
	rotatedAuthority, err := coordinator.loadAuthority(t.Context(), "work", kataAuthorityRequest{Scope: "global", Authority: "open"})
	require.NoError(err)
	require.Equal(uint64(1), rotatedAuthority.InvalidationEpoch)
	close(release)
	require.Error(<-oldErr, "an old-epoch load completed after rotation must not be accepted")

	var freshLoads atomic.Int64
	fresh, err := enrichmentCache.projectEvents(t.Context(), kataProjectEventsCacheKey{
		DaemonID: "work", DaemonEpoch: rotatedAuthority.InvalidationEpoch, ProjectID: 7,
	}, func(context.Context) ([]katagenerated.EventEnvelope, error) {
		freshLoads.Add(1)
		return []katagenerated.EventEnvelope{testKataEvent(2, nil, time.Unix(2, 0))}, nil
	})
	require.NoError(err)
	require.Len(fresh, 1)
	require.Equal(int64(2), fresh[0].EventID)
	require.Equal(int64(1), freshLoads.Load())
}

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

func TestKataSnapshotCoordinatorDoesNotDeliverCacheHitInvalidatedDuringTargetCheck(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	root, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cache := newKataSnapshotCache()
	key := kataSnapshotKey{
		DaemonID:          "work",
		DaemonFingerprint: kataDaemonTargetFingerprint(kata.Daemon{ID: "work", URL: "http://work.example"}),
		Scope:             "global",
		Authority:         "open",
	}
	cache.set(key, kataAuthoritySnapshot{Generation: 1, Issues: []kataTaskSummary{{UID: "old"}}})
	var resolves atomic.Int64
	coordinator := newKataSnapshotCoordinator(root, kataSnapshotCoordinatorDeps{
		cache: cache,
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) {
			if resolves.Add(1) == 2 {
				epoch := cache.invalidateDaemon("work")
				require.True(cache.setIfDaemonEpoch(key, kataAuthoritySnapshot{Generation: 2, Issues: []kataTaskSummary{{UID: "new"}}}, epoch))
			}
			return kata.Daemon{ID: "work", URL: "http://work.example"}, nil
		},
		newServerInstanceID: func() string { return "server-a" },
	})
	t.Cleanup(coordinator.close)

	result, err := coordinator.loadAuthority(t.Context(), "work", kataAuthorityRequest{Scope: "global", Authority: "open"})
	require.NoError(err)
	require.Equal(uint64(1), result.InvalidationEpoch)
	require.Equal("new", result.Snapshot.Issues[0].UID)
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

func TestKataSnapshotCoordinatorDoesNotResurrectCachedTargetAfterRoundTripRotation(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	root, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	currentURL := "http://target-a.example"
	var loads atomic.Int64
	coordinator := newKataSnapshotCoordinator(root, kataSnapshotCoordinatorDeps{
		cache: newKataSnapshotCache(),
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) {
			return kata.Daemon{ID: "work", URL: currentURL}, nil
		},
		newLoader: func(_ context.Context, daemon kata.Daemon) (kataAuthoritySnapshotLoader, error) {
			return kataAuthoritySnapshotLoaderFunc(func(context.Context, kataAuthorityRequest) (kataAuthoritySnapshot, error) {
				load := loads.Add(1)
				return kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: daemon.URL + "-" + string(rune('0'+load))}}}, nil
			}), nil
		},
		newServerInstanceID: func() string { return "server-a" },
	})
	t.Cleanup(coordinator.close)
	request := kataAuthorityRequest{Scope: "global", Authority: "open"}

	first, err := coordinator.loadAuthority(t.Context(), "work", request)
	require.NoError(err)
	currentURL = "http://target-b.example"
	second, err := coordinator.loadAuthority(t.Context(), "work", request)
	require.NoError(err)
	currentURL = "http://target-a.example"
	third, err := coordinator.loadAuthority(t.Context(), "work", request)
	require.NoError(err)

	require.Equal(int64(3), loads.Load())
	require.Equal(uint64(1), first.Generation)
	require.Equal(uint64(2), second.Generation)
	require.Equal(uint64(3), third.Generation)
	require.Equal("http://target-a.example-3", third.Snapshot.Issues[0].UID)
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

func TestKataSnapshotCoordinatorRunWaitsForDetachedSharedLoad(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	root, cancelRoot := context.WithCancel(t.Context())
	started := make(chan struct{})
	release := make(chan struct{})
	coordinator := newKataSnapshotCoordinator(root, kataSnapshotCoordinatorDeps{
		cache: newKataSnapshotCache(),
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) {
			return kata.Daemon{ID: "work", URL: "http://work.example"}, nil
		},
		newLoader: func(context.Context, kata.Daemon) (kataAuthoritySnapshotLoader, error) {
			return kataAuthoritySnapshotLoaderFunc(func(context.Context, kataAuthorityRequest) (kataAuthoritySnapshot, error) {
				close(started)
				<-release
				return kataAuthoritySnapshot{}, context.Canceled
			}), nil
		},
		newServerInstanceID: func() string { return "server-a" },
	})

	runDone := make(chan struct{})
	go func() {
		coordinator.run(root)
		close(runDone)
	}()
	callerCtx, cancelCaller := context.WithCancel(t.Context())
	loadDone := make(chan error, 1)
	go func() {
		_, err := coordinator.loadAuthority(callerCtx, "work", kataAuthorityRequest{Scope: "global", Authority: "open"})
		loadDone <- err
	}()
	<-started
	cancelCaller()
	require.ErrorIs(<-loadDone, context.Canceled)
	cancelRoot()

	select {
	case <-runDone:
		require.Fail("coordinator run returned before detached shared load completed")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	select {
	case <-runDone:
	case <-time.After(time.Second):
		require.Fail("coordinator run did not wait for detached shared load")
	}
}

func TestKataSnapshotCoordinatorRetriesCrossResponseInconsistency(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	root, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	var loads atomic.Int64
	coordinator := newKataSnapshotCoordinator(root, kataSnapshotCoordinatorDeps{
		cache: newKataSnapshotCache(),
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) {
			return kata.Daemon{ID: "work", URL: "http://work.example"}, nil
		},
		newLoader: func(context.Context, kata.Daemon) (kataAuthoritySnapshotLoader, error) {
			return kataAuthoritySnapshotLoaderFunc(func(context.Context, kataAuthorityRequest) (kataAuthoritySnapshot, error) {
				if loads.Add(1) == 1 {
					return kataAuthoritySnapshot{}, errKataAuthorityInconsistent
				}
				return kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "issue-a"}}}, nil
			}), nil
		},
		newServerInstanceID: func() string { return "server-a" },
	})
	t.Cleanup(coordinator.close)

	result, err := coordinator.loadAuthority(t.Context(), "work", kataAuthorityRequest{Scope: "global", Authority: "open"})
	require.NoError(err)
	require.Equal(int64(2), loads.Load())
	require.Equal("issue-a", result.Snapshot.Issues[0].UID)
}

func TestKataSnapshotCoordinatorBoundsPersistentCrossResponseInconsistency(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	root, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	var loads atomic.Int64
	coordinator := newKataSnapshotCoordinator(root, kataSnapshotCoordinatorDeps{
		cache: newKataSnapshotCache(),
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) {
			return kata.Daemon{ID: "work", URL: "http://work.example"}, nil
		},
		newLoader: func(context.Context, kata.Daemon) (kataAuthoritySnapshotLoader, error) {
			return kataAuthoritySnapshotLoaderFunc(func(context.Context, kataAuthorityRequest) (kataAuthoritySnapshot, error) {
				loads.Add(1)
				return kataAuthoritySnapshot{}, errKataAuthorityInconsistent
			}), nil
		},
		newServerInstanceID: func() string { return "server-a" },
	})
	t.Cleanup(coordinator.close)

	_, err := coordinator.loadAuthority(t.Context(), "work", kataAuthorityRequest{Scope: "global", Authority: "open"})
	require.Error(err)
	require.Equal(int64(2), loads.Load())
	problem, ok := err.(*ProblemError)
	require.True(ok, "want *ProblemError, got %T", err)
	require.Equal(502, problem.Status)
}

func TestValidateKataAuthorityRequestRejectsPaddedProjectUID(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	err := validateKataAuthorityRequest(kataAuthorityRequest{
		Scope:      "project",
		ProjectUID: " project-a ",
		Authority:  "open",
	})
	require.Error(err)
	problem, ok := err.(*ProblemError)
	require.True(ok, "want *ProblemError, got %T", err)
	require.Equal(http.StatusBadRequest, problem.Status)
}
