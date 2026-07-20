package server

import (
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
		DaemonID:   "work",
		View:       "tasks",
		Scope:      "project",
		ProjectUID: "project-a",
		Authority:  "ready",
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
	_, ok = cache.get(kataSnapshotKey{DaemonID: "work", View: "tasks", Scope: "global", Authority: "ready"})
	require.False(ok)

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
	first := kataSnapshotKey{DaemonID: "first", View: "tasks", Scope: "global", Authority: "open"}
	second := kataSnapshotKey{DaemonID: "second", View: "tasks", Scope: "global", Authority: "open"}
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

func TestKataSnapshotCacheInvalidatesOneDaemonAndAdvancesEpoch(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	cache := newKataSnapshotCache()
	t.Cleanup(cache.close)
	first := kataSnapshotKey{DaemonID: "work", View: "tasks", Scope: "global", Authority: "open"}
	second := kataSnapshotKey{DaemonID: "work", View: "logbook", Scope: "global", Authority: "closed"}
	other := kataSnapshotKey{DaemonID: "home", View: "tasks", Scope: "global", Authority: "open"}
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
