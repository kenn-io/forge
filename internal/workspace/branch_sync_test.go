package workspace

import (
	"os"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPushWorktreeBranchPushesAheadCommitsAndRunsHooks(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)
	marker := filepath.Join(filepath.Dir(work), "pre-push-ran")
	hook := filepath.Join(work, ".git", "hooks", "pre-push")
	require.NoError(os.WriteFile(
		hook,
		[]byte("#!/bin/sh\nprintf ran > "+marker+"\n"),
		0o755,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("ahead\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "ahead")

	require.NoError(PushWorktreeBranch(t.Context(), work))

	div, ok, err := WorktreeDivergence(t.Context(), work)
	require.NoError(err)
	require.True(ok)
	assert.Equal(Divergence{}, div)
	assert.FileExists(marker)
}

func TestPullWorktreeBranchFastForwardsBehindBranch(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)
	other := filepath.Join(filepath.Dir(work), "other")
	remote := filepath.Join(filepath.Dir(work), "remote.git")
	runWorkspaceTestGit(t, filepath.Dir(work), "clone", remote, other)
	runWorkspaceTestGit(t, other, "config", "user.email", "o@test.com")
	runWorkspaceTestGit(t, other, "config", "user.name", "Other")
	runWorkspaceTestGit(t, other, "checkout", "-b", "feature", "origin/feature")
	require.NoError(os.WriteFile(
		filepath.Join(other, "f.txt"), []byte("remote-extra\n"), 0o644,
	))
	runWorkspaceTestGit(t, other, "add", ".")
	runWorkspaceTestGit(t, other, "commit", "-m", "remote extra")
	runWorkspaceTestGit(t, other, "push", "origin", "feature")

	require.NoError(PullWorktreeBranch(t.Context(), work))

	contents, err := os.ReadFile(filepath.Join(work, "f.txt"))
	require.NoError(err)
	assert.Equal("remote-extra\n", string(contents))
	div, ok, err := WorktreeDivergence(t.Context(), work)
	require.NoError(err)
	require.True(ok)
	assert.Equal(Divergence{}, div)
}

func TestPushWorktreeBranchRejectsDivergedBranch(t *testing.T) {
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("local\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "local")

	other := filepath.Join(filepath.Dir(work), "other")
	remote := filepath.Join(filepath.Dir(work), "remote.git")
	runWorkspaceTestGit(t, filepath.Dir(work), "clone", remote, other)
	runWorkspaceTestGit(t, other, "config", "user.email", "o@test.com")
	runWorkspaceTestGit(t, other, "config", "user.name", "Other")
	runWorkspaceTestGit(t, other, "checkout", "-b", "feature", "origin/feature")
	require.NoError(os.WriteFile(
		filepath.Join(other, "f.txt"), []byte("remote\n"), 0o644,
	))
	runWorkspaceTestGit(t, other, "add", ".")
	runWorkspaceTestGit(t, other, "commit", "-m", "remote")
	runWorkspaceTestGit(t, other, "push", "origin", "feature")

	err := PushWorktreeBranch(t.Context(), work)

	require.Error(err)
	Assert.New(t).ErrorIs(err, ErrWorktreeDiverged)
}

func TestPullWorktreeBranchRejectsDirtyWorktree(t *testing.T) {
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty.txt"), []byte("dirty\n"), 0o644,
	))

	err := PullWorktreeBranch(t.Context(), work)

	require.Error(err)
	Assert.New(t).ErrorIs(err, ErrWorktreeDirty)
}
