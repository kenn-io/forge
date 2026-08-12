package gitclone

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/tokenauth"
)

type countingRepoBrowserRouteResolver struct {
	calls atomic.Int64
}

func (r *countingRepoBrowserRouteResolver) SourceForRepo(_, _, _, _ string) tokenauth.Source {
	r.calls.Add(1)
	return nil
}

func (*countingRepoBrowserRouteResolver) FallbackSource(string) tokenauth.Source { return nil }

type blockingRepoBrowserRouteResolver struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingRepoBrowserRouteResolver) SourceForRepo(_, _, _, _ string) tokenauth.Source {
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})
	return nil
}

func (*blockingRepoBrowserRouteResolver) FallbackSource(string) tokenauth.Source { return nil }

type callbackRepoBrowserRouteResolver struct {
	calls  atomic.Int64
	onCall func(int64)
}

func (r *callbackRepoBrowserRouteResolver) SourceForRepo(_, _, _, _ string) tokenauth.Source {
	call := r.calls.Add(1)
	if r.onCall != nil {
		r.onCall(call)
	}
	return nil
}

func (*callbackRepoBrowserRouteResolver) FallbackSource(string) tokenauth.Source { return nil }

func TestRepoBrowserListRefsDisambiguatesBranchAndTag(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	mainSHA := gitSHA(t, work, "main")
	commitTestRun(t, work, "git", "checkout", "-b", "release")
	require.NoError(os.WriteFile(filepath.Join(work, "release.txt"), []byte("release\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "release branch")
	branchSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "push", "origin", "HEAD:refs/heads/release")
	commitTestRun(t, work, "git", "checkout", "main")
	commitTestRun(t, work, "git", "tag", "release", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "refs/tags/release")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))

	refs, defaultRef, truncated, err := mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)

	assert.Equal(RepoBrowserRefBranch, defaultRef.Type)
	assert.Equal("main", defaultRef.Name)
	assert.Equal(mainSHA, defaultRef.SHA)
	assert.False(truncated)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "release", SHA: branchSHA})
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "release", SHA: mainSHA})
}

func TestRepoBrowserListRefsCapsLargeRefSets(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	refs := make([]RepoBrowserRef, RepoBrowserRefLimit+1)
	for i := range refs {
		refs[i] = RepoBrowserRef{Type: RepoBrowserRefBranch, Name: fmt.Sprintf("branch-%04d", i), SHA: fmt.Sprintf("%040d", i)}
	}

	capped, truncated := capRepoBrowserRefs(refs)

	assert.True(truncated)
	require.Len(capped, RepoBrowserRefLimit)
	assert.Equal("branch-0000", capped[0].Name)
	assert.Equal(fmt.Sprintf("branch-%04d", RepoBrowserRefLimit-1), capped[len(capped)-1].Name)
}

func TestEnsureRepoBrowserCloneDoesNotFetchTagsForExistingClone(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	mainSHA := gitSHA(t, work, "main")

	commitTestRun(t, work, "git", "tag", "v1.0.0", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "refs/tags/v1.0.0")
	require.NoError(mgr.EnsureRepoBrowserClone(t.Context(), repo))

	refs, _, truncated, err := mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.False(truncated)
	assert.NotContains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})
}

func TestRefreshRepoBrowserClonesRefreshesRegisteredRepos(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	mainSHA := gitSHA(t, work, "main")

	commitTestRun(t, work, "git", "tag", "v1.0.0", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "refs/tags/v1.0.0")
	mgr.RefreshRepoBrowserClones(t.Context())

	refs, _, truncated, err := mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.False(truncated)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})
}

func TestRefreshRepoBrowserClonesUsesSeededExistingClones(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	restarted := New(mgr.baseDir, nil)

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Updated\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "update readme")
	updatedSHA := gitSHA(t, work, "main")
	commitTestRun(t, work, "git", "push", "origin", "main")

	registered, err := restarted.RegisterExistingRepoBrowserClone(t.Context(), repo)
	require.NoError(err)
	require.True(registered)
	restarted.RefreshRepoBrowserClones(t.Context())

	resolved, err := restarted.ResolveRepoBrowserRef(t.Context(), repo, RepoBrowserRef{
		Type: RepoBrowserRefBranch,
		Name: "main",
	})
	require.NoError(err)
	assert.Equal(updatedSHA, resolved.SHA)
}

func TestRefreshRepoBrowserClonesEvictsStaleRegistrationBeforeFetch(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, _ := setupRepoBrowserTestRepo(t)
	routes := &countingRepoBrowserRouteResolver{}
	restarted := New(mgr.baseDir, routes)
	valid := true
	repo.RouteFence = NewRepoBrowserRouteFence(1, 2, 3)
	repo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		return valid, nil
	}
	repo.PublishIfRouteFenceMatches = repoBrowserTestPublishGuard(repo.ValidateRouteFence)

	registered, err := restarted.RegisterExistingRepoBrowserClone(t.Context(), repo)
	require.NoError(err)
	require.True(registered)
	valid = false

	restarted.RefreshRepoBrowserClones(t.Context())

	assert.Zero(routes.calls.Load(), "a stale registration must not resolve credentials or fetch")
	assert.Empty(restarted.repoBrowserReposSnapshot())
}

func TestRefreshRepoBrowserCloneKeepsPublishedCloneWhenStagingIsInvalidated(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	routes := &blockingRepoBrowserRouteResolver{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	restarted := New(mgr.baseDir, routes)
	var valid atomic.Bool
	valid.Store(true)
	repo.RouteFence = NewRepoBrowserRouteFence(1, 2, 3)
	repo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		return valid.Load(), nil
	}
	repo.PublishIfRouteFenceMatches = repoBrowserTestPublishGuard(repo.ValidateRouteFence)
	registered, err := restarted.RegisterExistingRepoBrowserClone(t.Context(), repo)
	require.NoError(err)
	require.True(registered)
	clonePath, err := restarted.repoBrowserClonePath(repo)
	require.NoError(err)
	before := repoBrowserCloneSnapshot(t, clonePath)

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Updated\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "update readme")
	commitTestRun(t, work, "git", "push", "origin", "main")

	done := make(chan error, 1)
	go func() {
		done <- restarted.RefreshRepoBrowserClone(t.Context(), repo)
	}()
	<-routes.started
	valid.Store(false)
	close(routes.release)

	require.ErrorIs(<-done, ErrRepoBrowserRouteFenceChanged)
	assert.Equal(before, repoBrowserCloneSnapshot(t, clonePath))
	blob, err := restarted.ReadRepoBrowserBlob(
		t.Context(), repo,
		RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main"},
		"README.md",
	)
	require.NoError(err)
	assert.Equal("# Widgets\n", blob.Content)
	assert.Empty(restarted.repoBrowserReposSnapshot())
}

func TestEnsureRepoBrowserCloneRemovesCloneInvalidatedDuringInitialFetch(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	_, repo, _ := setupRepoBrowserTestRepo(t)
	routes := &blockingRepoBrowserRouteResolver{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	mgr := New(filepath.Join(t.TempDir(), "clones"), routes)
	var valid atomic.Bool
	valid.Store(true)
	repo.RouteFence = NewRepoBrowserRouteFence(1, 2, 3)
	repo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		return valid.Load(), nil
	}
	repo.PublishIfRouteFenceMatches = repoBrowserTestPublishGuard(repo.ValidateRouteFence)

	done := make(chan error, 1)
	go func() {
		done <- mgr.EnsureRepoBrowserClone(t.Context(), repo)
	}()
	<-routes.started
	valid.Store(false)
	close(routes.release)

	require.ErrorIs(<-done, ErrRepoBrowserRouteFenceChanged)
	clonePath, err := mgr.repoBrowserClonePath(repo)
	require.NoError(err)
	assert.NoDirExists(clonePath)
	assert.Empty(mgr.repoBrowserReposSnapshot())
}

func TestRepoBrowserReadContinuesWhileStaleStageAwaitsValidation(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, staleRepo, currentRepo, _, _ := setupRepoBrowserRouteReuseRefreshTest(t)
	postFetchValidation := make(chan struct{})
	releaseValidation := make(chan struct{})
	var validations atomic.Int64
	var postFetchOnce sync.Once
	staleRepo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		if validations.Add(1) == 1 {
			return true, nil
		}
		postFetchOnce.Do(func() { close(postFetchValidation) })
		<-releaseValidation
		return false, nil
	}
	staleRepo.PublishIfRouteFenceMatches = repoBrowserTestPublishGuard(staleRepo.ValidateRouteFence)

	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- mgr.RefreshRepoBrowserClone(t.Context(), staleRepo)
	}()
	<-postFetchValidation

	readWaiting := make(chan struct{})
	var waitingOnce sync.Once
	mgr.repoBrowserReadWaitingForTest = func(string) {
		waitingOnce.Do(func() { close(readWaiting) })
	}
	type readResult struct {
		blob RepoBrowserBlob
		err  error
	}
	readDone := make(chan readResult, 1)
	go func() {
		blob, err := mgr.ReadRepoBrowserBlob(
			t.Context(), currentRepo,
			RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main"},
			"README.md",
		)
		readDone <- readResult{blob: blob, err: err}
	}()
	select {
	case result := <-readDone:
		require.NoError(result.err)
		assert.Equal("# Widgets\n", result.blob.Content)
	case <-readWaiting:
		require.Fail("read waited for unpublished staging refresh")
	}
	_, err := mgr.ReadRepoBrowserBlob(
		t.Context(), currentRepo,
		RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main"},
		"ONLY_B.md",
	)
	require.ErrorIs(err, ErrNotFound)

	close(releaseValidation)
	require.ErrorIs(<-refreshDone, ErrRepoBrowserRouteFenceChanged)
}

func TestRepoBrowserPublicationWaitsForAdmittedReader(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	repo.RouteFence = NewRepoBrowserRouteFence(1, 2, 3)
	repo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		return true, nil
	}
	publishReady := make(chan struct{})
	releasePublish := make(chan struct{})
	repo.PublishIfRouteFenceMatches = func(
		_ context.Context,
		_ RepoBrowserRouteFence,
		publish func() error,
	) (bool, error) {
		close(publishReady)
		<-releasePublish
		return true, publish()
	}

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Updated\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "update readme")
	commitTestRun(t, work, "git", "push", "origin", "main")

	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- mgr.RefreshRepoBrowserClone(t.Context(), repo)
	}()
	<-publishReady

	readAdmitted := make(chan struct{})
	releaseRead := make(chan struct{})
	var admittedOnce sync.Once
	mgr.repoBrowserAfterReadLockForTest = func(string) {
		admittedOnce.Do(func() {
			close(readAdmitted)
			<-releaseRead
		})
	}
	type readResult struct {
		blob RepoBrowserBlob
		err  error
	}
	readDone := make(chan readResult, 1)
	go func() {
		blob, err := mgr.ReadRepoBrowserBlob(
			t.Context(), repo,
			RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main"},
			"README.md",
		)
		readDone <- readResult{blob: blob, err: err}
	}()
	<-readAdmitted
	close(releasePublish)
	close(releaseRead)

	oldRead := <-readDone
	require.NoError(oldRead.err)
	assert.Equal("# Widgets\n", oldRead.blob.Content)
	require.NoError(<-refreshDone)

	newRead, err := mgr.ReadRepoBrowserBlob(
		t.Context(), repo,
		RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main"},
		"README.md",
	)
	require.NoError(err)
	assert.Equal("# Updated\n", newRead.Content)
}

func TestRepoBrowserRefreshValidFollowerRetriesStaleLeader(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, staleRepo, currentRepo, initialSHA, work := setupRepoBrowserRouteReuseRefreshTest(t)
	postFetchValidation := make(chan struct{})
	releaseValidation := make(chan struct{})
	var staleValidations atomic.Int64
	var postFetchOnce sync.Once
	staleRepo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		if staleValidations.Add(1) == 1 {
			return true, nil
		}
		postFetchOnce.Do(func() { close(postFetchValidation) })
		<-releaseValidation
		return false, nil
	}
	staleRepo.PublishIfRouteFenceMatches = repoBrowserTestPublishGuard(staleRepo.ValidateRouteFence)
	var currentValidations atomic.Int64
	currentRepo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		currentValidations.Add(1)
		return true, nil
	}
	currentRepo.PublishIfRouteFenceMatches = repoBrowserTestPublishGuard(currentRepo.ValidateRouteFence)
	followerJoined := make(chan struct{})
	var joinedOnce sync.Once
	mgr.repoBrowserAfterRefreshJoinForTest = func(fence RepoBrowserRouteFence) {
		if fence == currentRepo.RouteFence {
			joinedOnce.Do(func() { close(followerJoined) })
		}
	}

	staleDone := make(chan error, 1)
	go func() {
		staleDone <- mgr.RefreshRepoBrowserClone(t.Context(), staleRepo)
	}()
	<-postFetchValidation
	commitTestRun(t, work, "git", "push", "--force", "origin", initialSHA+":refs/heads/main")
	currentDone := make(chan error, 1)
	go func() {
		currentDone <- mgr.RefreshRepoBrowserClone(t.Context(), currentRepo)
	}()
	<-followerJoined
	close(releaseValidation)

	require.ErrorIs(<-staleDone, ErrRepoBrowserRouteFenceChanged)
	require.NoError(<-currentDone)
	resolved, err := mgr.ResolveRepoBrowserRef(t.Context(), currentRepo, RepoBrowserRef{
		Type: RepoBrowserRefBranch,
		Name: "main",
	})
	require.NoError(err)
	assert.Equal(initialSHA, resolved.SHA)
	assert.Greater(currentValidations.Load(), int64(1),
		"the valid follower must validate and run its own retry")
}

func TestRepoBrowserRefreshValidFollowerRetriesStaleLeaderFetchFailure(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, staleRepo, currentRepo, initialSHA, work := setupRepoBrowserRouteReuseRefreshTest(t)
	var staleCurrent atomic.Bool
	staleCurrent.Store(true)
	staleRepo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		return staleCurrent.Load(), nil
	}
	staleRepo.PublishIfRouteFenceMatches = repoBrowserTestPublishGuard(staleRepo.ValidateRouteFence)
	var currentValidations atomic.Int64
	currentRepo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		currentValidations.Add(1)
		return true, nil
	}
	currentRepo.PublishIfRouteFenceMatches = repoBrowserTestPublishGuard(currentRepo.ValidateRouteFence)

	fetchStarted := make(chan struct{})
	releaseFetch := make(chan struct{})
	fetchErr := errors.New("stale staging fetch failed")
	mgr.repoBrowserFetchErrorForTest = func(fence RepoBrowserRouteFence) error {
		if fence != staleRepo.RouteFence {
			return nil
		}
		close(fetchStarted)
		<-releaseFetch
		return fetchErr
	}
	followerJoined := make(chan struct{})
	var joinedOnce sync.Once
	mgr.repoBrowserAfterRefreshJoinForTest = func(fence RepoBrowserRouteFence) {
		if fence == currentRepo.RouteFence {
			joinedOnce.Do(func() { close(followerJoined) })
		}
	}

	staleDone := make(chan error, 1)
	go func() {
		staleDone <- mgr.RefreshRepoBrowserClone(t.Context(), staleRepo)
	}()
	<-fetchStarted
	currentDone := make(chan error, 1)
	go func() {
		currentDone <- mgr.RefreshRepoBrowserClone(t.Context(), currentRepo)
	}()
	<-followerJoined
	staleCurrent.Store(false)
	commitTestRun(t, work, "git", "push", "--force", "origin", initialSHA+":refs/heads/main")
	close(releaseFetch)

	staleErr := <-staleDone
	require.ErrorIs(staleErr, ErrRepoBrowserRouteFenceChanged)
	require.NoError(<-currentDone)
	resolved, err := mgr.ResolveRepoBrowserRef(t.Context(), currentRepo, RepoBrowserRef{
		Type: RepoBrowserRefBranch,
		Name: "main",
	})
	require.NoError(err)
	assert.Equal(initialSHA, resolved.SHA)
	assert.Greater(currentValidations.Load(), int64(1),
		"the valid follower must validate and run its own retry")
}

func TestRepoBrowserStagingCleanupFailureIsNotRetryable(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, staleRepo, currentRepo, initialSHA, _ := setupRepoBrowserRouteReuseRefreshTest(t)
	postFetchValidation := make(chan struct{})
	releaseValidation := make(chan struct{})
	var staleValidations atomic.Int64
	var postFetchOnce sync.Once
	staleRepo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		if staleValidations.Add(1) == 1 {
			return true, nil
		}
		postFetchOnce.Do(func() { close(postFetchValidation) })
		<-releaseValidation
		return false, nil
	}
	staleRepo.PublishIfRouteFenceMatches = repoBrowserTestPublishGuard(staleRepo.ValidateRouteFence)
	var currentValidations atomic.Int64
	currentRepo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		currentValidations.Add(1)
		return true, nil
	}
	currentRepo.PublishIfRouteFenceMatches = repoBrowserTestPublishGuard(currentRepo.ValidateRouteFence)
	cleanupErr := errors.New("staging cleanup failed")
	mgr.removeRepoBrowserStagingForTest = func(string) error { return cleanupErr }
	followerJoined := make(chan struct{})
	var joinedOnce sync.Once
	var currentJoins atomic.Int64
	mgr.repoBrowserAfterRefreshJoinForTest = func(fence RepoBrowserRouteFence) {
		if fence == currentRepo.RouteFence {
			currentJoins.Add(1)
			joinedOnce.Do(func() { close(followerJoined) })
		}
	}

	staleDone := make(chan error, 1)
	go func() {
		staleDone <- mgr.RefreshRepoBrowserClone(t.Context(), staleRepo)
	}()
	<-postFetchValidation
	currentDone := make(chan error, 1)
	go func() {
		currentDone <- mgr.RefreshRepoBrowserClone(t.Context(), currentRepo)
	}()
	<-followerJoined
	close(releaseValidation)

	require.ErrorIs(<-staleDone, cleanupErr)
	require.ErrorIs(<-currentDone, cleanupErr)
	assert.Equal(int64(1), currentJoins.Load(),
		"a follower must not retry when the stale clone could not be quarantined")
	assert.Positive(currentValidations.Load())

	blob, err := mgr.ReadRepoBrowserBlob(
		t.Context(), currentRepo,
		RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main"},
		"README.md",
	)
	require.NoError(err)
	assert.Equal("# Widgets\n", blob.Content)
	_, err = mgr.ReadRepoBrowserBlob(
		t.Context(), currentRepo,
		RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main"},
		"ONLY_B.md",
	)
	require.ErrorIs(err, ErrNotFound)

	restarted := New(mgr.baseDir, nil)
	registered, err := restarted.RegisterExistingRepoBrowserClone(t.Context(), currentRepo)
	require.NoError(err)
	require.True(registered)
	resolved, err := restarted.ResolveRepoBrowserRef(t.Context(), currentRepo, RepoBrowserRef{
		Type: RepoBrowserRefBranch,
		Name: "main",
	})
	require.NoError(err)
	assert.Equal(initialSHA, resolved.SHA)
}

func TestRepoBrowserFetchErrorsLeavePublishedCloneUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name        string
		offlineCall int64
	}{
		{name: "branch fetch", offlineCall: 1},
		{name: "tag fetch", offlineCall: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			mgr, repo, work := setupRepoBrowserTestRepo(t)
			repo.ProviderRepoID = "provider-repository-a"
			require.NoError(mgr.EnsureRepoBrowserClone(t.Context(), repo))
			clonePath, err := mgr.repoBrowserClonePath(repo)
			require.NoError(err)
			before := repoBrowserCloneSnapshot(t, clonePath)

			require.NoError(os.WriteFile(filepath.Join(work, "ONLY_B.md"), []byte("only repository B\n"), 0o644))
			commitTestRun(t, work, "git", "add", ".")
			commitTestRun(t, work, "git", "commit", "-m", "replace route with repository B")
			commitTestRun(t, work, "git", "push", "origin", "main")

			remoteOffline := repo.RemoteURL + ".offline"
			var offlineOnce sync.Once
			routes := &callbackRepoBrowserRouteResolver{onCall: func(call int64) {
				if call == tc.offlineCall {
					offlineOnce.Do(func() {
						require.NoError(os.Rename(repo.RemoteURL, remoteOffline))
					})
				}
			}}
			restarted := New(mgr.baseDir, routes)
			repo.RouteFence = NewRepoBrowserRouteFence(1, 2, 1)
			repo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
				return true, nil
			}
			repo.PublishIfRouteFenceMatches = repoBrowserTestPublishGuard(repo.ValidateRouteFence)

			require.Error(restarted.RefreshRepoBrowserClone(t.Context(), repo))
			assert.Equal(before, repoBrowserCloneSnapshot(t, clonePath))
			blob, err := restarted.ReadRepoBrowserBlob(
				t.Context(), repo,
				RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main"},
				"README.md",
			)
			require.NoError(err)
			assert.Equal("# Widgets\n", blob.Content)
			_, err = restarted.ReadRepoBrowserBlob(
				t.Context(), repo,
				RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main"},
				"ONLY_B.md",
			)
			require.ErrorIs(err, ErrNotFound)
		})
	}
}

func TestRepoBrowserPublicationFailureRestoresPublishedClone(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, _, repo, initialSHA, _ := setupRepoBrowserRouteReuseRefreshTest(t)
	repo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		return true, nil
	}
	repo.PublishIfRouteFenceMatches = func(
		_ context.Context,
		_ RepoBrowserRouteFence,
		publish func() error,
	) (bool, error) {
		return true, publish()
	}
	clonePath, err := mgr.repoBrowserClonePath(repo)
	require.NoError(err)
	before := repoBrowserCloneSnapshot(t, clonePath)
	publishErr := errors.New("publish staging failed")
	mgr.publishRepoBrowserStagingForTest = func(string, string) error { return publishErr }

	require.ErrorIs(mgr.RefreshRepoBrowserClone(t.Context(), repo), publishErr)
	assert.Equal(before, repoBrowserCloneSnapshot(t, clonePath))
	resolved, err := mgr.ResolveRepoBrowserRef(t.Context(), repo, RepoBrowserRef{
		Type: RepoBrowserRefBranch,
		Name: "main",
	})
	require.NoError(err)
	assert.Equal(initialSHA, resolved.SHA)
}

func TestRepoBrowserGuardedPublicationRefusesRouteChangeAfterValidation(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, _, repo, initialSHA, _ := setupRepoBrowserRouteReuseRefreshTest(t)
	var validations atomic.Int64
	var routeCurrent atomic.Bool
	routeCurrent.Store(true)
	repo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		validations.Add(1)
		return routeCurrent.Load(), nil
	}
	guardCalled := make(chan struct{})
	repo.PublishIfRouteFenceMatches = func(
		_ context.Context,
		_ RepoBrowserRouteFence,
		_ func() error,
	) (bool, error) {
		routeCurrent.Store(false)
		close(guardCalled)
		return false, nil
	}
	clonePath, err := mgr.repoBrowserClonePath(repo)
	require.NoError(err)
	before := repoBrowserCloneSnapshot(t, clonePath)

	require.ErrorIs(mgr.RefreshRepoBrowserClone(t.Context(), repo), ErrRepoBrowserRouteFenceChanged)
	<-guardCalled
	assert.GreaterOrEqual(validations.Load(), int64(2))
	assert.Equal(before, repoBrowserCloneSnapshot(t, clonePath))
	resolved, err := mgr.ResolveRepoBrowserRef(t.Context(), repo, RepoBrowserRef{
		Type: RepoBrowserRefBranch,
		Name: "main",
	})
	require.NoError(err)
	assert.Equal(initialSHA, resolved.SHA)
}

func TestRepoBrowserBarrierReadAdmissionHonorsContextCancellation(t *testing.T) {
	barrier := newRepoBrowserBarrier()
	require.NoError(t, barrier.lockWrite(t.Context()))
	t.Cleanup(barrier.unlockWrite)
	ctx, cancel := context.WithCancel(t.Context())
	waiting := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		err := barrier.lockRead(ctx, func() { close(waiting) })
		if err == nil {
			barrier.unlockRead()
		}
		done <- err
	}()
	<-waiting
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "repo browser read did not abandon blocked barrier admission")
	}
}

func TestEnsureRepoBrowserCloneDoesNotRefreshExistingClone(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	initial := repoBrowserMainRef(t, mgr, repo)

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Updated\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "update readme")
	updatedSHA := gitSHA(t, work, "main")
	commitTestRun(t, work, "git", "push", "origin", "main")

	require.NoError(mgr.EnsureRepoBrowserClone(t.Context(), repo))
	stale := repoBrowserMainRef(t, mgr, repo)
	assert.Equal(initial.SHA, stale.SHA)

	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	refreshed := repoBrowserMainRef(t, mgr, repo)
	assert.Equal(updatedSHA, refreshed.SHA)
}

func TestEnsureRepoBrowserCloneReleasesReadBarrierBeforeFinalFenceValidation(t *testing.T) {
	require := require.New(t)
	mgr, repo, _ := setupRepoBrowserTestRepo(t)
	repo.RouteFence = NewRepoBrowserRouteFence(1, 2, 3)
	barrierHeld := errors.New("repo browser read barrier held during final fence validation")
	var validations atomic.Int64
	repo.ValidateRouteFence = func(context.Context, RepoBrowserRouteFence) (bool, error) {
		if validations.Add(1) == 1 {
			return true, nil
		}
		barrier := mgr.repoBrowserCloneBarrier(repo)
		if !barrier.semaphore.TryAcquire(repoBrowserBarrierCapacity) {
			return false, barrierHeld
		}
		barrier.semaphore.Release(repoBrowserBarrierCapacity)
		return true, nil
	}
	repo.PublishIfRouteFenceMatches = repoBrowserTestPublishGuard(repo.ValidateRouteFence)

	require.NoError(mgr.EnsureRepoBrowserClone(t.Context(), repo))
	require.Equal(int64(2), validations.Load())
}

func TestRepoBrowserScheduledRefreshContextStaysCancelable(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	_, repo, _ := setupRepoBrowserTestRepo(t)
	mgr := New(filepath.Join(t.TempDir(), "clones-canceled"), nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := mgr.RefreshRepoBrowserClone(ctx, repo)
	require.ErrorIs(err, context.Canceled)
	repos := mgr.repoBrowserReposSnapshot()
	assert.Empty(repos)
}

func TestRepoBrowserRequestRefreshWorkDetachesCallerCancellation(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	requestWork := repoBrowserRefreshWorkParent(ctx, repoBrowserRefreshDetachCaller)
	scheduledWork := repoBrowserRefreshWorkParent(ctx, repoBrowserRefreshRespectCaller)

	cancel()

	require.NoError(requestWork.Err())
	assert.ErrorIs(scheduledWork.Err(), context.Canceled)
}

func TestRepoBrowserRefreshFetchesTagsWithoutPruning(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	mainSHA := gitSHA(t, work, "main")

	commitTestRun(t, work, "git", "tag", "v1.0.0", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "refs/tags/v1.0.0")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	refs, _, truncated, err := mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.False(truncated)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})

	commitTestRun(t, work, "git", "tag", "-d", "v1.0.0")
	commitTestRun(t, work, "git", "push", "origin", ":refs/tags/v1.0.0")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))

	refs, _, truncated, err = mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.False(truncated)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})

	commitTestRun(t, work, "git", "tag", "v1.0.1", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "refs/tags/v1.0.1")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))

	refs, _, truncated, err = mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.False(truncated)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.1", SHA: mainSHA})

	require.NoError(os.WriteFile(filepath.Join(work, "retag.txt"), []byte("retag\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "retag target")
	movedSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "tag", "-f", "v1.0.1", movedSHA)
	commitTestRun(t, work, "git", "push", "origin", "main")
	commitTestRun(t, work, "git", "push", "--force", "origin", "refs/tags/v1.0.1")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))

	refs, _, truncated, err = mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.False(truncated)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.1", SHA: movedSHA})
}

func TestRepoBrowserRefNamesResolveAsExactRefs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	initialSHA := gitSHA(t, work, "main")
	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("updated\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "move main")
	mainSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "tag", "release", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "main", "refs/tags/release")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))

	ref, err := mgr.ResolveRepoBrowserRef(t.Context(), repo, RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main"})
	require.NoError(err)
	assert.Equal(mainSHA, ref.SHA)

	_, err = mgr.ResolveRepoBrowserRef(t.Context(), repo, RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main~1"})
	require.ErrorIs(err, ErrNotFound)

	_, err = mgr.ResolveRepoBrowserRef(t.Context(), repo, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "release^{}"})
	require.ErrorIs(err, ErrNotFound)

	_, err = mgr.ResolveRepoBrowserRef(t.Context(), repo, RepoBrowserRef{Type: RepoBrowserRefCommit, SHA: initialSHA})
	assert.NoError(err)
}

func TestRepoBrowserListTreeReaderStopsAtEntryLimit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var input strings.Builder
	for i := range RepoBrowserTreeEntryLimit + 1 {
		_, err := fmt.Fprintf(&input, "100644 blob %040d %d\tfile-%05d.txt\x00", i, i, i)
		require.NoError(err)
	}
	canceled := false

	entries, truncated, err := readRepoBrowserTreeEntries(strings.NewReader(input.String()), func() {
		canceled = true
	})

	require.NoError(err)
	assert.True(truncated)
	assert.True(canceled)
	assert.Len(entries, RepoBrowserTreeEntryLimit)
}

func TestRepoBrowserListTreeIncludesTrackedDotfiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	ref := repoBrowserMainRef(t, mgr, repo)

	entries, truncated, err := mgr.ListRepoBrowserTree(t.Context(), repo, ref)
	require.NoError(err)

	var paths []string
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	assert.False(truncated)
	assert.Contains(paths, ".github/workflows/ci.yml")
	assert.Contains(paths, ".gitignore")
	assert.Contains(paths, "README.md")
	assert.Contains(paths, "src/main.go")
	assert.NotContains(paths, ".git")
	assert.Equal(gitSHA(t, work, "main"), ref.SHA)
}

func TestRepoBrowserReadBlobRejectsTraversalAndLargeFiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	largePath := filepath.Join(work, "large.txt")
	require.NoError(os.WriteFile(largePath, []byte(string(make([]byte, RepoBrowserBlobSizeLimit+1))), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "large file")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	_, err := mgr.ReadRepoBrowserBlob(t.Context(), repo, ref, "../secret.txt")
	require.ErrorIs(err, ErrUnsafePath)

	blob, err := mgr.ReadRepoBrowserBlob(t.Context(), repo, ref, "large.txt")
	require.NoError(err)
	assert.True(blob.TooLarge)
	assert.Equal(int64(RepoBrowserBlobSizeLimit+1), blob.Size)
	assert.Empty(blob.Content)
}

func TestRepoBrowserLastChangedBatchCapsPaths(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, _ := setupRepoBrowserTestRepo(t)
	ref := repoBrowserMainRef(t, mgr, repo)
	paths := make([]string, RepoBrowserLastChangedBatchMax+1)
	for i := range paths {
		paths[i] = "README.md"
	}

	_, err := mgr.RepoBrowserLastChanged(t.Context(), repo, ref, paths)

	require.Error(err)
	assert.ErrorIs(err, ErrTooManyPaths)
}

func TestRepoBrowserLastChangedFallsBackPastBatchLogLimit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	readmeSHA := gitSHA(t, work, "HEAD")

	gitfixture.AppendFileCommits(t, work, "main", "churn.txt", RepoBrowserLastChangedLogLimit+1)
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	changed, err := mgr.RepoBrowserLastChanged(t.Context(), repo, ref, []string{"README.md", "churn.txt"})

	require.NoError(err)
	assert.Equal(readmeSHA, changed["README.md"].SHA)
	assert.Equal(gitSHA(t, work, "HEAD"), changed["churn.txt"].SHA)
}

func TestRepoBrowserLastChangedHandlesCommitPrefixedPathsAndUTCTimes(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	pathName := "commit:notes.md"
	require.NoError(os.WriteFile(filepath.Join(work, pathName), []byte("notes\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "--date=2026-06-01T12:34:56-07:00", "-m", "commit-prefixed path")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	changed, err := mgr.RepoBrowserLastChanged(t.Context(), repo, ref, []string{pathName})
	require.NoError(err)

	require.Contains(changed, pathName)
	assert.Equal(gitSHA(t, work, "HEAD"), changed[pathName].SHA)
	assert.Equal(time.Date(2026, 6, 1, 19, 34, 56, 0, time.UTC), changed[pathName].AuthoredAt)
}

func TestRepoBrowserLastChangedTreatsCommitFormatShapedPathAsPath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	commitShapedPath := strings.Join([]string{
		strings.Repeat("a", 40),
		"Fake Author",
		"fake@example.com",
		"2026-06-01T12:34:56Z",
		"fake subject",
	}, "\x1f")
	secondPath := "zz-after.md"
	require.NoError(os.WriteFile(filepath.Join(work, commitShapedPath), []byte("literal\n"), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, secondPath), []byte("after\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "commit-shaped path")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)
	wantSHA := gitSHA(t, work, "HEAD")

	changed, err := mgr.RepoBrowserLastChanged(t.Context(), repo, ref, []string{commitShapedPath, secondPath})
	require.NoError(err)

	require.Contains(changed, commitShapedPath)
	require.Contains(changed, secondPath)
	assert.Equal(wantSHA, changed[commitShapedPath].SHA)
	assert.Equal(wantSHA, changed[secondPath].SHA)
	assert.Equal("commit-shaped path", changed[secondPath].Subject)
}

func TestRepoBrowserFileHistoryIsBoundedAtSelectedSHA(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("two\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "readme two")
	selectedSHA := gitSHA(t, work, "HEAD")
	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("three\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "readme three")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))

	history, err := mgr.RepoBrowserFileHistory(
		t.Context(),
		repo,
		RepoBrowserRef{Type: RepoBrowserRefCommit, SHA: selectedSHA},
		"README.md",
	)
	require.NoError(err)
	require.NotEmpty(history)
	assert.Equal(selectedSHA, history[0].SHA)
	assert.Equal("readme two", history[0].Subject)
	for _, commit := range history {
		assert.NotEqual("readme three", commit.Subject)
	}
}

func TestRepoBrowserFileHistoryRequiresSelectedTreePath(t *testing.T) {
	require := require.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	require.NoError(os.WriteFile(filepath.Join(work, "later.md"), []byte("later\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "later file")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := RepoBrowserRef{Type: RepoBrowserRefCommit, SHA: gitSHA(t, work, "HEAD~1")}

	_, err := mgr.RepoBrowserFileHistory(t.Context(), repo, ref, "later.md")
	require.ErrorIs(err, ErrNotFound)
}

func TestRepoBrowserCommitDetailRequiresSelectedFileHistory(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	require.NoError(os.WriteFile(filepath.Join(work, "other.txt"), []byte("other\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "other file", "-m", "Explain the file change.\n\nKeep the body visible.")
	otherSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	_, err := mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, "README.md", otherSHA)
	require.ErrorIs(err, ErrCommitOutOfScope)

	commit, err := mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, "other.txt", otherSHA)
	require.NoError(err)
	assert.Equal(otherSHA, commit.SHA)
	assert.Equal("other file", commit.Subject)
	assert.Equal("Explain the file change.\n\nKeep the body visible.", commit.Body)
}

func TestRepoBrowserCommitDetailRejectsUnknownFullSHA(t *testing.T) {
	require := require.New(t)
	mgr, repo, _ := setupRepoBrowserTestRepo(t)
	ref := repoBrowserMainRef(t, mgr, repo)

	_, err := mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, "README.md", strings.Repeat("a", 40))

	require.ErrorIs(err, ErrNotFound)
}

func TestRepoBrowserCommitDetailAcceptsOlderFileHistory(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	readmeSHA := gitSHA(t, work, "HEAD")

	for i := range RepoBrowserHistoryLimit + 1 {
		require.NoError(os.WriteFile(
			filepath.Join(work, fmt.Sprintf("later-%02d.txt", i)),
			[]byte("later\n"),
			0o644,
		))
		commitTestRun(t, work, "git", "add", ".")
		commitTestRun(t, work, "git", "commit", "-m", fmt.Sprintf("later %02d", i))
	}
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	commit, err := mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, "README.md", readmeSHA)
	require.NoError(err)
	assert.Equal(readmeSHA, commit.SHA)
	assert.Equal("initial", commit.Subject)
}

func TestRepoBrowserCommitDetailAcceptsMergeCommitTouchingPath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	commitTestRun(t, work, "git", "checkout", "-b", "feature")
	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Widgets\n\nFeature\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "feature readme")
	commitTestRun(t, work, "git", "checkout", "main")
	require.NoError(os.WriteFile(filepath.Join(work, "main.txt"), []byte("main\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "main work")
	commitTestRun(t, work, "git", "merge", "--no-ff", "feature", "-m", "merge feature")
	mergeSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	commit, err := mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, "README.md", mergeSHA)
	require.NoError(err)
	assert.Equal(mergeSHA, commit.SHA)
	assert.Equal("merge feature", commit.Subject)
}

func TestRepoBrowserHistoryTreatsPathspecMagicAsLiteral(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	magicPath := ":(glob)*.md"
	require.NoError(os.WriteFile(filepath.Join(work, magicPath), []byte("literal\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "literal pathspec file")
	literalSHA := gitSHA(t, work, "HEAD")

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Widgets\n\nUpdated\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "readme update")
	readmeSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	blob, err := mgr.ReadRepoBrowserBlob(t.Context(), repo, ref, magicPath)
	require.NoError(err)
	assert.Equal("literal\n", blob.Content)

	changed, err := mgr.RepoBrowserLastChanged(t.Context(), repo, ref, []string{magicPath})
	require.NoError(err)
	assert.Equal(literalSHA, changed[magicPath].SHA)

	history, err := mgr.RepoBrowserFileHistory(t.Context(), repo, ref, magicPath)
	require.NoError(err)
	require.NotEmpty(history)
	assert.Equal(literalSHA, history[0].SHA)
	for _, commit := range history {
		assert.NotEqual(readmeSHA, commit.SHA)
	}

	_, err = mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, magicPath, readmeSHA)
	require.ErrorIs(err, ErrCommitOutOfScope)
}

func TestRepoBrowserMarkdownAssetRejectsUnsafeAndOversizedPaths(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	require.NoError(os.WriteFile(filepath.Join(work, "image.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "page.html"), []byte(`<script>alert(1)</script>`), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "script.js"), []byte(`alert(1)`), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "image.png"), []byte{0x89, 0x50, 0x4e, 0x47}, 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "huge.png"), []byte(string(make([]byte, RepoBrowserBlobSizeLimit+1))), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "assets")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	_, err := mgr.ReadRepoBrowserAsset(t.Context(), repo, ref, "/etc/passwd")
	require.ErrorIs(err, ErrUnsafePath)

	for _, path := range []string{"image.svg", "page.html", "script.js"} {
		_, err = mgr.ReadRepoBrowserAsset(t.Context(), repo, ref, path)
		require.ErrorIs(err, ErrUnsupportedAsset, path)
	}

	asset, err := mgr.ReadRepoBrowserAsset(t.Context(), repo, ref, "image.png")
	require.NoError(err)
	assert.Equal("image/png", asset.MediaType)

	_, err = mgr.ReadRepoBrowserAsset(t.Context(), repo, ref, "huge.png")
	assert.True(errors.Is(err, ErrTooLarge) || errors.Is(err, ErrTooLargeAsset))
}

func setupRepoBrowserTestRepo(t *testing.T) (*Manager, RepoBrowserRepoRef, string) {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", remote, work)
	commitTestRun(t, work, "git", "config", "user.email", "alice@example.com")
	commitTestRun(t, work, "git", "config", "user.name", "Alice")
	require.NoError(t, os.MkdirAll(filepath.Join(work, ".github", "workflows"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(work, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, ".github", "workflows", "ci.yml"), []byte("name: ci\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, ".gitignore"), []byte("tmp\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("# Widgets\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, "src", "main.go"), []byte("package main\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "initial")
	commitTestRun(t, work, "git", "push", "origin", "main")

	mgr := New(filepath.Join(dir, "clones"), nil)
	repo := RepoBrowserRepoRef{
		Provider:  "github",
		Host:      "github.com",
		Owner:     "acme",
		Name:      "widgets",
		RepoPath:  "acme/widgets",
		RemoteURL: remote,
	}
	require.NoError(t, mgr.EnsureRepoBrowserClone(t.Context(), repo))
	return mgr, repo, work
}

func setupRepoBrowserRouteReuseRefreshTest(
	t *testing.T,
) (*Manager, RepoBrowserRepoRef, RepoBrowserRepoRef, string, string) {
	t.Helper()
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	repo.ProviderRepoID = "provider-repository-a"
	require.NoError(t, mgr.EnsureRepoBrowserClone(t.Context(), repo))
	initialSHA := gitSHA(t, work, "main")
	require.NoError(t, os.WriteFile(
		filepath.Join(work, "README.md"), []byte("repository B\n"), 0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(work, "ONLY_B.md"), []byte("only repository B\n"), 0o644,
	))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "replace route with repository B")
	commitTestRun(t, work, "git", "push", "origin", "main")

	staleRepo := repo
	staleRepo.RouteFence = NewRepoBrowserRouteFence(1, 2, 1)
	currentRepo := staleRepo
	currentRepo.RouteFence = NewRepoBrowserRouteFence(1, 2, 3)
	return mgr, staleRepo, currentRepo, initialSHA, work
}

func repoBrowserMainRef(t *testing.T, mgr *Manager, repo RepoBrowserRepoRef) RepoBrowserRef {
	t.Helper()
	_, ref, err := mgr.resolveRepoBrowserDefaultBranch(t.Context(), repo, "main")
	require.NoError(t, err)
	return RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main", SHA: ref}
}

func repoBrowserCloneSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			snapshot[rel] = "dir:" + info.Mode().String()
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot[rel] = "link:" + target
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot[rel] = fmt.Sprintf("file:%s:%x", info.Mode(), sha256.Sum256(data))
		}
		return nil
	}))
	return snapshot
}

func repoBrowserTestPublishGuard(
	validate RepoBrowserRouteFenceValidator,
) RepoBrowserRouteFencePublishGuard {
	return func(
		ctx context.Context,
		fence RepoBrowserRouteFence,
		publish func() error,
	) (bool, error) {
		matches, err := validate(ctx, fence)
		if err != nil || !matches {
			return false, err
		}
		return true, publish()
	}
}
