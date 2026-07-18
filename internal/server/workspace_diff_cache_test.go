package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/workspace"
)

func TestWorkspaceDiffCacheMissThenHitPreparesOnce(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	now := time.Unix(100, 0)
	prepareCalls := 0
	key := workspaceDiffTestKey()
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
		now: func() time.Time { return now },
		resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			return workspaceDiffTestResolved(), true, nil
		},
		fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			return "v1", nil
		},
		prepare: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			prepareCalls++
			return workspaceDiffTestResult("one.txt"), nil
		},
	})

	first, state, err := cache.Get(t.Context(), key)
	require.NoError(err)
	require.NotNil(first)
	assert.Equal(workspaceDiffCacheMiss, state)
	assert.False(first.Diff.Stale)

	second, state, err := cache.Get(t.Context(), key)
	require.NoError(err)
	require.NotNil(second)
	assert.Equal(workspaceDiffCacheHit, state)
	assert.Equal(uint64(1), second.Revision)
	assert.Equal(1, prepareCalls)
	assert.Empty(second.Files[0].Patch)
	assert.Empty(second.Files[0].Hunks)
}

func TestWorkspaceDiffCacheProtectedEntriesDoNotConsumeCostBudget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		selected bool
	}{
		{name: "pair-retained snapshot"},
		{name: "selected snapshot", selected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			now := time.Unix(100, 0)
			cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
				now:      func() time.Time { return now },
				maxBytes: 3,
			})
			protectedKey := workspaceDiffTestKey()
			inactiveKey := protectedKey
			inactiveKey.Spec.Base = workspace.WorktreeDiffBasePushed
			newestInactiveKey := inactiveKey
			newestInactiveKey.Spec.HideWhitespace = true
			entry := func(version string, costExempt bool) *workspaceDiffCacheEntry {
				return &workspaceDiffCacheEntry{
					snapshot: &workspaceDiffSnapshot{
						Version:   version,
						SizeBytes: 2,
					},
					validatedAt:   now,
					lastAccess:    now,
					retainedUntil: now.Add(workspaceDiffCachePairRetention),
					costExempt:    costExempt,
				}
			}

			protectedEntry := entry("protected", true)
			if tt.selected {
				protectedEntry.retainedUntil = now
			}

			cache.mu.Lock()
			require.True(cache.storeEntryLocked(protectedKey, protectedEntry, now))
			require.True(cache.storeEntryLocked(inactiveKey, entry("inactive", false), now))
			require.True(cache.storeEntryLocked(newestInactiveKey, entry("newest", false), now))
			if tt.selected {
				cache.selected[protectedKey.WorkspaceID] = 1
				cache.active[protectedKey.WorkspaceID] = map[workspaceDiffLogicalKey]time.Time{
					protectedKey: now,
				}
			}
			cache.maintainLocked(now)
			cache.mu.Unlock()

			require.NotNil(cache.peekEntry(protectedKey))
			require.Nil(cache.peekEntry(inactiveKey))
			require.NotNil(cache.peekEntry(newestInactiveKey))
		})
	}
}

func TestWorkspaceDiffCacheReconnectRetainsActiveScopes(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	now := time.Unix(100, 0)
	var fingerprintCalls atomic.Int64
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
		now: func() time.Time { return now },
		resolve: func(_ context.Context, spec workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			resolved := workspaceDiffTestResolved()
			resolved.DiffSnapshotSpec = spec
			return resolved, true, nil
		},
		fingerprint: func(_ context.Context, resolved workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			fingerprintCalls.Add(1)
			return workspace.DiffFingerprint(resolved.Base), nil
		},
		prepare: func(_ context.Context, resolved workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			return workspaceDiffTestResult(string(resolved.Base) + ".txt"), nil
		},
	})
	headKey := workspaceDiffTestKey()
	pushedKey := headKey
	pushedKey.Spec.Base = workspace.WorktreeDiffBasePushed
	release := cache.Select(headKey.WorkspaceID, nil)
	_, _, err := cache.Get(t.Context(), headKey)
	require.NoError(err)
	_, _, err = cache.Get(t.Context(), pushedKey)
	require.NoError(err)
	release()

	cache.mu.Lock()
	_, retained := cache.active[headKey.WorkspaceID][pushedKey]
	cache.mu.Unlock()
	require.True(retained)
	baseline := fingerprintCalls.Load()
	now = now.Add(workspaceDiffCacheFreshFor)
	release = cache.Select(headKey.WorkspaceID, nil)
	defer release()
	cache.ValidateSelected()

	assert.Eventually(
		func() bool { return fingerprintCalls.Load() >= baseline+2 },
		time.Second,
		time.Millisecond,
	)
}

func TestWorkspaceDiffCacheChangedValidationReplacesStableSnapshot(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	now := time.Unix(100, 0)
	fingerprint := workspace.DiffFingerprint("v1")
	preparePath := "one.txt"
	var changes []uint64
	key := workspaceDiffTestKey()
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
		now: func() time.Time { return now },
		resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			return workspaceDiffTestResolved(), true, nil
		},
		fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			return fingerprint, nil
		},
		prepare: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			return workspaceDiffTestResult(preparePath), nil
		},
		onChanged: func(_ string, revision uint64, _ string) { changes = append(changes, revision) },
	})

	_, _, err := cache.Get(t.Context(), key)
	require.NoError(err)
	fingerprint = "v2"
	preparePath = "two.txt"
	now = now.Add(workspaceDiffCachePairRetention + time.Second)
	require.NoError(cache.validate(t.Context(), key))

	got, state, err := cache.Get(t.Context(), key)
	require.NoError(err)
	assert.Equal(workspaceDiffCacheHit, state)
	assert.Equal("two.txt", got.Diff.Files[0].Path)
	assert.Equal(uint64(2), got.Revision)
	assert.Equal([]uint64{2}, changes)
}

func TestWorkspaceDiffCacheConcurrentMissesCoalesce(t *testing.T) {
	require := require.New(t)
	key := workspaceDiffTestKey()
	started := make(chan struct{})
	release := make(chan struct{})
	waiting := make(chan struct{}, 2)
	prepareCalls := 0
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
		resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			return workspaceDiffTestResolved(), true, nil
		},
		fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			return "v1", nil
		},
		prepare: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			prepareCalls++
			if prepareCalls == 1 {
				close(started)
			}
			<-release
			return workspaceDiffTestResult("one.txt"), nil
		},
		onColdWait: func() { waiting <- struct{}{} },
	})

	type result struct {
		state workspaceDiffCacheState
		err   error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, state, err := cache.Get(t.Context(), key)
			results <- result{state: state, err: err}
		})
	}
	<-started
	<-waiting
	<-waiting
	close(release)
	wg.Wait()
	close(results)

	states := map[workspaceDiffCacheState]int{}
	for result := range results {
		require.NoError(result.err)
		states[result.state]++
	}
	assert.Equal(t, 1, prepareCalls)
	assert.Equal(t, 2, states[workspaceDiffCacheMiss]+states[workspaceDiffCacheCoalesced])
}

func workspaceDiffTestKey() workspaceDiffLogicalKey {
	return workspaceDiffLogicalKey{
		WorkspaceID: "ws-1",
		Spec: workspace.DiffSnapshotSpec{
			WorktreePath: "/tmp/worktree",
			Base:         workspace.WorktreeDiffBaseHead,
		},
	}
}

func TestWorkspaceDiffCacheExpiredActiveEntryCanBeEvicted(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	now := time.Now()
	key := workspaceDiffTestKey()
	entry := &workspaceDiffCacheEntry{
		snapshot:   &workspaceDiffSnapshot{SizeBytes: workspaceDiffCacheMaxBytes + 1},
		lastAccess: now.Add(-workspaceDiffCacheIdleTTL - time.Second),
		costExempt: true,
	}
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{now: func() time.Time { return now }})
	cache.mu.Lock()
	cache.selected[key.WorkspaceID] = 1
	cache.active[key.WorkspaceID] = map[workspaceDiffLogicalKey]time.Time{key: entry.lastAccess}
	cache.storeEntryLocked(key, entry, now)
	cache.mu.Unlock()

	cache.maintain(now)

	assert.Nil(cache.peekEntry(key))
	assert.NotContains(cache.active, key.WorkspaceID)
}

func TestWorkspaceDiffCacheSelectionRetriesFailedPrewarm(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	retry := make(chan time.Time)
	firstAttempt := make(chan struct{})
	ready := make(chan struct{})
	resolveCalls := 0
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
		after: func(time.Duration) <-chan time.Time { return retry },
		resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			return workspaceDiffTestResolved(), true, nil
		},
		fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			return "v1", nil
		},
		prepare: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			return workspaceDiffTestResult("one.txt"), nil
		},
		onReady: func(string, uint64, string) { close(ready) },
	})

	release := cache.Select("ws-1", func(context.Context) (workspaceDiffLogicalKey, error) {
		resolveCalls++
		if resolveCalls == 1 {
			close(firstAttempt)
			return workspaceDiffLogicalKey{}, errors.New("temporarily unavailable")
		}
		return workspaceDiffTestKey(), nil
	})
	t.Cleanup(release)
	<-firstAttempt
	retry <- time.Now()
	<-ready

	assert.NotNil(cache.peekEntry(workspaceDiffTestKey()))
	assert.Equal(2, resolveCalls)
	require.NotNil(cache.selectionCancel["ws-1"])
}

func TestWorkspaceDiffCacheSelectedColdFailureWaitsForPrewarmBackoff(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	retry := make(chan time.Time)
	attempts := make(chan int64, 2)
	backoffStarted := make(chan struct{})
	var prepareCalls atomic.Int64
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
		after: func(time.Duration) <-chan time.Time {
			close(backoffStarted)
			return retry
		},
		resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			return workspaceDiffTestResolved(), true, nil
		},
		fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			return "v1", nil
		},
		prepare: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			call := prepareCalls.Add(1)
			attempts <- call
			if call == 1 {
				return nil, errors.New("temporarily unavailable")
			}
			return workspaceDiffTestResult("one.txt"), nil
		},
	})

	release := cache.Select("ws-1", func(context.Context) (workspaceDiffLogicalKey, error) {
		return workspaceDiffTestKey(), nil
	})
	t.Cleanup(release)
	require.Equal(int64(1), <-attempts)
	<-backoffStarted

	cache.ValidateSelected()
	earlyAttempt := false
	select {
	case attempt := <-attempts:
		earlyAttempt = true
		assert.Fail("periodic validation bypassed cold retry backoff", "attempt=%d", attempt)
	case <-time.After(50 * time.Millisecond):
	}

	retry <- time.Now()
	if !earlyAttempt {
		require.Equal(int64(2), <-attempts)
	}
	assert.Equal(int64(2), prepareCalls.Load())
}

func TestWorkspaceDiffCacheSelectedColdPrewarmSignalsReady(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	var readyWorkspaceID string
	var readyVersion string
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
		resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			return workspaceDiffTestResolved(), true, nil
		},
		fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			return "v1", nil
		},
		prepare: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			return workspaceDiffTestResult("one.txt"), nil
		},
		onReady: func(workspaceID string, _ uint64, version string) {
			readyWorkspaceID = workspaceID
			readyVersion = version
		},
	})
	cache.selected["ws-1"] = 1

	cache.prewarmSelected(t.Context(), func(context.Context) (workspaceDiffLogicalKey, error) {
		return workspaceDiffTestKey(), nil
	})

	assert.Equal("ws-1", readyWorkspaceID)
	assert.NotEmpty(readyVersion)
}

func TestWorkspaceDiffCacheSelectedWarmPrewarmDoesNotSignalReady(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	readyCalls := 0
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
		resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			return workspaceDiffTestResolved(), true, nil
		},
		fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			return "v1", nil
		},
		prepare: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			return workspaceDiffTestResult("one.txt"), nil
		},
		onReady: func(string, uint64, string) { readyCalls++ },
	})
	_, _, err := cache.Get(t.Context(), workspaceDiffTestKey())
	require.NoError(err)
	cache.selected["ws-1"] = 1

	cache.prewarmSelected(t.Context(), func(context.Context) (workspaceDiffLogicalKey, error) {
		return workspaceDiffTestKey(), nil
	})

	assert.Zero(readyCalls)
}

func TestWorkspaceDiffCacheRevalidateWorkspaceChecksCachedSnapshots(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		selected bool
	}{
		{name: "selected", selected: true},
		{name: "unselected", selected: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			assert := assert.New(t)
			key := workspaceDiffTestKey()
			var fingerprint atomic.Value
			fingerprint.Store(workspace.DiffFingerprint("v1"))
			var prepareCalls atomic.Int64
			changed := make(chan string, 1)
			cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
				resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
					return workspaceDiffTestResolved(), true, nil
				},
				fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
					return fingerprint.Load().(workspace.DiffFingerprint), nil
				},
				prepare: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
					prepareCalls.Add(1)
					return workspaceDiffTestResult("one.txt"), nil
				},
				onChanged: func(_ string, _ uint64, version string) { changed <- version },
			})
			_, _, err := cache.Get(t.Context(), key)
			require.NoError(err)
			if tc.selected {
				cache.mu.Lock()
				cache.selected[key.WorkspaceID] = 1
				cache.active[key.WorkspaceID] = map[workspaceDiffLogicalKey]time.Time{key: time.Now()}
				cache.mu.Unlock()
			}

			fingerprint.Store(workspace.DiffFingerprint("v2"))
			cache.RevalidateWorkspace(key.WorkspaceID)

			select {
			case version := <-changed:
				assert.NotEmpty(version)
			case <-time.After(time.Second):
				require.Fail("cached snapshot was not revalidated")
			}
			assert.Equal(int64(2), prepareCalls.Load())
		})
	}
}

func TestWorkspaceDiffCacheValidationTimeoutDoesNotPoisonForegroundWaiter(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	key := workspaceDiffTestKey()
	prepareStarted := make(chan struct{})
	releasePrepare := make(chan struct{})
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
		resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			return workspaceDiffTestResolved(), true, nil
		},
		fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			return "v1", nil
		},
		prepare: func(ctx context.Context, _ workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			close(prepareStarted)
			select {
			case <-releasePrepare:
				return workspaceDiffTestResult("one.txt"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})

	validationCtx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	validationDone := make(chan error, 1)
	go func() { validationDone <- cache.validate(validationCtx, key) }()
	<-prepareStarted
	type getResult struct {
		err error
	}
	getDone := make(chan getResult, 1)
	go func() {
		_, _, err := cache.Get(t.Context(), key)
		getDone <- getResult{err: err}
	}()
	require.ErrorIs(<-validationDone, context.DeadlineExceeded)

	select {
	case result := <-getDone:
		require.Fail("foreground waiter inherited validation cancellation", "error=%v", result.err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releasePrepare)
	require.NoError((<-getDone).err)
}

func TestWorkspaceDiffCacheBackgroundValidationDoesNotRenewAccess(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	now := time.Unix(100, 0)
	key := workspaceDiffTestKey()
	fingerprint := workspace.DiffFingerprint("v1")
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
		now: func() time.Time { return now },
		resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			return workspaceDiffTestResolved(), true, nil
		},
		fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			return fingerprint, nil
		},
		prepare: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			return workspaceDiffTestResult("one.txt"), nil
		},
	})
	_, _, err := cache.Get(t.Context(), key)
	require.NoError(err)
	initialAccess := cache.peekEntry(key).lastAccess
	fingerprint = "v2"
	now = now.Add(time.Minute)

	require.NoError(cache.validate(t.Context(), key))

	assert.Equal(initialAccess, cache.peekEntry(key).lastAccess)
}

func TestWorkspaceDiffCacheSelectedValidationMeetsMaxAge(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	now := time.Unix(100, 0)
	key := workspaceDiffTestKey()
	var fingerprint atomic.Value
	fingerprint.Store(workspace.DiffFingerprint("v1"))
	var fingerprintCalls atomic.Int64
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
		now: func() time.Time { return now },
		resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			return workspaceDiffTestResolved(), true, nil
		},
		fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			fingerprintCalls.Add(1)
			return fingerprint.Load().(workspace.DiffFingerprint), nil
		},
		prepare: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			return workspaceDiffTestResult("one.txt"), nil
		},
	})
	_, _, err := cache.Get(t.Context(), key)
	require.NoError(err)
	initialCalls := fingerprintCalls.Load()
	cache.mu.Lock()
	cache.selected[key.WorkspaceID] = 1
	cache.active[key.WorkspaceID] = map[workspaceDiffLogicalKey]time.Time{key: now}
	cache.mu.Unlock()
	fingerprint.Store(workspace.DiffFingerprint("v2"))

	now = now.Add(workspaceDiffCacheFreshFor - workspaceDiffValidationPoll - time.Nanosecond)
	cache.ValidateSelected()
	assert.Equal(initialCalls, fingerprintCalls.Load())

	now = now.Add(time.Nanosecond)
	cache.ValidateSelected()
	assert.Eventually(func() bool { return fingerprintCalls.Load() > initialCalls }, time.Second, time.Millisecond)
}

func TestWorkspaceDiffCacheRetainsOversizedSnapshotForCoherentPair(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	now := time.Unix(100, 0)
	key := workspaceDiffTestKey()
	prepareCalls := 0
	cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
		now:      func() time.Time { return now },
		maxBytes: 1,
		resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			return workspaceDiffTestResolved(), true, nil
		},
		fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			return "v1", nil
		},
		prepare: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			prepareCalls++
			return workspaceDiffTestResult("one.txt"), nil
		},
	})
	first, _, err := cache.Get(t.Context(), key)
	require.NoError(err)

	second, state, err := cache.Get(t.Context(), key)
	require.NoError(err)
	assert.Equal(workspaceDiffCacheHit, state)
	assert.Equal(first.Version, second.Version)
	assert.Equal(1, prepareCalls)

	now = now.Add(workspaceDiffCachePairRetention + time.Second)
	cache.maintain(now)
	assert.Nil(cache.peekEntry(key))
}

func workspaceDiffTestResolved() workspace.ResolvedDiffSnapshotSpec {
	return workspace.ResolvedDiffSnapshotSpec{
		DiffSnapshotSpec: workspaceDiffTestKey().Spec,
		BaseRef:          "HEAD",
		BaseOID:          "base",
		HeadOID:          "head",
		IncludeUntracked: true,
	}
}

func workspaceDiffTestResult(path string) *gitclone.DiffResult {
	return &gitclone.DiffResult{Files: []gitclone.DiffFile{{
		Path:   path,
		Status: "modified",
		Patch:  "patch",
		Hunks: []gitclone.Hunk{{Lines: []gitclone.Line{{
			Type: "add", Content: "line",
		}}}},
	}}}
}
