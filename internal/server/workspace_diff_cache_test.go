package server

import (
	"context"
	"sync"
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
	now = now.Add(workspaceDiffCacheFreshFor + time.Second)
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
