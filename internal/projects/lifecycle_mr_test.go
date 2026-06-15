package projects

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	Require "github.com/stretchr/testify/require"

	gitenv "go.kenn.io/kit/git/env"
	"go.kenn.io/middleman/internal/procutil"
)

func lifecycleGitCommand(dir string, args ...string) *exec.Cmd {
	cmd := procutil.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitenv.StripAll(os.Environ())
	return cmd
}

func identityOfCloneURL(rawURL string) string {
	return CloneURLIdentity(rawURL)
}

// initOriginAndClone builds an "origin" repository with one commit on main
// and a clone of it, returning (originDir, cloneDir). The clone is the
// project checkout merge-request worktrees are created in.
func initOriginAndClone(t *testing.T) (string, string) {
	t.Helper()
	origin := initLifecycleRepo(t)
	clone := filepath.Join(t.TempDir(), "clone")
	lifecycleGit(t, filepath.Dir(origin), "clone", "-q", origin, clone)
	lifecycleGit(t, clone, "config", "user.email", "t@e.st")
	lifecycleGit(t, clone, "config", "user.name", "Tester")
	return origin, clone
}

func worktreeConfig(t *testing.T, dir, key string) string {
	t.Helper()
	cmd := lifecycleGitCommand(dir, "config", "--get", key)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestCreateWorktreeFromMergeRequestSameRepo covers the same-repo scenario:
// the head branch is fetched from origin, the new local branch starts at
// it, and upstream tracking points at origin's head branch.
func TestCreateWorktreeFromMergeRequestSameRepo(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	origin, clone := initOriginAndClone(t)
	lifecycleGit(t, origin, "checkout", "-q", "-b", "feature-x")
	lifecycleGit(t, origin, "commit", "--allow-empty", "-m", "pr work")
	headSHA := lifecycleGit(t, origin, "rev-parse", "feature-x")
	lifecycleGit(t, origin, "checkout", "-q", "main")

	dest := filepath.Join(t.TempDir(), "wt")
	result, err := CreateWorktreeFromMergeRequest(
		context.Background(), MergeRequestWorktreeOptions{
			ProjectRoot:         clone,
			Branch:              "pr-42",
			Path:                dest,
			Number:              42,
			HeadBranch:          "feature-x",
			HeadRepoCloneURL:    origin,
			ProjectRepoIdentity: identityOfCloneURL(origin),
		})
	require.NoError(err)
	assert.Equal(dest, result.Path)
	assert.Equal(headSHA, lifecycleGit(t, dest, "rev-parse", "HEAD"),
		"worktree starts at the merge request head")
	assert.Equal("pr-42",
		lifecycleGit(t, dest, "rev-parse", "--abbrev-ref", "HEAD"))
	assert.Equal("origin", worktreeConfig(t, dest, "branch.pr-42.remote"))
	assert.Equal("refs/heads/feature-x",
		worktreeConfig(t, dest, "branch.pr-42.merge"))
	assert.Equal("upstream", worktreeConfig(t, dest, "push.default"))
}

// TestCreateWorktreeFromMergeRequestPullRefFallback covers the no-fork-URL
// scenario: the merge request head is fetched via the platform pull ref and
// no upstream tracking is configured.
func TestCreateWorktreeFromMergeRequestPullRefFallback(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	origin, clone := initOriginAndClone(t)
	lifecycleGit(t, origin, "checkout", "-q", "-b", "contributor-work")
	lifecycleGit(t, origin, "commit", "--allow-empty", "-m", "pr work")
	headSHA := lifecycleGit(t, origin, "rev-parse", "contributor-work")
	lifecycleGit(t, origin, "update-ref", "refs/pull/7/head", headSHA)
	lifecycleGit(t, origin, "checkout", "-q", "main")

	dest := filepath.Join(t.TempDir(), "wt")
	_, err := CreateWorktreeFromMergeRequest(
		context.Background(), MergeRequestWorktreeOptions{
			ProjectRoot: clone,
			Branch:      "pr-7",
			Path:        dest,
			Number:      7,
			HeadBranch:  "contributor-work",
		})
	require.NoError(err)
	assert.Equal(headSHA, lifecycleGit(t, dest, "rev-parse", "HEAD"),
		"worktree starts at the pull ref head")
	assert.Empty(worktreeConfig(t, dest, "branch.pr-7.remote"),
		"no tracking without a fork clone URL")
}

// TestCreateWorktreeFromMergeRequestFork covers the fork scenario: checkout
// still comes from origin's pull ref, while tracking is configured against
// a dedicated fork remote.
func TestCreateWorktreeFromMergeRequestFork(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	origin, clone := initOriginAndClone(t)

	// The fork: a second clone of origin with the head branch.
	fork := filepath.Join(t.TempDir(), "fork")
	lifecycleGit(t, filepath.Dir(origin), "clone", "-q", origin, fork)
	lifecycleGit(t, fork, "config", "user.email", "t@e.st")
	lifecycleGit(t, fork, "config", "user.name", "Tester")
	lifecycleGit(t, fork, "checkout", "-q", "-b", "fork-work")
	lifecycleGit(t, fork, "commit", "--allow-empty", "-m", "fork pr work")
	headSHA := lifecycleGit(t, fork, "rev-parse", "fork-work")

	// GitHub exposes the fork's head on origin's pull ref.
	lifecycleGit(t, origin, "fetch", "-q", fork,
		"+refs/heads/fork-work:refs/pull/9/head")

	dest := filepath.Join(t.TempDir(), "wt")
	_, err := CreateWorktreeFromMergeRequest(
		context.Background(), MergeRequestWorktreeOptions{
			ProjectRoot:         clone,
			Branch:              "pr-9",
			Path:                dest,
			Number:              9,
			HeadBranch:          "fork-work",
			HeadRepoCloneURL:    fork,
			ProjectRepoIdentity: identityOfCloneURL(origin),
		})
	require.NoError(err)
	assert.Equal(headSHA, lifecycleGit(t, dest, "rev-parse", "HEAD"))
	remote := worktreeConfig(t, dest, "branch.pr-9.remote")
	assert.NotEmpty(remote, "fork tracking remote configured")
	assert.NotEqual("origin", remote)
	assert.Equal("refs/heads/fork-work",
		worktreeConfig(t, dest, "branch.pr-9.merge"))
	remoteURL := lifecycleGit(t, clone, "remote", "get-url", remote)
	assert.Equal(fork, remoteURL)
}

// TestCreateWorktreeFromMergeRequestTrackingFetchFailureIsNonFatal: when
// the fork cannot be fetched, the import still succeeds via the pull ref
// and tracking is silently disabled.
func TestCreateWorktreeFromMergeRequestTrackingFetchFailureIsNonFatal(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	origin, clone := initOriginAndClone(t)
	lifecycleGit(t, origin, "checkout", "-q", "-b", "gone-fork-work")
	lifecycleGit(t, origin, "commit", "--allow-empty", "-m", "pr work")
	headSHA := lifecycleGit(t, origin, "rev-parse", "gone-fork-work")
	lifecycleGit(t, origin, "update-ref", "refs/pull/11/head", headSHA)
	lifecycleGit(t, origin, "checkout", "-q", "main")

	dest := filepath.Join(t.TempDir(), "wt")
	_, err := CreateWorktreeFromMergeRequest(
		context.Background(), MergeRequestWorktreeOptions{
			ProjectRoot:         clone,
			Branch:              "pr-11",
			Path:                dest,
			Number:              11,
			HeadBranch:          "gone-fork-work",
			HeadRepoCloneURL:    filepath.Join(t.TempDir(), "no-such-fork"),
			ProjectRepoIdentity: identityOfCloneURL(origin),
		})
	require.NoError(err)
	assert.Equal(headSHA, lifecycleGit(t, dest, "rev-parse", "HEAD"))
	assert.Empty(worktreeConfig(t, dest, "branch.pr-11.remote"),
		"tracking silently disabled when the fork fetch fails")
}

// TestCreateWorktreeFromMergeRequestHookFailureRollsBack: a failing setup
// hook rolls back the imported worktree and its branch.
func TestCreateWorktreeFromMergeRequestHookFailureRollsBack(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	origin, clone := initOriginAndClone(t)
	lifecycleGit(t, origin, "checkout", "-q", "-b", "feature-x")
	lifecycleGit(t, origin, "commit", "--allow-empty", "-m", "pr work")
	lifecycleGit(t, origin, "checkout", "-q", "main")
	require.NoError(os.WriteFile(
		filepath.Join(clone, "setup.sh"),
		[]byte("#!/bin/sh\nexit 5\n"), 0o755))

	dest := filepath.Join(t.TempDir(), "wt")
	_, err := CreateWorktreeFromMergeRequest(
		context.Background(), MergeRequestWorktreeOptions{
			ProjectRoot:         clone,
			Branch:              "pr-42",
			Path:                dest,
			Number:              42,
			HeadBranch:          "feature-x",
			HeadRepoCloneURL:    origin,
			ProjectRepoIdentity: identityOfCloneURL(origin),
			SetupScript:         "setup.sh",
		})
	var hookErr *HookError
	require.ErrorAs(err, &hookErr)
	assert.Equal(5, hookErr.ExitCode)
	_, statErr := os.Stat(dest)
	assert.True(os.IsNotExist(statErr))
	assert.False(branchExistsInRepo(t, clone, "pr-42"))
}
