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
	load := func(context.Context) ([]katagenerated.EventEnvelope, error) {
		loads.Add(1)
		return []katagenerated.EventEnvelope{testKataEvent(1, nil, time.Unix(1, 0))}, nil
	}

	first, err := cache.projectEvents(t.Context(), key, load)
	require.NoError(err)
	second, err := cache.projectEvents(t.Context(), key, load)
	require.NoError(err)
	assert.Equal(first, second)
	assert.Equal(int64(1), loads.Load())

	epoch.Store(4)
	cache.invalidateDaemon("local", 4)
	key.DaemonEpoch = 4
	_, err = cache.projectEvents(t.Context(), key, load)
	require.NoError(err)
	assert.Equal(int64(2), loads.Load())
}

func TestKataSnapshotEnrichmentCacheDoesNotTouchTTLOnHit(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	cache := newKataSnapshotEnrichmentCacheWithConfig(80*time.Millisecond, 8, func(string) uint64 { return 0 })
	t.Cleanup(cache.close)
	var loads atomic.Int64
	key := kataProjectEventsCacheKey{DaemonID: "local", ProjectID: 7}
	load := func(context.Context) ([]katagenerated.EventEnvelope, error) {
		loads.Add(1)
		return []katagenerated.EventEnvelope{testKataEvent(1, nil, time.Unix(1, 0))}, nil
	}

	_, err := cache.projectEvents(t.Context(), key, load)
	require.NoError(err)
	time.Sleep(50 * time.Millisecond)
	_, err = cache.projectEvents(t.Context(), key, load)
	require.NoError(err)
	time.Sleep(40 * time.Millisecond)
	_, err = cache.projectEvents(t.Context(), key, load)
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
	load := func(eventID int64) func(context.Context) ([]katagenerated.EventEnvelope, error) {
		return func(context.Context) ([]katagenerated.EventEnvelope, error) {
			loads.Add(1)
			return []katagenerated.EventEnvelope{testKataEvent(eventID, nil, time.Unix(eventID, 0))}, nil
		}
	}
	firstKey := kataProjectEventsCacheKey{DaemonID: "local", ProjectID: 7}
	secondKey := kataProjectEventsCacheKey{DaemonID: "local", ProjectID: 8}

	first, err := cache.projectEvents(t.Context(), firstKey, load(1))
	require.NoError(err)
	second, err := cache.projectEvents(t.Context(), secondKey, load(2))
	require.NoError(err)
	reloaded, err := cache.projectEvents(t.Context(), firstKey, load(1))
	require.NoError(err)

	require.Len(first, 1)
	require.Len(second, 1)
	require.Len(reloaded, 1)
	assert.Equal(int64(1), first[0].EventID)
	assert.Equal(int64(2), second[0].EventID)
	assert.Equal(int64(1), reloaded[0].EventID)
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
	load := func(context.Context) ([]katagenerated.EventEnvelope, error) {
		loads.Add(1)
		return []katagenerated.EventEnvelope{testKataEvent(1, &issueUID, time.Unix(1, 0))}, nil
	}

	first, err := cache.projectEvents(t.Context(), key, load)
	require.NoError(err)
	second, err := cache.projectEvents(t.Context(), key, load)
	require.NoError(err)

	require.Len(first, 1)
	require.Len(second, 1)
	assert.Equal(issueUID, *first[0].IssueUID)
	assert.Equal(issueUID, *second[0].IssueUID)
	assert.Equal(int64(2), loads.Load())
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
	graphLoad := func(context.Context) (*katagenerated.ReachableGraphResponseBody, error) {
		graphLoads.Add(1)
		return testKataGraphResponse("issue-a", "issue-b").JSON200, nil
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
	load := func(ctx context.Context) ([]katagenerated.EventEnvelope, error) {
		if loads.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return []katagenerated.EventEnvelope{testKataEvent(1, nil, time.Unix(1, 0))}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	canceledCtx, cancel := context.WithCancel(t.Context())
	firstErr := make(chan error, 1)
	go func() {
		_, err := cache.projectEvents(canceledCtx, key, load)
		firstErr <- err
	}()
	<-started
	secondResult := make(chan []katagenerated.EventEnvelope, 1)
	secondErr := make(chan error, 1)
	go func() {
		result, err := cache.projectEvents(t.Context(), key, load)
		secondResult <- result
		secondErr <- err
	}()
	cancel()
	require.ErrorIs(<-firstErr, context.Canceled)
	close(release)
	require.NoError(<-secondErr)
	assert.Len(<-secondResult, 1)
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
	load := func(context.Context) (*katagenerated.ReachableGraphResponseBody, error) {
		if loads.Add(1) == 1 {
			return nil, errors.New("temporary")
		}
		return testKataGraphResponse("issue-a", "issue-b").JSON200, nil
	}

	_, err := cache.graph(t.Context(), key, load)
	require.Error(err)
	graph, err := cache.graph(t.Context(), key, load)
	require.NoError(err)

	assert.Equal("issue-a", graph.SourceUID)
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
	load := func(epoch uint64) func(context.Context) ([]katagenerated.EventEnvelope, error) {
		return func(context.Context) ([]katagenerated.EventEnvelope, error) {
			mu.Lock()
			loads[epoch]++
			mu.Unlock()
			return []katagenerated.EventEnvelope{testKataEvent(int64(epoch), nil, time.Unix(int64(epoch), 0))}, nil
		}
	}

	_, err := cache.projectEvents(t.Context(), oldKey, load(3))
	require.NoError(err)
	currentEpoch.Store(4)
	cache.invalidateDaemon("local", 4)
	_, err = cache.projectEvents(t.Context(), newKey, load(4))
	require.NoError(err)
	_, err = cache.projectEvents(t.Context(), oldKey, load(3))
	require.ErrorIs(err, errKataSnapshotEnrichmentStale)
	_, err = cache.projectEvents(t.Context(), oldKey, load(3))
	require.ErrorIs(err, errKataSnapshotEnrichmentStale)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(1, loads[4])
	assert.Equal(1, loads[3], "old-epoch values must not load after authority advances")
}
