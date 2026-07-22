package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKataSnapshotCacheUsesExactKeyWithoutExtendingTTL(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	cache := newKataSnapshotCacheWithConfig(100*time.Millisecond, 128)
	t.Cleanup(cache.close)
	key := kataSnapshotKey{
		DaemonID:          "work",
		DaemonFingerprint: "target-a",
		Scope:             "project",
		ProjectUID:        "project-a",
		Authority:         "ready",
	}
	want := kataAuthoritySnapshot{
		FetchedAt:       time.Unix(123, 0),
		Projects:        []kataProjectSummary{{ID: 7, UID: "project-a", Name: "A", OpenCount: 2}},
		MemberIssueUIDs: []string{"issue-a"},
		Issues:          []kataTaskSummary{{UID: "issue-a"}},
	}
	cache.set(key, want)

	got, ok := cache.get(key)
	require.True(ok)
	require.Equal(want, got)
	_, ok = cache.get(kataSnapshotKey{DaemonID: "work", Scope: "global", Authority: "ready"})
	require.False(ok)
	rotated := key
	rotated.DaemonFingerprint = "target-b"
	_, ok = cache.get(rotated)
	require.False(ok, "a daemon target change must not reuse authority from the previous target")

	time.Sleep(60 * time.Millisecond)
	_, ok = cache.get(key)
	require.True(ok)
	time.Sleep(60 * time.Millisecond)
	_, ok = cache.get(key)
	require.False(ok, "a cache hit must not extend the five-second freshness window")
	require.Eventually(func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return len(cache.keysByDaemon["work"]) == 0
	}, time.Second, 10*time.Millisecond)
}

func TestKataSnapshotCacheCleansDaemonIndexAfterCapacityEviction(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	cache := newKataSnapshotCacheWithConfig(time.Minute, 1)
	t.Cleanup(cache.close)
	first := kataSnapshotKey{DaemonID: "first", Scope: "global", Authority: "open"}
	second := kataSnapshotKey{DaemonID: "second", Scope: "global", Authority: "open"}
	cache.set(first, kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "issue-first"}}})
	cache.set(second, kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "issue-second"}}})

	_, ok := cache.get(first)
	require.False(ok)
	_, ok = cache.get(second)
	require.True(ok)
	require.Eventually(func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return len(cache.keysByDaemon["first"]) == 0 && len(cache.keysByDaemon["second"]) == 1
	}, time.Second, 10*time.Millisecond)
}

func TestKataSnapshotCacheEvictsBySerializedByteBudget(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	firstSnapshot := kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "issue-first", Body: "first authority payload"}}}
	secondSnapshot := kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "issue-second", Body: "second authority payload"}}}
	firstEncoded, err := json.Marshal(firstSnapshot)
	require.NoError(err)
	secondEncoded, err := json.Marshal(secondSnapshot)
	require.NoError(err)
	maxBytes := max(uint64(len(firstEncoded)), uint64(len(secondEncoded)))
	cache := newKataSnapshotCacheWithLimits(time.Minute, 128, maxBytes)
	t.Cleanup(cache.close)
	first := kataSnapshotKey{DaemonID: "first", Scope: "global", Authority: "open"}
	second := kataSnapshotKey{DaemonID: "second", Scope: "global", Authority: "open"}
	cache.set(first, firstSnapshot)
	_, ok := cache.get(first)
	require.True(ok)

	cache.set(second, secondSnapshot)
	_, ok = cache.get(first)
	require.False(ok)
	_, ok = cache.get(second)
	require.True(ok)
}

func TestKataSnapshotCacheInvalidatesOneDaemonAndAdvancesEpoch(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	cache := newKataSnapshotCache()
	t.Cleanup(cache.close)
	first := kataSnapshotKey{DaemonID: "work", Scope: "global", Authority: "open"}
	second := kataSnapshotKey{DaemonID: "work", Scope: "global", Authority: "closed"}
	second.DaemonFingerprint = "rotated-target"
	other := kataSnapshotKey{DaemonID: "home", Scope: "global", Authority: "open"}
	cache.set(first, kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "issue-first"}}})
	cache.set(second, kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "issue-second"}}})
	cache.set(other, kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "issue-other"}}})

	require.Equal(uint64(0), cache.daemonEpoch("work"))
	require.Equal(uint64(1), cache.invalidateDaemon("work"))
	require.Equal(uint64(1), cache.daemonEpoch("work"))
	_, ok := cache.get(first)
	require.False(ok)
	_, ok = cache.get(second)
	require.False(ok)
	_, ok = cache.get(other)
	require.True(ok)

	require.Equal(uint64(2), cache.invalidateDaemon("work"))
}

func TestKataSnapshotCacheRunsExpiryCleanupUntilContextCanceled(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	cache := newKataSnapshotCacheWithConfig(30*time.Millisecond, 128)
	t.Cleanup(cache.close)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		cache.run(ctx)
		close(done)
	}()
	key := kataSnapshotKey{DaemonID: "work", DaemonFingerprint: "target", Scope: "global", Authority: "open"}
	cache.set(key, kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "issue-a"}}})

	require.Eventually(func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return len(cache.keysByDaemon["work"]) == 0
	}, time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		require.Fail("cache cleanup did not stop after context cancellation")
	}
}

func TestKataSnapshotCacheOwnsImmutableSnapshotCopies(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	cache := newKataSnapshotCache()
	t.Cleanup(cache.close)
	key := kataSnapshotKey{DaemonID: "work", DaemonFingerprint: "target", Scope: "global", Authority: "open"}
	lastEventAt := time.Unix(123, 0)
	snapshot := kataAuthoritySnapshot{
		Projects:        []kataProjectSummary{{ID: 7, UID: "project-a", Name: "A", LastEventAt: &lastEventAt}},
		MemberIssueUIDs: []string{"issue-a"},
		Issues:          []kataTaskSummary{{UID: "issue-a"}},
	}
	cache.set(key, snapshot)
	snapshot.Projects[0].Name = "mutated input"
	*snapshot.Projects[0].LastEventAt = time.Unix(456, 0)
	snapshot.MemberIssueUIDs[0] = "mutated-input"
	snapshot.Issues[0].UID = "mutated-input"

	first, ok := cache.get(key)
	require.True(ok)
	require.Equal("A", first.Projects[0].Name)
	require.Equal(time.Unix(123, 0), *first.Projects[0].LastEventAt)
	require.Equal("issue-a", first.MemberIssueUIDs[0])
	require.Equal("issue-a", first.Issues[0].UID)
	first.Projects[0].Name = "mutated output"
	*first.Projects[0].LastEventAt = time.Unix(789, 0)
	first.MemberIssueUIDs[0] = "mutated-output"
	first.Issues[0].UID = "mutated-output"

	second, ok := cache.get(key)
	require.True(ok)
	require.Equal("A", second.Projects[0].Name)
	require.Equal(time.Unix(123, 0), *second.Projects[0].LastEventAt)
	require.Equal("issue-a", second.MemberIssueUIDs[0])
	require.Equal("issue-a", second.Issues[0].UID)
}

func TestKataSnapshotCacheDefaultsToCapacity128(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	cache := newKataSnapshotCache()
	t.Cleanup(cache.close)
	for i := range 129 {
		key := kataSnapshotKey{
			DaemonID:          "work",
			DaemonFingerprint: "target",
			Scope:             "project",
			ProjectUID:        fmt.Sprintf("project-%03d", i),
			Authority:         "open",
		}
		cache.set(key, kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: fmt.Sprintf("issue-%03d", i)}}})
	}

	require.Equal(128, cache.entries.Len())
	_, ok := cache.get(kataSnapshotKey{
		DaemonID:          "work",
		DaemonFingerprint: "target",
		Scope:             "project",
		ProjectUID:        "project-000",
		Authority:         "open",
	})
	require.False(ok)
}

func TestKataSnapshotCacheRejectsInsertionFromInvalidatedEpoch(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	cache := newKataSnapshotCache()
	t.Cleanup(cache.close)
	key := kataSnapshotKey{DaemonID: "work", DaemonFingerprint: "target", Scope: "global", Authority: "open"}
	staleEpoch := cache.daemonEpoch("work")
	require.Equal(uint64(1), cache.invalidateDaemon("work"))

	require.False(cache.setIfDaemonEpoch(key, kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "stale"}}}, staleEpoch))
	_, ok := cache.get(key)
	require.False(ok)

	currentEpoch := cache.daemonEpoch("work")
	require.True(cache.setIfDaemonEpoch(key, kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "current"}}}, currentEpoch))
	got, ok := cache.get(key)
	require.True(ok)
	require.Equal("current", got.Issues[0].UID)
}

func TestKataSnapshotCacheInvalidatesObservedDaemonEpochOnlyOnce(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	cache := newKataSnapshotCache()
	t.Cleanup(cache.close)
	firstKey := kataSnapshotKey{DaemonID: "work", DaemonFingerprint: "first", Scope: "global", Authority: "open"}
	cache.set(firstKey, kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "old"}}})

	newEpoch, invalidated := cache.invalidateDaemonIfEpoch("work", 0)
	require.True(invalidated)
	require.Equal(uint64(1), newEpoch)

	secondKey := kataSnapshotKey{DaemonID: "work", DaemonFingerprint: "second", Scope: "global", Authority: "open"}
	require.True(cache.setIfDaemonEpoch(secondKey, kataAuthoritySnapshot{Issues: []kataTaskSummary{{UID: "new"}}}, newEpoch))

	currentEpoch, invalidated := cache.invalidateDaemonIfEpoch("work", 0)
	require.False(invalidated)
	require.Equal(newEpoch, currentEpoch)
	snapshot, ok := cache.get(secondKey)
	require.True(ok)
	require.Equal("new", snapshot.Issues[0].UID)
}
