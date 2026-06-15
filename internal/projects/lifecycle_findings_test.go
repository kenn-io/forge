package projects

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	Require "github.com/stretchr/testify/require"
)

// TestCreateWorktreeOnDiskReportsBranchCreated pins the BranchCreated flag
// callers thread into RollbackCreatedWorktree: a branch this call created may
// be force-deleted on rollback, a pre-existing one must never be.
func TestCreateWorktreeOnDiskReportsBranchCreated(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	lifecycleGit(t, repo, "branch", "existing")

	attached, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "existing",
		Path:        filepath.Join(t.TempDir(), "wt-existing"),
	})
	require.NoError(err)
	assert.False(attached.BranchCreated,
		"attaching a pre-existing branch must not report it as created")

	created, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "brand-new",
		Path:        filepath.Join(t.TempDir(), "wt-new"),
	})
	require.NoError(err)
	assert.True(created.BranchCreated)
}

// TestRollbackCreatedWorktreePreservesPreexistingBranch covers the
// registry-conflict rollback path: rolling back with BranchCreated=false
// removes the worktree but leaves the branch intact.
func TestRollbackCreatedWorktreePreservesPreexistingBranch(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	lifecycleGit(t, repo, "branch", "keep-me")

	dest := filepath.Join(t.TempDir(), "wt")
	result, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "keep-me",
		Path:        dest,
	})
	require.NoError(err)

	RollbackCreatedWorktree(
		context.Background(), repo, result.Path, result.Branch,
		result.BranchCreated,
	)
	_, statErr := os.Stat(dest)
	assert.True(os.IsNotExist(statErr), "rollback must remove the worktree")
	assert.True(branchExistsInRepo(t, repo, "keep-me"),
		"rollback must not delete a branch it did not create")
}

// TestCreateWorktreeOnDiskRejectsSymlinkedHookEscape verifies hook confinement
// resolves symlinks: a symlink inside the project pointing at a script outside
// it must be rejected, not executed.
func TestCreateWorktreeOnDiskRejectsSymlinkedHookEscape(t *testing.T) {
	require := Require.New(t)
	repo := initLifecycleRepo(t)

	outside := filepath.Join(t.TempDir(), "outside.sh")
	require.NoError(os.WriteFile(
		outside, []byte("#!/bin/sh\nexit 0\n"), 0o755,
	))
	link := filepath.Join(repo, "hook-link.sh")
	require.NoError(os.Symlink(outside, link))

	_, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "feat/escape",
		SetupScript: "hook-link.sh",
	})
	require.ErrorIs(err, ErrHookOutsideProject)
}

// TestCreateWorktreeFromMergeRequestGitLabRef verifies the merge-request head
// resolution is provider-aware: a GitLab project fetches
// refs/merge-requests/<n>/head instead of GitHub's refs/pull/<n>/head.
func TestCreateWorktreeFromMergeRequestGitLabRef(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	origin, clone := initOriginAndClone(t)
	lifecycleGit(t, origin, "checkout", "-q", "-b", "mr-work")
	lifecycleGit(t, origin, "commit", "--allow-empty", "-m", "mr work")
	headSHA := lifecycleGit(t, origin, "rev-parse", "mr-work")
	lifecycleGit(t, origin, "update-ref", "refs/merge-requests/5/head", headSHA)
	lifecycleGit(t, origin, "checkout", "-q", "main")

	dest := filepath.Join(t.TempDir(), "wt")
	_, err := CreateWorktreeFromMergeRequest(
		context.Background(), MergeRequestWorktreeOptions{
			ProjectRoot: clone,
			Branch:      "mr-5",
			Path:        dest,
			Number:      5,
			HeadBranch:  "mr-work",
			Platform:    "gitlab",
		})
	require.NoError(err)
	assert.Equal(headSHA, lifecycleGit(t, dest, "rev-parse", "HEAD"),
		"worktree starts at the GitLab merge-request ref head")
}
