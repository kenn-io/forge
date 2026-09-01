package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestRunProviderOnlySeedsLaunchSpecSourceWithoutWorkspace(t *testing.T) {
	require := require.New(t)
	dbPath := filepath.Join(t.TempDir(), "forge.db")
	cloneURL := "/data/member/worktrees/origin.git"
	require.NoError(run(t.Context(), []string{
		"-db", dbPath,
		"-provider-only",
		"-clone-url", cloneURL,
	}))

	database := dbtest.OpenPreparedAt(t, dbPath)

	repo, err := database.GetRepoByIdentity(t.Context(), db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		RepoPath:     "acme/fleet-widget",
	})
	require.NoError(err)
	require.NotNil(repo)
	require.Equal("e2e-fleet-widget", repo.PlatformRepoID)
	require.Equal("https://github.com/acme/fleet-widget", repo.WebURL)
	require.Equal(cloneURL, repo.CloneURL)
	require.Equal("main", repo.DefaultBranch)

	pull, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repo.ID, 7)
	require.NoError(err)
	require.NotNil(pull)
	require.Equal("feature/fleet-read", pull.HeadBranch)

	workspaces, err := database.ListWorkspaces(t.Context())
	require.NoError(err)
	require.Empty(workspaces)
}

func TestRunProviderOnlyRequiresCloneURL(t *testing.T) {
	require := require.New(t)
	err := run(t.Context(), []string{
		"-db", filepath.Join(t.TempDir(), "forge.db"),
		"-provider-only",
	})
	require.EqualError(err, "-clone-url is required with -provider-only")
}
