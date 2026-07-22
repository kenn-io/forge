package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	katagenerated "go.kenn.io/kata/pkg/client/generated"
)

func TestKataSnapshotEnrichmentCacheSharesProjectEventsAcrossIssues(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	var epoch atomic.Uint64
	epoch.Store(3)
	cache := newKataSnapshotEnrichmentCacheWithConfig(time.Minute, 8, func(string) uint64 { return epoch.Load() })
	t.Cleanup(cache.close)
	var loads atomic.Int64
	key := kataProjectEventsCacheKey{DaemonID: "local", DaemonEpoch: 3, ProjectID: 7}
	load := func(_ context.Context, maxBytes uint64) (kataProjectEventsLoadResult, error) {
		loads.Add(1)
		return testKataProjectEventsLoadResult(maxBytes, "", testKataEvent(1, nil, time.Unix(1, 0))), nil
	}

	first, err := cache.projectEvents(t.Context(), key, "issue-a", load)
	require.NoError(err)
	second, err := cache.projectEvents(t.Context(), key, "issue-b", load)
	require.NoError(err)
	assert.Equal(first.Events, second.Events)
	assert.Equal(int64(1), loads.Load())

	epoch.Store(4)
	cache.invalidateDaemon("local", 4)
	key.DaemonEpoch = 4
	_, err = cache.projectEvents(t.Context(), key, "issue-a", load)
	require.NoError(err)
	assert.Equal(int64(2), loads.Load())
}

func TestKataSnapshotEnrichmentCacheCoalescesConcurrentProjectEventsAcrossIssues(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	cache := newKataSnapshotEnrichmentCacheWithConfig(time.Minute, 8, func(string) uint64 { return 0 })
	t.Cleanup(cache.close)
	key := kataProjectEventsCacheKey{DaemonID: "local", ProjectID: 7}
	issueA := "issue-a"
	issueB := "issue-b"
	loadStarted := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseLoad := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseLoad)
	loadFor := func(selectedUID string) func(context.Context, uint64) (kataProjectEventsLoadResult, error) {
		return func(ctx context.Context, maxBytes uint64) (kataProjectEventsLoadResult, error) {
			loadStarted <- selectedUID
			select {
			case <-release:
				return testKataProjectEventsLoadResult(
					maxBytes,
					selectedUID,
					testKataEvent(1, &issueA, time.Unix(1, 0)),
					testKataEvent(2, &issueB, time.Unix(2, 0)),
				), nil
			case <-ctx.Done():
				return kataProjectEventsLoadResult{}, ctx.Err()
			}
		}
	}
	type callResult struct {
		value kataProjectEventsResult
		err   error
	}
	resultA := make(chan callResult, 1)
	resultB := make(chan callResult, 1)
	go func() {
		value, err := cache.projectEvents(t.Context(), key, issueA, loadFor(issueA))
		resultA <- callResult{value: value, err: err}
	}()
	require.Equal(issueA, <-loadStarted)
	go func() {
		value, err := cache.projectEvents(t.Context(), key, issueB, loadFor(issueB))
		resultB <- callResult{value: value, err: err}
	}()
	select {
	case selectedUID := <-loadStarted:
		releaseLoad()
		require.Failf("duplicate concurrent load", "loader for %q started before the shared project load completed", selectedUID)
	case <-time.After(100 * time.Millisecond):
	}
	releaseLoad()

	first := <-resultA
	second := <-resultB
	require.NoError(first.err)
	require.NoError(second.err)
	assert.True(first.value.CompleteProject)
	assert.True(second.value.CompleteProject)
	assert.Equal([]katagenerated.EventEnvelope{testKataEvent(1, &issueA, time.Unix(1, 0))}, filterKataProjectEvents(first.value.Events, issueA))
	assert.Equal([]katagenerated.EventEnvelope{testKataEvent(2, &issueB, time.Unix(2, 0))}, filterKataProjectEvents(second.value.Events, issueB))
	assert.Empty(loadStarted)
}

func TestKataSnapshotEnrichmentCacheDoesNotTouchTTLOnHit(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	cache := newKataSnapshotEnrichmentCacheWithConfig(80*time.Millisecond, 8, func(string) uint64 { return 0 })
	t.Cleanup(cache.close)
	var loads atomic.Int64
	key := kataProjectEventsCacheKey{DaemonID: "local", ProjectID: 7}
	load := func(_ context.Context, maxBytes uint64) (kataProjectEventsLoadResult, error) {
		loads.Add(1)
		return testKataProjectEventsLoadResult(maxBytes, "", testKataEvent(1, nil, time.Unix(1, 0))), nil
	}

	_, err := cache.projectEvents(t.Context(), key, "issue-a", load)
	require.NoError(err)
	time.Sleep(50 * time.Millisecond)
	_, err = cache.projectEvents(t.Context(), key, "issue-a", load)
	require.NoError(err)
	time.Sleep(40 * time.Millisecond)
	_, err = cache.projectEvents(t.Context(), key, "issue-a", load)
	require.NoError(err)

	assert.Equal(int64(2), loads.Load())
}

func TestKataSnapshotEnrichmentCacheBoundsEntriesWithoutTruncatingResults(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	cache := newKataSnapshotEnrichmentCacheWithLimits(t.Context(), time.Minute, 1, 1<<20, func(string) uint64 { return 0 })
	t.Cleanup(cache.close)
	var loads atomic.Int64
	load := func(eventID int64) func(context.Context, uint64) (kataProjectEventsLoadResult, error) {
		return func(_ context.Context, maxBytes uint64) (kataProjectEventsLoadResult, error) {
			loads.Add(1)
			return testKataProjectEventsLoadResult(maxBytes, "", testKataEvent(eventID, nil, time.Unix(eventID, 0))), nil
		}
	}
	firstKey := kataProjectEventsCacheKey{DaemonID: "local", ProjectID: 7}
	secondKey := kataProjectEventsCacheKey{DaemonID: "local", ProjectID: 8}

	first, err := cache.projectEvents(t.Context(), firstKey, "issue-a", load(1))
	require.NoError(err)
	second, err := cache.projectEvents(t.Context(), secondKey, "issue-a", load(2))
	require.NoError(err)
	reloaded, err := cache.projectEvents(t.Context(), firstKey, "issue-a", load(1))
	require.NoError(err)

	require.Len(first.Events, 1)
	require.Len(second.Events, 1)
	require.Len(reloaded.Events, 1)
	assert.Equal(int64(1), first.Events[0].EventID)
	assert.Equal(int64(2), second.Events[0].EventID)
	assert.Equal(int64(1), reloaded.Events[0].EventID)
	assert.Equal(int64(3), loads.Load())
}

func TestKataSnapshotEnrichmentCacheReturnsOversizedProjectEventsWithoutCaching(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	cache := newKataSnapshotEnrichmentCacheWithLimits(t.Context(), time.Minute, 8, 64, func(string) uint64 { return 0 })
	t.Cleanup(cache.close)
	var loads atomic.Int64
	issueUID := strings.Repeat("issue", 40)
	key := kataProjectEventsCacheKey{DaemonID: "local", ProjectID: 7}
	load := func(_ context.Context, maxBytes uint64) (kataProjectEventsLoadResult, error) {
		loads.Add(1)
		return testKataProjectEventsLoadResult(maxBytes, issueUID, testKataEvent(1, &issueUID, time.Unix(1, 0))), nil
	}

	first, err := cache.projectEvents(t.Context(), key, issueUID, load)
	require.NoError(err)
	second, err := cache.projectEvents(t.Context(), key, issueUID, load)
	require.NoError(err)

	require.Len(first.Events, 1)
	require.Len(second.Events, 1)
	assert.Equal(issueUID, *first.Events[0].IssueUID)
	assert.Equal(issueUID, *second.Events[0].IssueUID)
	assert.Equal(int64(2), loads.Load())
}

func TestKataSnapshotEnrichmentCacheRetriesConcurrentOversizedProjectEventsPerIssue(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	cache := newKataSnapshotEnrichmentCacheWithLimits(t.Context(), time.Minute, 8, 64, func(string) uint64 { return 0 })
	t.Cleanup(cache.close)
	key := kataProjectEventsCacheKey{DaemonID: "local", ProjectID: 7}
	issueA := strings.Repeat("issue-a", 40)
	issueB := strings.Repeat("issue-b", 40)
	loadStarted := make(chan string, 2)
	releaseA := make(chan struct{})
	var releaseOnce sync.Once
	releaseFirstLoad := func() { releaseOnce.Do(func() { close(releaseA) }) }
	t.Cleanup(releaseFirstLoad)
	loadFor := func(selectedUID string) func(context.Context, uint64) (kataProjectEventsLoadResult, error) {
		return func(ctx context.Context, maxBytes uint64) (kataProjectEventsLoadResult, error) {
			loadStarted <- selectedUID
			if selectedUID == issueA {
				select {
				case <-releaseA:
				case <-ctx.Done():
					return kataProjectEventsLoadResult{}, ctx.Err()
				}
			}
			return testKataProjectEventsLoadResult(
				maxBytes,
				selectedUID,
				testKataEvent(1, &selectedUID, time.Unix(1, 0)),
			), nil
		}
	}
	type callResult struct {
		value kataProjectEventsResult
		err   error
	}
	resultA := make(chan callResult, 1)
	resultB := make(chan callResult, 1)
	go func() {
		value, err := cache.projectEvents(t.Context(), key, issueA, loadFor(issueA))
		resultA <- callResult{value: value, err: err}
	}()
	require.Equal(issueA, <-loadStarted)
	go func() {
		value, err := cache.projectEvents(t.Context(), key, issueB, loadFor(issueB))
		resultB <- callResult{value: value, err: err}
	}()
	select {
	case selectedUID := <-loadStarted:
		releaseFirstLoad()
		require.Failf("premature selected load", "loader for %q started before the shared project load completed", selectedUID)
	case <-time.After(100 * time.Millisecond):
	}
	releaseFirstLoad()

	first := <-resultA
	second := <-resultB
	require.NoError(first.err)
	require.NoError(second.err)
	require.Len(first.value.Events, 1)
	require.Len(second.value.Events, 1)
	assert.False(first.value.CompleteProject)
	assert.False(second.value.CompleteProject)
	assert.Equal(issueA, first.value.SelectedUID)
	assert.Equal(issueB, second.value.SelectedUID)
	assert.Equal(issueA, *first.value.Events[0].IssueUID)
	assert.Equal(issueB, *second.value.Events[0].IssueUID)
	select {
	case selectedUID := <-loadStarted:
		assert.Equal(issueB, selectedUID)
	case <-time.After(time.Second):
		require.Fail("selected fallback did not load")
	}
	assert.Empty(loadStarted)
}

func TestKataSnapshotEnrichmentCacheSeparatesDetailAndGraphKeys(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	cache := newKataSnapshotEnrichmentCacheWithConfig(time.Minute, 8, func(string) uint64 { return 2 })
	t.Cleanup(cache.close)
	var detailLoads atomic.Int64
	var graphLoads atomic.Int64
	detailKey := kataIssueDetailCacheKey{DaemonID: "local", DaemonEpoch: 2, IssueUID: "issue-a"}
	detailLoad := func(context.Context) (kataCachedIssueDetail, error) {
		detailLoads.Add(1)
		response := testKataShowIssueResponse("issue-a")
		return kataCachedIssueDetail{Body: response.JSON200, Issue: response.JSON200.Issue}, nil
	}
	otherDetailKey := detailKey
	otherDetailKey.IssueUID = "issue-b"
	graphKey := kataGraphCacheKey{DaemonID: "local", DaemonEpoch: 2, SourceUID: "issue-a", Depth: "full"}
	graphLoad := func(context.Context) (kataCachedGraph, error) {
		graphLoads.Add(1)
		return kataCachedGraph{Body: testKataGraphResponse("issue-a", "issue-b").JSON200}, nil
	}
	otherGraphKey := graphKey
	otherGraphKey.HideDone = true

	_, err := cache.issueDetail(t.Context(), detailKey, detailLoad)
	require.NoError(err)
	_, err = cache.issueDetail(t.Context(), detailKey, detailLoad)
	require.NoError(err)
	_, err = cache.issueDetail(t.Context(), otherDetailKey, detailLoad)
	require.NoError(err)
	_, err = cache.graph(t.Context(), graphKey, graphLoad)
	require.NoError(err)
	_, err = cache.graph(t.Context(), graphKey, graphLoad)
	require.NoError(err)
	_, err = cache.graph(t.Context(), otherGraphKey, graphLoad)
	require.NoError(err)

	assert.Equal(int64(2), detailLoads.Load())
	assert.Equal(int64(2), graphLoads.Load())
}

func TestKataSnapshotEnrichmentCacheCoalescesLoadsWithoutCallerCancellation(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	cache := newKataSnapshotEnrichmentCacheWithConfig(time.Minute, 8, func(string) uint64 { return 0 })
	t.Cleanup(cache.close)
	key := kataProjectEventsCacheKey{DaemonID: "local", ProjectID: 7}
	started := make(chan struct{})
	release := make(chan struct{})
	var loads atomic.Int64
	load := func(ctx context.Context, maxBytes uint64) (kataProjectEventsLoadResult, error) {
		if loads.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return testKataProjectEventsLoadResult(maxBytes, "", testKataEvent(1, nil, time.Unix(1, 0))), nil
		case <-ctx.Done():
			return kataProjectEventsLoadResult{}, ctx.Err()
		}
	}

	canceledCtx, cancel := context.WithCancel(t.Context())
	firstErr := make(chan error, 1)
	go func() {
		_, err := cache.projectEvents(canceledCtx, key, "issue-a", load)
		firstErr <- err
	}()
	<-started
	secondResult := make(chan kataProjectEventsResult, 1)
	secondErr := make(chan error, 1)
	go func() {
		result, err := cache.projectEvents(t.Context(), key, "issue-a", load)
		secondResult <- result
		secondErr <- err
	}()
	cancel()
	require.ErrorIs(<-firstErr, context.Canceled)
	close(release)
	require.NoError(<-secondErr)
	assert.Len((<-secondResult).Events, 1)
	assert.Equal(int64(1), loads.Load())
}

func TestKataSnapshotEnrichmentCacheDoesNotCacheErrors(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	cache := newKataSnapshotEnrichmentCache(func(string) uint64 { return 0 })
	t.Cleanup(cache.close)
	var loads atomic.Int64
	key := kataGraphCacheKey{DaemonID: "local", SourceUID: "issue-a", Depth: "full"}
	load := func(context.Context) (kataCachedGraph, error) {
		if loads.Add(1) == 1 {
			return kataCachedGraph{}, errors.New("temporary")
		}
		return kataCachedGraph{Body: testKataGraphResponse("issue-a", "issue-b").JSON200}, nil
	}

	_, err := cache.graph(t.Context(), key, load)
	require.Error(err)
	graph, err := cache.graph(t.Context(), key, load)
	require.NoError(err)

	assert.Equal("issue-a", graph.Body.SourceUID)
	assert.Equal(int64(2), loads.Load())
}

func TestKataSnapshotEnrichmentCacheRejectsOldEpochAfterAdvancement(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	var currentEpoch atomic.Uint64
	currentEpoch.Store(3)
	cache := newKataSnapshotEnrichmentCacheWithConfig(time.Minute, 8, func(string) uint64 { return currentEpoch.Load() })
	t.Cleanup(cache.close)
	oldKey := kataProjectEventsCacheKey{DaemonID: "local", DaemonEpoch: 3, ProjectID: 7}
	newKey := kataProjectEventsCacheKey{DaemonID: "local", DaemonEpoch: 4, ProjectID: 7}
	var mu sync.Mutex
	loads := map[uint64]int{}
	load := func(epoch uint64) func(context.Context, uint64) (kataProjectEventsLoadResult, error) {
		return func(_ context.Context, maxBytes uint64) (kataProjectEventsLoadResult, error) {
			mu.Lock()
			loads[epoch]++
			mu.Unlock()
			return testKataProjectEventsLoadResult(maxBytes, "", testKataEvent(int64(epoch), nil, time.Unix(int64(epoch), 0))), nil
		}
	}

	_, err := cache.projectEvents(t.Context(), oldKey, "issue-a", load(3))
	require.NoError(err)
	currentEpoch.Store(4)
	cache.invalidateDaemon("local", 4)
	_, err = cache.projectEvents(t.Context(), newKey, "issue-a", load(4))
	require.NoError(err)
	_, err = cache.projectEvents(t.Context(), oldKey, "issue-a", load(3))
	require.ErrorIs(err, errKataSnapshotEnrichmentStale)
	_, err = cache.projectEvents(t.Context(), oldKey, "issue-a", load(3))
	require.ErrorIs(err, errKataSnapshotEnrichmentStale)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(1, loads[4])
	assert.Equal(1, loads[3], "old-epoch values must not load after authority advances")
}

func testKataProjectEventsLoadResult(
	maxBytes uint64,
	selectedUID string,
	events ...katagenerated.EventEnvelope,
) kataProjectEventsLoadResult {
	result := newKataProjectEventsLoadResult(maxBytes)
	for _, event := range events {
		if event.IssueUID != nil && *event.IssueUID == selectedUID {
			result.SelectedHistory = append(result.SelectedHistory, event)
		}
		result.admitProjectEvent(event, maxBytes)
	}
	return result
}
