//go:build integration

package gitclone

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/tokenauth"
	gitcmd "go.kenn.io/kit/git/cmd"
)

type blockingRouteResolver struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingRouteResolver) SourceForRepo(_, _, _, _ string) tokenauth.Source {
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})
	return nil
}

func (*blockingRouteResolver) FallbackSource(string) tokenauth.Source { return nil }

// setupTestRepo creates a bare "remote" repo with one commit and returns
// both the remote path and the working clone path (for follow-up pushes).
func setupTestRepo(t *testing.T) (remote, work string) {
	t.Helper()
	dir := t.TempDir()
	remote = filepath.Join(dir, "remote.git")
	run(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)

	work = filepath.Join(dir, "work")
	run(t, dir, "git", "clone", remote, work)
	run(t, work, "git", "config", "user.email", "test@test.com")
	run(t, work, "git", "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(work, "hello.go"), []byte("package main\n"), 0o644))
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", "initial")
	run(t, work, "git", "push", "origin", "main")
	return remote, work
}

// commitAndPush creates a new commit on main in the given working clone
// and pushes it to origin. Returns the new commit SHA.
func commitAndPush(t *testing.T, work, file, content, msg string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(work, file), []byte(content), 0o644))
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", msg)
	run(t, work, "git", "push", "origin", "main")
	out, err := gitcmd.New().Output(t.Context(), work, "rev-parse", "HEAD")
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	require.Equal(t, "git", name)
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "command %s %v failed: %s%s", name, args, out, stderr)
}

func TestIntegrationEnsureClone(t *testing.T) {
	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	err := mgr.EnsureClone(ctx, "github", "github.com", "testowner", "testrepo", remote)
	require.NoError(t, err)

	clonePath := filepath.Join(
		clonesDir, "github.com", "testowner", "testrepo.git")
	assert.DirExists(t, clonePath)

	// Second call should be a no-op fetch, not re-clone.
	err = mgr.EnsureClone(ctx, "github", "github.com", "testowner", "testrepo", remote)
	require.NoError(t, err)
}

func TestIntegrationEnsureCloneIsolatesObjectsFromLocalSource(t *testing.T) {
	_, source := setupTestRepo(t)
	commitBytes, err := gitcmd.New().Output(t.Context(), source, "rev-parse", "HEAD")
	require.NoError(t, err)
	commit := strings.TrimSpace(string(commitBytes))
	sourceObject := filepath.Join(source, ".git", "objects", commit[:2], commit[2:])
	require.FileExists(t, sourceObject)

	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)
	require.NoError(t, mgr.EnsureClone(
		t.Context(), "github", "github.com", "testowner", "testrepo", source,
	))

	// A local clone may hardlink loose objects from its source. Corrupt the
	// source object to prove the managed clone owns an independent copy.
	require.NoError(t, os.Chmod(sourceObject, 0o600))
	require.NoError(t, os.WriteFile(sourceObject, []byte("corrupt"), 0o600))

	clonePath := filepath.Join(clonesDir, "github.com", "testowner", "testrepo.git")
	_, err = gitcmd.New().Output(t.Context(), clonePath, "cat-file", "-p", commit)
	require.NoError(t, err)
}

func TestIntegrationEnsureCloneInNamespacePartitionsStorage(t *testing.T) {
	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	err := mgr.EnsureCloneInNamespace(
		ctx, "gitlab", "gitlab", "github.com",
		"testowner", "testrepo", remote,
	)
	require.NoError(t, err)

	namespacedPath := filepath.Join(
		clonesDir, "gitlab", "github.com", "testowner", "testrepo.git",
	)
	assert.DirExists(t, namespacedPath)
	defaultPath := filepath.Join(
		clonesDir, "github.com", "testowner", "testrepo.git",
	)
	assert.NoDirExists(t, defaultPath)
}

func TestIntegrationEnsureClonePartitionsConcurrentRouteReuseByProviderIdentity(t *testing.T) {
	remoteA, workA := setupTestRepo(t)
	remoteB, workB := setupTestRepo(t)
	shaABytes, err := gitcmd.New().Output(t.Context(), workA, "rev-parse", "HEAD")
	require.NoError(t, err)
	shaA := strings.TrimSpace(string(shaABytes))
	shaB := commitAndPush(t, workB, "replacement.go", "package replacement\n", "replacement")

	mgr := New(t.TempDir(), nil)
	ctxA := WithRepositoryIdentity(t.Context(), "provider-repo-a")
	ctxB := WithRepositoryIdentity(t.Context(), "provider-repo-b")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, clone := range []struct {
		ctx    context.Context
		remote string
	}{{ctxA, remoteA}, {ctxB, remoteB}} {
		go func() {
			ready.Done()
			<-start
			errs <- mgr.EnsureClone(
				clone.ctx, "github", "github.com", "acme", "widget", clone.remote,
			)
		}()
	}
	ready.Wait()
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	gotA, err := mgr.RevParse(ctxA, "github", "github.com", "acme", "widget", "HEAD")
	require.NoError(t, err)
	gotB, err := mgr.RevParse(ctxB, "github", "github.com", "acme", "widget", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, shaA, gotA)
	assert.Equal(t, shaB, gotB)
}

func TestIntegrationEnsureCloneValidatedRemovesCloneAfterRouteChange(t *testing.T) {
	remote, _ := setupTestRepo(t)
	mgr := New(t.TempDir(), nil)
	ctx := WithRepositoryIdentity(t.Context(), "provider-repo-a")
	validationErr := errors.New("repository route changed")
	var validations atomic.Int64

	err := mgr.EnsureCloneValidated(
		ctx, "github", "github.com", "acme", "widget", remote,
		func(context.Context) error {
			if validations.Add(1) == 1 {
				return nil
			}
			return validationErr
		},
	)
	require.ErrorIs(t, err, validationErr)
	assert.Equal(t, int64(2), validations.Load())
	clonePath, pathErr := mgr.ClonePathForContext(
		ctx, "github", "github.com", "acme", "widget",
	)
	require.NoError(t, pathErr)
	assert.NoDirExists(t, clonePath)
	assert.NoDirExists(t, clonePath+".removing",
		"invalidation must not leave a renamed-aside clone behind")
}

func TestIntegrationEnsureCloneValidatedRestoresExistingCloneAfterRouteChange(
	t *testing.T,
) {
	remote, work := setupTestRepo(t)
	mgr := New(t.TempDir(), nil)
	ctx := WithRepositoryIdentity(t.Context(), "provider-repo-a")
	require.NoError(t, mgr.EnsureClone(
		ctx, "github", "github.com", "acme", "widget", remote,
	))
	clonePath, err := mgr.ClonePathForContext(
		ctx, "github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)
	originalSHABytes, err := gitcmd.New().Output(
		t.Context(), clonePath, "rev-parse", "refs/remotes/origin/main",
	)
	require.NoError(t, err)
	originalSHA := strings.TrimSpace(string(originalSHABytes))
	worktreePath := filepath.Join(t.TempDir(), "linked-worktree")
	run(t, clonePath, "git", "worktree", "add", "-b", "workspace-test",
		worktreePath, "refs/remotes/origin/main")
	run(t, clonePath, "git", "config", "--unset-all", "remote.origin.fetch")
	run(t, clonePath, "git", "config", "--add", "remote.origin.fetch", legacyBranchRefspec)

	newSHA := commitAndPush(t, work, "replacement.go", "package replacement\n", "replacement")
	require.NotEqual(t, originalSHA, newSHA)
	validationErr := errors.New("repository route changed")
	var validations atomic.Int64
	err = mgr.EnsureCloneValidated(
		ctx, "github", "github.com", "acme", "widget", remote,
		func(context.Context) error {
			if validations.Add(1) == 1 {
				return nil
			}
			return validationErr
		},
	)

	require.ErrorIs(t, err, validationErr)
	assert.DirExists(t, clonePath)
	assert.NoDirExists(t, clonePath+".removing")
	restoredSHABytes, err := gitcmd.New().Output(
		t.Context(), clonePath, "rev-parse", "refs/remotes/origin/main",
	)
	require.NoError(t, err)
	assert.Equal(t, originalSHA, strings.TrimSpace(string(restoredSHABytes)))
	worktreeSHABytes, err := gitcmd.New().Output(t.Context(), worktreePath, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, originalSHA, strings.TrimSpace(string(worktreeSHABytes)))
	refspecBytes, err := gitcmd.New().Output(
		t.Context(), clonePath, "config", "--get-all", "remote.origin.fetch",
	)
	require.NoError(t, err)
	assert.Equal(t, legacyBranchRefspec, strings.TrimSpace(string(refspecBytes)))
}

func TestIntegrationEnsureCloneValidatedRejectsStaleCallerBeforeUnvalidatedFetch(t *testing.T) {
	remote, _ := setupTestRepo(t)
	routes := &blockingRouteResolver{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	mgr := New(t.TempDir(), routes)
	ctx := WithRepositoryIdentity(t.Context(), "provider-repo-a")
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- mgr.EnsureClone(
			ctx, "github", "github.com", "acme", "widget", remote,
		)
	}()
	<-routes.started

	validationErr := errors.New("repository route changed")
	validatedDone := make(chan error, 1)
	go func() {
		validatedDone <- mgr.EnsureCloneValidated(
			ctx, "github", "github.com", "acme", "widget", remote,
			func(context.Context) error { return validationErr },
		)
	}()
	select {
	case err := <-validatedDone:
		require.ErrorIs(t, err, validationErr)
	case <-time.After(5 * time.Second):
		require.Fail(t, "stale caller was not rejected before joining clone work")
	}
	close(routes.release)

	require.NoError(t, <-leaderDone)
	// The stale caller never joins or mutates the unvalidated leader's flight.
	clonePath, err := mgr.ClonePathForContext(
		ctx, "github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)
	assert.DirExists(t, clonePath)
}

func TestIntegrationEnsureCloneValidatedStaleFollowerKeepsValidatedClone(t *testing.T) {
	remote, _ := setupTestRepo(t)
	mgr := New(t.TempDir(), nil)
	ctx := WithRepositoryIdentity(t.Context(), "provider-repo-a")

	leaderValidationStarted := make(chan struct{})
	releaseLeaderValidation := make(chan struct{})
	var releaseOnce sync.Once
	releaseLeader := func() {
		releaseOnce.Do(func() { close(releaseLeaderValidation) })
	}
	t.Cleanup(releaseLeader)
	var leaderValidations atomic.Int64
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- mgr.EnsureCloneValidated(
			ctx, "github", "github.com", "acme", "widget", remote,
			func(context.Context) error {
				if leaderValidations.Add(1) == 2 {
					close(leaderValidationStarted)
					<-releaseLeaderValidation
				}
				return nil
			},
		)
	}()
	select {
	case <-leaderValidationStarted:
	case <-time.After(5 * time.Second):
		require.Fail(t, "leader did not reach post-fetch validation")
	}
	key := ensureCloneKey(
		cloneNamespaceForContext(ctx, "github"), "github.com", "acme", "widget",
	)

	staleErr := errors.New("repository route changed")
	followerDone := make(chan error, 1)
	go func() {
		followerDone <- mgr.EnsureCloneValidated(
			ctx, "github", "github.com", "acme", "widget", remote,
			func(context.Context) error { return staleErr },
		)
	}()
	select {
	case err := <-followerDone:
		require.ErrorIs(t, err, staleErr)
	case <-time.After(5 * time.Second):
		require.Fail(t, "stale follower was not rejected before joining")
	}
	mgr.ensureMu.Lock()
	flight := mgr.ensureFlights[key]
	waiters := 0
	if flight != nil {
		waiters = flight.waiters
	}
	mgr.ensureMu.Unlock()
	assert.Equal(t, 1, waiters, "stale follower must not join the current flight")
	releaseLeader()

	require.NoError(t, <-leaderDone,
		"the caller whose route still owns the clone must keep using it")
	clonePath, err := mgr.ClonePathForContext(
		ctx, "github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)
	assert.DirExists(t, clonePath,
		"a stale follower must not remove a clone the current route still owns")
	require.Eventually(t, func() bool {
		mgr.ensureMu.Lock()
		defer mgr.ensureMu.Unlock()
		return mgr.ensureFlights[key] == nil
	}, 5*time.Second, time.Millisecond)
}

func TestIntegrationEnsureCloneValidatedRejectsStaleCallerWhileCurrentCallerValidates(t *testing.T) {
	remote, _ := setupTestRepo(t)
	mgr := New(t.TempDir(), nil)
	ctx := WithRepositoryIdentity(t.Context(), "provider-repo-a")
	require.NoError(t, mgr.EnsureClone(
		ctx, "github", "github.com", "acme", "widget", remote,
	))
	key := ensureCloneKey(
		cloneNamespaceForContext(ctx, "github"), "github.com", "acme", "widget",
	)
	clonePath, err := mgr.ClonePathForContext(
		ctx, "github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)

	currentValidationStarted := make(chan struct{})
	releaseCurrentValidation := make(chan struct{})
	var releaseOnce sync.Once
	releaseCurrent := func() {
		releaseOnce.Do(func() { close(releaseCurrentValidation) })
	}
	t.Cleanup(releaseCurrent)
	currentDone := make(chan error, 1)
	go func() {
		currentDone <- mgr.EnsureCloneValidated(
			ctx, "github", "github.com", "acme", "widget", remote,
			func(context.Context) error {
				mgr.ensureMu.Lock()
				flight := mgr.ensureFlights[key]
				validatingJoinedCaller := flight != nil && flight.complete
				mgr.ensureMu.Unlock()
				if validatingJoinedCaller {
					close(currentValidationStarted)
					<-releaseCurrentValidation
				}
				return nil
			},
		)
	}()

	select {
	case <-currentValidationStarted:
	case <-time.After(5 * time.Second):
		require.Fail(t, "current caller validation ran after its flight was released")
	}

	staleErr := errors.New("repository route changed")
	var staleValidations atomic.Int64
	staleDone := make(chan error, 1)
	go func() {
		staleDone <- mgr.EnsureCloneValidated(
			ctx, "github", "github.com", "acme", "widget", remote,
			func(context.Context) error {
				staleValidations.Add(1)
				return staleErr
			},
		)
	}()
	select {
	case err := <-staleDone:
		require.ErrorIs(t, err, staleErr)
	case <-time.After(5 * time.Second):
		require.Fail(t, "stale caller was not rejected before joining clone work")
	}
	assert.Equal(t, int64(1), staleValidations.Load())
	assert.DirExists(t, clonePath,
		"a stale caller must not remove the current caller's validated clone")
	select {
	case err := <-currentDone:
		require.Fail(t, "current caller returned before validation was released", "%v", err)
	default:
	}

	releaseCurrent()
	require.NoError(t, <-currentDone)
}

func TestIntegrationEnsureCloneValidatedFollowerRetriesAfterStarterInvalidation(t *testing.T) {
	remote, _ := setupTestRepo(t)
	mgr := New(t.TempDir(), nil)
	ctx := WithRepositoryIdentity(t.Context(), "provider-repo-a")

	followerJoined := make(chan struct{})
	staleErr := errors.New("repository route changed")
	var starterValidations atomic.Int64
	starterDone := make(chan error, 1)
	go func() {
		starterDone <- mgr.EnsureCloneValidated(
			ctx, "github", "github.com", "acme", "widget", remote,
			func(context.Context) error {
				if starterValidations.Add(1) == 2 {
					<-followerJoined
					return staleErr
				}
				return nil
			},
		)
	}()
	key := ensureCloneKey(
		cloneNamespaceForContext(ctx, "github"), "github.com", "acme", "widget",
	)
	require.Eventually(t, func() bool {
		mgr.ensureMu.Lock()
		defer mgr.ensureMu.Unlock()
		return mgr.ensureFlights[key] != nil
	}, 5*time.Second, time.Millisecond)

	var followerValidations atomic.Int64
	followerDone := make(chan error, 1)
	go func() {
		followerDone <- mgr.EnsureCloneValidated(
			ctx, "github", "github.com", "acme", "widget", remote,
			func(context.Context) error {
				followerValidations.Add(1)
				return nil
			},
		)
	}()
	require.Eventually(t, func() bool {
		mgr.ensureMu.Lock()
		defer mgr.ensureMu.Unlock()
		flight := mgr.ensureFlights[key]
		return flight != nil && flight.waiters == 2
	}, 5*time.Second, time.Millisecond)
	close(followerJoined)

	require.ErrorIs(t, <-starterDone, staleErr)
	// A follower whose own route still owns the path must not be rejected
	// because the starter's route lost ownership: it refetches the clone the
	// starter's failed validation removed.
	require.NoError(t, <-followerDone)
	assert.Greater(t, followerValidations.Load(), int64(1),
		"a follower retries and validates the freshly fetched clone")
	clonePath, err := mgr.ClonePathForContext(
		ctx, "github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)
	assert.DirExists(t, clonePath)
}

func TestIntegrationEnsureCloneValidatedCleanupFailureIsNotRetryable(t *testing.T) {
	remote, _ := setupTestRepo(t)
	mgr := New(t.TempDir(), nil)
	ctx := WithRepositoryIdentity(t.Context(), "provider-repo-a")

	cleanupErr := errors.New("remove invalidated clone failed")
	var cleanupCalls atomic.Int64
	mgr.removeCloneAsideForTest = func(string) error {
		cleanupCalls.Add(1)
		return cleanupErr
	}

	followerJoined := make(chan struct{})
	staleErr := errors.New("repository route changed")
	var starterValidations atomic.Int64
	starterDone := make(chan error, 1)
	go func() {
		starterDone <- mgr.EnsureCloneValidated(
			ctx, "github", "github.com", "acme", "widget", remote,
			func(context.Context) error {
				if starterValidations.Add(1) == 2 {
					<-followerJoined
					return staleErr
				}
				return nil
			},
		)
	}()
	key := ensureCloneKey(
		cloneNamespaceForContext(ctx, "github"), "github.com", "acme", "widget",
	)
	require.Eventually(t, func() bool {
		mgr.ensureMu.Lock()
		defer mgr.ensureMu.Unlock()
		return mgr.ensureFlights[key] != nil
	}, 5*time.Second, time.Millisecond)

	var followerValidations atomic.Int64
	followerDone := make(chan error, 1)
	go func() {
		followerDone <- mgr.EnsureCloneValidated(
			ctx, "github", "github.com", "acme", "widget", remote,
			func(context.Context) error {
				followerValidations.Add(1)
				return nil
			},
		)
	}()
	require.Eventually(t, func() bool {
		mgr.ensureMu.Lock()
		defer mgr.ensureMu.Unlock()
		flight := mgr.ensureFlights[key]
		return flight != nil && flight.waiters == 2
	}, 5*time.Second, time.Millisecond)
	close(followerJoined)

	for _, err := range []error{<-starterDone, <-followerDone} {
		require.ErrorIs(t, err, staleErr)
		require.ErrorIs(t, err, cleanupErr)
		var invalidated *cloneValidationError
		require.NotErrorAs(t, err, &invalidated)
	}
	assert.Equal(t, int64(1), cleanupCalls.Load())
	assert.Equal(t, int64(1), followerValidations.Load(),
		"a follower must preflight once but not retry after failed quarantine")
	clonePath, err := mgr.ClonePathForContext(
		ctx, "github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)
	assert.DirExists(t, clonePath,
		"a failed cleanup leaves the possibly contaminated clone in place")
}

func TestWaitEnsureCloneFlightReleasedRejectsMissingFlight(t *testing.T) {
	err := waitEnsureCloneFlightReleased(t.Context(), nil)
	require.EqualError(t, err, "wait for clone flight release: missing flight")
}

// TestEnsureCloneShortCircuitsCanceledContext verifies that a caller
// with an already-canceled context does not start any clone work. The
// pre-check exists so a canceled caller cannot trigger background
// fetches that outlive the request it abandoned.
func TestIntegrationEnsureCloneShortCircuitsCanceledContext(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := mgr.EnsureClone(ctx, "github", "github.com", "testowner", "testrepo", remote)
	require.ErrorIs(err, context.Canceled)

	clonePath := filepath.Join(
		clonesDir, "github.com", "testowner", "testrepo.git")
	_, statErr := os.Stat(clonePath)
	assert.True(os.IsNotExist(statErr),
		"no clone directory should be created when ctx is already canceled")
}

// TestEnsureCloneSweepsPartialClone verifies that a previously aborted
// clone attempt — manifesting as a non-empty directory at the clone
// path that lacks the HEAD file — is cleaned out before the retry runs
// git clone --bare. Without the sweep, git refuses to write into the
// non-empty destination and every retry would fail with "destination
// path already exists and is not an empty directory."
func TestIntegrationEnsureCloneSweepsPartialClone(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	// Simulate a partial clone left behind by a failed attempt: the
	// target directory exists with stray files but no HEAD.
	clonePath := filepath.Join(
		clonesDir, "github.com", "testowner", "testrepo.git")
	require.NoError(os.MkdirAll(clonePath, 0o755))
	require.NoError(os.WriteFile(
		filepath.Join(clonePath, "stray"), []byte("junk"), 0o644))

	require.NoError(mgr.EnsureClone(
		t.Context(), "github", "github.com", "testowner", "testrepo", remote))

	// Verify the partial state was cleaned out and replaced with a
	// real bare clone.
	_, err := os.Stat(filepath.Join(clonePath, "HEAD"))
	require.NoError(err, "real bare clone should exist after sweep")
	_, err = os.Stat(filepath.Join(clonePath, "stray"))
	assert.True(os.IsNotExist(err), "stray file from partial clone should be gone")
}

// TestEnsureCloneInstallsDefaultRefspecs verifies that a fresh clone gets the
// bounded ref families workspace setup needs during background refresh.
func TestIntegrationEnsureCloneInstallsDefaultRefspecs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	require.NoError(mgr.EnsureClone(
		t.Context(), "github", "github.com", "testowner", "testrepo", remote))

	clonePath, err := mgr.ClonePath("github", "github.com", "testowner", "testrepo")
	require.NoError(err)
	refspecs := getFetchRefspecs(t, clonePath)
	assert.Contains(refspecs, remoteTrackingRefspec)
	assert.Contains(refspecs, pullRefspec)
	assert.NotContains(refspecs, gitlabMergeRequestRefspec)
	assert.NotContains(refspecs, legacyBranchRefspec)
}

// TestEnsureCloneFetchesNewBranchCommits is the regression test for the bug
// where a merged PR's merge commit was never fetched into the bare clone
// because git clone --bare sets no default fetch refspec.
func TestIntegrationEnsureCloneFetchesNewBranchCommits(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, work := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	// Push a new commit to the remote after the initial clone.
	newSHA := commitAndPush(t, work, "second.go", "package main\n", "second")

	// Re-run EnsureClone and verify the new commit is now reachable.
	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	got, err := mgr.RevParse(ctx, "github", "github.com", "testowner", "testrepo", newSHA)
	require.NoError(err)
	assert.Equal(newSHA, got)
}

func TestIntegrationEnsureCloneDoesNotRefreshMovedRemoteTags(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, work := setupTestRepo(t)
	initialSHA := gitSHA(t, work, "HEAD")
	run(t, work, "git", "tag", "v1.0.0", initialSHA)
	run(t, work, "git", "push", "origin", "refs/tags/v1.0.0")
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	movedSHA := commitAndPush(t, work, "retag.go", "package main\n", "retag target")
	run(t, work, "git", "tag", "-f", "v1.0.0", movedSHA)
	run(t, work, "git", "push", "--force", "origin", "refs/tags/v1.0.0")

	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	got, err := mgr.RevParse(ctx, "github", "github.com", "testowner", "testrepo", "refs/tags/v1.0.0")
	require.NoError(err)
	assert.Equal(initialSHA, got)
	_, err = mgr.RevParse(ctx, "github", "github.com", "testowner", "testrepo", movedSHA)
	require.NoError(err)
}

func TestIntegrationEnsureCloneDoesNotFetchGitLabMergeRequestHeadsByDefault(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, work := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "gitlab", "gitlab.com", "testowner", "testrepo", remote))

	require.NoError(os.WriteFile(
		filepath.Join(work, "gitlab-mr.go"), []byte("package main\n"), 0o644,
	))
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", "gitlab mr head")
	out, err := gitcmd.New().Output(t.Context(), work, "rev-parse", "HEAD")
	require.NoError(err)
	headSHA := strings.TrimSpace(string(out))
	run(t, work, "git", "push", "origin", "HEAD:refs/merge-requests/17/head")

	require.NoError(mgr.EnsureClone(
		ctx, "gitlab", "gitlab.com", "testowner", "testrepo", remote))

	clonePath, err := mgr.ClonePath("gitlab", "gitlab.com", "testowner", "testrepo")
	require.NoError(err)
	got, err := gitcmd.New().Output(
		t.Context(), clonePath, "rev-parse", "refs/merge-requests/17/head",
	)
	require.Error(err)
	assert.NotContains(strings.TrimSpace(string(got)), headSHA)
}

// TestEnsureCloneMigratesBrokenClone simulates a clone created by the
// previous version of cloneBare (only pull refspec, no remote-tracking
// refspec) and verifies ensureRefspecs migrates it so branch fetches
// work again.
func TestIntegrationEnsureCloneMigratesBrokenClone(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, work := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	// Simulate a pre-fix clone: unset all fetch refspecs, then add only
	// the pull refspec back. This matches the state created by the old
	// cloneBare which never installed a branch refspec.
	clonePath, err := mgr.ClonePath("github", "github.com", "testowner", "testrepo")
	require.NoError(err)
	run(t, clonePath, "git", "config", "--unset-all", "remote.origin.fetch")
	run(t, clonePath, "git", "config", "--add",
		"remote.origin.fetch", "+refs/pull/*/head:refs/pull/*/head")
	refspecs := getFetchRefspecs(t, clonePath)
	require.NotContains(refspecs, remoteTrackingRefspec)
	require.Contains(refspecs, pullRefspec)

	// Push a new commit that would be invisible without the remote-tracking
	// refspec.
	newSHA := commitAndPush(t, work, "third.go", "package main\n", "third")

	// Next EnsureClone should re-add the remote-tracking refspec and fetch
	// the commit.
	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	refspecs = getFetchRefspecs(t, clonePath)
	assert.Contains(refspecs, remoteTrackingRefspec)
	assert.Contains(refspecs, pullRefspec)
	assert.NotContains(refspecs, gitlabMergeRequestRefspec)
	assert.NotContains(refspecs, legacyBranchRefspec)

	got, err := mgr.RevParse(ctx, "github", "github.com", "testowner", "testrepo", newSHA)
	require.NoError(err)
	assert.Equal(newSHA, got)
}

// TestEnsureCloneRemovesLegacyBranchRefspec verifies that legacy clones stop
// fetching origin branches into refs/heads/*, which would collide with a
// workspace checking out the PR branch name locally.
func TestIntegrationEnsureCloneRemovesLegacyBranchRefspec(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	clonePath, err := mgr.ClonePath("github", "github.com", "testowner", "testrepo")
	require.NoError(err)
	run(t, clonePath, "git", "config", "--add",
		"remote.origin.fetch", legacyBranchRefspec)
	run(t, clonePath, "git", "config", "--add",
		"remote.origin.fetch", gitlabMergeRequestRefspec)
	refspecs := getFetchRefspecs(t, clonePath)
	require.Contains(refspecs, legacyBranchRefspec)
	require.Contains(refspecs, gitlabMergeRequestRefspec)

	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	refspecs = getFetchRefspecs(t, clonePath)
	assert.Contains(refspecs, remoteTrackingRefspec)
	assert.Contains(refspecs, pullRefspec)
	assert.NotContains(refspecs, gitlabMergeRequestRefspec)
	assert.NotContains(refspecs, legacyBranchRefspec)
}

// TestEnsureCloneMigratesCloneWithNoRefspec covers a clone that has no
// fetch refspec at all (the state left by a vanilla `git clone --bare`
// before any kenn-forge-specific refspec was added). In that case
// `git config --get-all remote.origin.fetch` exits 1, which must not
// short-circuit ensureRefspecs.
func TestIntegrationEnsureCloneMigratesCloneWithNoRefspec(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, work := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	// Remove every fetch refspec so the key is entirely unset, matching
	// the state of a clone that was created by an older code path which
	// did not install any refspec.
	clonePath, err := mgr.ClonePath("github", "github.com", "testowner", "testrepo")
	require.NoError(err)
	run(t, clonePath, "git", "config", "--unset-all", "remote.origin.fetch")
	refspecs := getFetchRefspecs(t, clonePath)
	require.Empty(refspecs)

	// Push a new commit that would be invisible without the remote-tracking
	// refspec.
	newSHA := commitAndPush(t, work, "fourth.go", "package main\n", "fourth")

	// Next EnsureClone should install both refspecs and fetch the commit.
	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	refspecs = getFetchRefspecs(t, clonePath)
	assert.Contains(refspecs, remoteTrackingRefspec)
	assert.Contains(refspecs, pullRefspec)
	assert.NotContains(refspecs, gitlabMergeRequestRefspec)
	assert.NotContains(refspecs, legacyBranchRefspec)

	got, err := mgr.RevParse(ctx, "github", "github.com", "testowner", "testrepo", newSHA)
	require.NoError(err)
	assert.Equal(newSHA, got)
}

// TestEnsureCloneRestoresOriginHead verifies that EnsureClone leaves the
// remote default-branch symref available as refs/remotes/origin/HEAD.
// Issue workspaces start from origin/HEAD, so older clones that lack that
// symref would otherwise fail to create a worktree.
func TestIntegrationEnsureCloneRestoresOriginHead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	clonePath, err := mgr.ClonePath("github", "github.com", "testowner", "testrepo")
	require.NoError(err)
	run(t, clonePath, "git", "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	_, err = gitcmd.New().Output(t.Context(), clonePath, "symbolic-ref", "refs/remotes/origin/HEAD")
	require.Error(err)

	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	headRef := gitSymbolicRef(
		t, clonePath, "refs/remotes/origin/HEAD",
	)
	assert.Equal("refs/remotes/origin/main", headRef)
}

func TestIntegrationEnsureCloneRepairsStaleOriginHead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	clonePath, err := mgr.ClonePath("github", "github.com", "testowner", "testrepo")
	require.NoError(err)
	run(t, clonePath, "git", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")
	assert.Equal("refs/remotes/origin/master", gitSymbolicRef(
		t, clonePath, "refs/remotes/origin/HEAD",
	))

	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	headRef := gitSymbolicRef(
		t, clonePath, "refs/remotes/origin/HEAD",
	)
	assert.Equal("refs/remotes/origin/main", headRef)
}

func TestIntegrationEnsureCloneToleratesUnresolvedRemoteHead(t *testing.T) {
	require := require.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	run(t, remote, "git", "symbolic-ref", "HEAD", "refs/heads/missing")

	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))
}

// getFetchRefspecs returns the current fetch refspecs configured for the
// "origin" remote in a bare clone. Returns an empty slice when the key
// is unset; `git config --get-all` signals that with exit code 1.
func getFetchRefspecs(t *testing.T, clonePath string) []string {
	t.Helper()
	out, err := gitcmd.New().Output(t.Context(), clonePath,
		"config", "--get-all", "remote.origin.fetch")
	if err != nil {
		if gitcmd.IsExitCode(err, 1) {
			return nil // key unset
		}
		require.NoError(t, err)
	}
	var result []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func gitSymbolicRef(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := gitcmd.New().Output(t.Context(), dir, "symbolic-ref", ref)
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestIntegrationEnsureCloneIgnoresInheritedGitEnv(t *testing.T) {
	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	// Worktree/index vars
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_DIR", "")
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", t.TempDir())
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", t.TempDir())
	// Config injection vars
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_0", "X-Bad: injected")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'http.extraHeader=X-Bad: injected'")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	// Credential/interactive helpers
	t.Setenv("GIT_ASKPASS", "/bin/false")
	t.Setenv("GIT_SSH_COMMAND", "/bin/false")
	t.Setenv("SSH_ASKPASS", "/bin/false")

	err := mgr.EnsureClone(t.Context(), "github", "github.com", "testowner", "testrepo", remote)
	require.NoError(t, err)
}

func TestIntegrationMergeBase(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github", "github.com", "testowner", "testrepo", remote))

	// Get the HEAD SHA.
	clonePath, err := mgr.ClonePath("github", "github.com", "testowner", "testrepo")
	require.NoError(err)
	out, err := gitcmd.New().Output(t.Context(), clonePath, "rev-parse", "HEAD")
	require.NoError(err)
	headSHA := strings.TrimSpace(string(out))

	// Merge base of HEAD with itself is HEAD.
	mb, err := mgr.MergeBase(
		ctx, "github", "github.com", "testowner", "testrepo", headSHA, headSHA)
	require.NoError(err)
	assert.Equal(headSHA, mb)
}

func TestIntegrationRepoBrowserClonePartitionsRouteReuseByProviderIdentity(t *testing.T) {
	remoteA, workA := setupTestRepo(t)
	remoteB, workB := setupTestRepo(t)
	shaABytes, err := gitcmd.New().Output(t.Context(), workA, "rev-parse", "HEAD")
	require.NoError(t, err)
	shaA := strings.TrimSpace(string(shaABytes))
	shaB := commitAndPush(t, workB, "replacement.go", "package replacement\n", "replacement")

	mgr := New(t.TempDir(), nil)
	refA := RepoBrowserRepoRef{
		Provider: "github", Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		ProviderRepoID: "provider-repo-a", RemoteURL: remoteA,
	}
	refB := RepoBrowserRepoRef{
		Provider: "github", Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		ProviderRepoID: "provider-repo-b", RemoteURL: remoteB,
	}
	require.NoError(t, mgr.EnsureRepoBrowserClone(t.Context(), refA))
	require.NoError(t, mgr.EnsureRepoBrowserClone(t.Context(), refB))

	pathA, err := mgr.repoBrowserClonePath(refA)
	require.NoError(t, err)
	pathB, err := mgr.repoBrowserClonePath(refB)
	require.NoError(t, err)
	require.NotEqual(t, pathA, pathB,
		"distinct repository identities sharing a path must not share browser clone storage")
	gotA, err := gitcmd.New().Output(t.Context(), pathA, "rev-parse", "HEAD")
	require.NoError(t, err)
	gotB, err := gitcmd.New().Output(t.Context(), pathB, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, shaA, strings.TrimSpace(string(gotA)))
	assert.Equal(t, shaB, strings.TrimSpace(string(gotB)))
}

func TestAdoptLegacyClonesKeepsMainAndBrowserCachesAvailableOffline(t *testing.T) {
	remote, work := setupTestRepo(t)
	shaBytes, err := gitcmd.New().Output(t.Context(), work, "rev-parse", "HEAD")
	require.NoError(t, err)
	wantSHA := strings.TrimSpace(string(shaBytes))
	mgr := New(t.TempDir(), nil)
	legacyRepo := RepoBrowserRepoRef{
		Provider: "github", Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		RemoteURL: remote,
	}

	require.NoError(t, mgr.EnsureClone(
		t.Context(), "github", "github.com", "acme", "widget", remote,
	))
	require.NoError(t, mgr.EnsureRepoBrowserClone(t.Context(), legacyRepo))
	legacyMainPath, err := mgr.ClonePath(
		"github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)
	legacyBrowserPath, err := mgr.repoBrowserClonePath(legacyRepo)
	require.NoError(t, err)
	require.NoError(t, os.Rename(remote, remote+".offline"))

	stableRepo := legacyRepo
	stableRepo.ProviderRepoID = "provider-repo-1"
	require.NoError(t, mgr.AdoptLegacyClones(t.Context(), stableRepo))

	legacyMainSHA, err := mgr.RevParse(
		t.Context(), "github", "github.com", "acme", "widget", "HEAD",
	)
	require.NoError(t, err)
	assert.Equal(t, wantSHA, legacyMainSHA)
	stableCtx := WithRepositoryIdentity(t.Context(), stableRepo.ProviderRepoID)
	gotMainSHA, err := mgr.RevParse(
		stableCtx, "github", "github.com", "acme", "widget", "HEAD",
	)
	require.NoError(t, err)
	assert.Equal(t, wantSHA, gotMainSHA)
	stableMainPath, err := mgr.ClonePathForContext(
		stableCtx, "github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)
	stableOrigin, err := gitcmd.New().Output(
		t.Context(), stableMainPath, "config", "--get", "remote.origin.url",
	)
	require.NoError(t, err)
	assert.Equal(t, remote, strings.TrimSpace(string(stableOrigin)))
	stableFetch, err := gitcmd.New().Output(
		t.Context(), stableMainPath, "config", "--get-all", "remote.origin.fetch",
	)
	require.NoError(t, err)
	assert.ElementsMatch(t, defaultRefspecs(), strings.Fields(string(stableFetch)))
	_, err = gitcmd.New().Output(
		t.Context(), stableMainPath, "config", "--get", "remote.origin.mirror",
	)
	assert.Error(t, err)
	resolved, err := mgr.ResolveRepoBrowserRef(
		t.Context(), stableRepo,
		RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main"},
	)
	require.NoError(t, err)
	assert.Equal(t, wantSHA, resolved.SHA)
	assert.DirExists(t, legacyMainPath)
	assert.NoDirExists(t, legacyBrowserPath)
}

func TestAdoptLegacyClonesCopiesMainWithoutSharedRefsOrObjects(t *testing.T) {
	remote, _ := setupTestRepo(t)
	mgr := New(t.TempDir(), nil)
	legacyRepo := RepoBrowserRepoRef{
		Provider: "github", Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		RemoteURL: remote,
	}
	require.NoError(t, mgr.EnsureClone(
		t.Context(), "github", "github.com", "acme", "widget", remote,
	))
	legacyMainPath, err := mgr.ClonePath(
		"github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)
	objectContent := []byte("independent legacy object\n")
	objectOut, stderr, err := gitcmd.New().Run(
		t.Context(), legacyMainPath, bytes.NewReader(objectContent),
		"hash-object", "-w", "--stdin",
	)
	require.NoError(t, err, string(stderr))
	objectSHA := strings.TrimSpace(string(objectOut))
	run(t, legacyMainPath, "git", "update-ref", "refs/test/independent", objectSHA)

	stableRepo := legacyRepo
	stableRepo.ProviderRepoID = "provider-repo-1"
	require.NoError(t, mgr.AdoptLegacyClones(t.Context(), stableRepo))
	stableMainPath, err := mgr.ClonePathForContext(
		WithRepositoryIdentity(t.Context(), stableRepo.ProviderRepoID),
		"github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)
	stableRef, err := mgr.RevParse(
		WithRepositoryIdentity(t.Context(), stableRepo.ProviderRepoID),
		"github", "github.com", "acme", "widget", "refs/test/independent",
	)
	require.NoError(t, err)
	assert.Equal(t, objectSHA, stableRef)
	stableObjectPath := filepath.Join(
		stableMainPath, "objects", objectSHA[:2], objectSHA[2:],
	)
	legacyObjectPath := filepath.Join(
		legacyMainPath, "objects", objectSHA[:2], objectSHA[2:],
	)
	require.FileExists(t, stableObjectPath)
	stableObjectInfo, err := os.Stat(stableObjectPath)
	require.NoError(t, err)
	legacyObjectInfo, err := os.Stat(legacyObjectPath)
	require.NoError(t, err)
	assert.False(t, os.SameFile(stableObjectInfo, legacyObjectInfo))
	assert.NoFileExists(t, filepath.Join(stableMainPath, "objects", "info", "alternates"))
	require.NoError(t, os.WriteFile(stableObjectPath, []byte("corrupt"), 0o444))
	run(t, stableMainPath, "git", "update-ref", "-d", "refs/test/independent")

	legacyRef, err := mgr.RevParse(
		t.Context(), "github", "github.com", "acme", "widget",
		"refs/test/independent",
	)
	require.NoError(t, err)
	assert.Equal(t, objectSHA, legacyRef)
	legacyObject, err := gitcmd.New().Output(
		t.Context(), legacyMainPath, "cat-file", "blob", objectSHA,
	)
	require.NoError(t, err)
	assert.Equal(t, objectContent, legacyObject)
}

func TestAdoptLegacyClonesCopyFailureDoesNotPublishPartialStableMain(t *testing.T) {
	remote, _ := setupTestRepo(t)
	mgr := New(t.TempDir(), nil)
	legacyRepo := RepoBrowserRepoRef{
		Provider: "github", Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		RemoteURL: remote,
	}
	require.NoError(t, mgr.EnsureClone(
		t.Context(), "github", "github.com", "acme", "widget", remote,
	))
	legacyMainPath, err := mgr.ClonePath(
		"github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)
	brokenRefPath := filepath.Join(legacyMainPath, "refs", "test", "broken")
	require.NoError(t, os.MkdirAll(filepath.Dir(brokenRefPath), 0o755))
	require.NoError(t, os.WriteFile(
		brokenRefPath, []byte(strings.Repeat("f", 40)+"\n"), 0o644,
	))

	stableRepo := legacyRepo
	stableRepo.ProviderRepoID = "provider-repo-1"
	err = mgr.AdoptLegacyClones(t.Context(), stableRepo)
	require.Error(t, err)
	stableMainPath, pathErr := mgr.ClonePathForContext(
		WithRepositoryIdentity(t.Context(), stableRepo.ProviderRepoID),
		"github", "github.com", "acme", "widget",
	)
	require.NoError(t, pathErr)
	assert.NoDirExists(t, stableMainPath)
	legacySHA, revErr := mgr.RevParse(
		t.Context(), "github", "github.com", "acme", "widget", "HEAD",
	)
	require.NoError(t, revErr)
	assert.NotEmpty(t, legacySHA)
	staging, globErr := filepath.Glob(filepath.Join(
		filepath.Dir(stableMainPath), "."+filepath.Base(stableMainPath)+".adopting-*",
	))
	require.NoError(t, globErr)
	assert.Empty(t, staging)
}

func TestAdoptLegacyClonesRejectsIncompleteStableMain(t *testing.T) {
	remote, _ := setupTestRepo(t)
	mgr := New(t.TempDir(), nil)
	legacyRepo := RepoBrowserRepoRef{
		Provider: "github", Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		RemoteURL: remote,
	}
	require.NoError(t, mgr.EnsureClone(
		t.Context(), "github", "github.com", "acme", "widget", remote,
	))
	stableRepo := legacyRepo
	stableRepo.ProviderRepoID = "provider-repo-1"
	stableMainPath, err := mgr.ClonePathForContext(
		WithRepositoryIdentity(t.Context(), stableRepo.ProviderRepoID),
		"github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(stableMainPath, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(stableMainPath, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644,
	))

	err = mgr.AdoptLegacyClones(t.Context(), stableRepo)
	require.Error(t, err)
	assert.ErrorContains(t, err, "incomplete")
	legacySHA, revErr := mgr.RevParse(
		t.Context(), "github", "github.com", "acme", "widget", "HEAD",
	)
	require.NoError(t, revErr)
	assert.NotEmpty(t, legacySHA)
}

func TestAdoptLegacyClonesHandlesConcurrentStableMainPublication(t *testing.T) {
	remote, _ := setupTestRepo(t)
	cloneBase := t.TempDir()
	seedManager := New(cloneBase, nil)
	require.NoError(t, seedManager.EnsureClone(
		t.Context(), "github", "github.com", "acme", "widget", remote,
	))
	stableRepo := RepoBrowserRepoRef{
		Provider: "github", Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		ProviderRepoID: "provider-repo-1", RemoteURL: remote,
	}

	ctx := t.Context()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		mgr := New(cloneBase, nil)
		go func() {
			ready.Done()
			<-start
			errs <- mgr.AdoptLegacyClones(ctx, stableRepo)
		}()
	}
	ready.Wait()
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	legacySHA, err := seedManager.RevParse(
		t.Context(), "github", "github.com", "acme", "widget", "HEAD",
	)
	require.NoError(t, err)
	stableSHA, err := seedManager.RevParse(
		WithRepositoryIdentity(t.Context(), stableRepo.ProviderRepoID),
		"github", "github.com", "acme", "widget", "HEAD",
	)
	require.NoError(t, err)
	assert.Equal(t, legacySHA, stableSHA)
}

func TestAdoptLegacyClonesRejectsMismatchedStoredOrigin(t *testing.T) {
	remote, _ := setupTestRepo(t)
	mgr := New(t.TempDir(), nil)
	legacyRepo := RepoBrowserRepoRef{
		Provider: "github", Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		RemoteURL: remote,
	}
	require.NoError(t, mgr.EnsureClone(
		t.Context(), "github", "github.com", "acme", "widget", remote,
	))
	require.NoError(t, mgr.EnsureRepoBrowserClone(t.Context(), legacyRepo))
	legacyMainPath, err := mgr.ClonePath(
		"github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)
	run(t, legacyMainPath, "git", "config", "remote.origin.url",
		"https://github.com/other/repository.git")

	stableRepo := legacyRepo
	stableRepo.ProviderRepoID = "provider-repo-1"
	err = mgr.AdoptLegacyClones(t.Context(), stableRepo)
	require.Error(t, err)
	assert.ErrorContains(t, err, "does not match configured repo")
	assert.DirExists(t, legacyMainPath)
	stableMainPath, pathErr := mgr.ClonePathForContext(
		WithRepositoryIdentity(t.Context(), stableRepo.ProviderRepoID),
		"github", "github.com", "acme", "widget",
	)
	require.NoError(t, pathErr)
	assert.NoDirExists(t, stableMainPath)
}
