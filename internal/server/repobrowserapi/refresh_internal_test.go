package repobrowserapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	gitcmd "go.kenn.io/kit/git/cmd"
)

func TestRepoBrowserRefreshDropsRetiredRouteBeforeFetch(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	routeRoot := t.TempDir()
	remote := filepath.Join(routeRoot, "remote.git")
	oldWork := createRefreshTestRepository(t, remote, "old content\n")
	oldSHA := refreshTestGitSHA(t, oldWork, "main")

	identity := db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "stable-old",
		Owner:          "acme",
		Name:           "widgets",
		RepoPath:       "acme/widgets",
	}
	oldID, err := database.UpsertRepoByProviderID(ctx, identity)
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderMetadata(ctx, oldID, db.RepoProviderMetadata{
		CloneURL:      remote,
		DefaultBranch: "main",
	}))

	clones := gitclone.New(filepath.Join(t.TempDir(), "clones"), nil)
	oldRef := gitclone.RepoBrowserRepoRef{
		RepoID:    oldID,
		Provider:  identity.Platform,
		Host:      identity.PlatformHost,
		Owner:     identity.Owner,
		Name:      identity.Name,
		RepoPath:  identity.RepoPath,
		RemoteURL: remote,
	}
	require.NoError(clones.EnsureRepoBrowserClone(ctx, oldRef))

	retiredRemote := filepath.Join(routeRoot, "retired.git")
	require.NoError(os.Rename(remote, retiredRemote))
	replacementWork := createRefreshTestRepository(t, remote, "replacement content\n")
	replacementSHA := refreshTestGitSHA(t, replacementWork, "main")
	require.NotEqual(oldSHA, replacementSHA)

	identity.PlatformRepoID = "stable-replacement"
	replacementID, err := database.UpsertRepoByProviderID(ctx, identity)
	require.NoError(err)
	require.NotEqual(oldID, replacementID)
	require.NoError(database.UpdateRepoProviderMetadata(
		ctx,
		replacementID,
		db.RepoProviderMetadata{
			CloneURL:      remote,
			DefaultBranch: "main",
		},
	))

	handler := New(Deps{
		Resolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{
			DB: database,
		}),
		Clones: clones,
	})
	handler.runRefreshPass(ctx)

	resolved, err := clones.ResolveRepoBrowserRef(ctx, oldRef, gitclone.RepoBrowserRef{
		Type: gitclone.RepoBrowserRefBranch,
		Name: "main",
	})
	require.NoError(err)
	require.Equal(oldSHA, resolved.SHA)
}

func TestRequestRepoBrowserLeaseBlocksRepositoryReplacement(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	createRefreshTestRepository(t, remote, "old content\n")
	identity := db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "stable-old",
		Owner:          "acme",
		Name:           "widgets",
		RepoPath:       "acme/widgets",
	}
	repoID, err := database.UpsertRepoByProviderID(ctx, identity)
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderMetadata(
		ctx,
		repoID,
		db.RepoProviderMetadata{CloneURL: remote, DefaultBranch: "main"},
	))
	handler := New(Deps{
		Resolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{
			DB: database,
		}),
		Clones: gitclone.New(filepath.Join(t.TempDir(), "clones"), nil),
	})

	_, _, release, err := handler.ensureRepoBrowserClone(
		ctx,
		identity.Platform,
		identity.PlatformHost,
		identity.Owner,
		identity.Name,
		identity.RepoPath,
	)
	require.NoError(err)

	writeAttempted := make(chan struct{})
	restore := database.SetBeforeRepositoryReconciliationWriteLockForTest(
		func() { close(writeAttempted) },
	)
	defer restore()
	replacementDone := make(chan error, 1)
	go func() {
		identity.PlatformRepoID = "stable-replacement"
		_, replacementErr := database.UpsertRepoByProviderID(ctx, identity)
		replacementDone <- replacementErr
	}()
	select {
	case <-writeAttempted:
	case <-time.After(2 * time.Second):
		require.Fail("repository replacement did not queue")
	}
	select {
	case replacementErr := <-replacementDone:
		require.NoError(replacementErr)
		require.Fail("repository replacement bypassed request clone lease")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	select {
	case replacementErr := <-replacementDone:
		require.NoError(replacementErr)
	case <-time.After(2 * time.Second):
		require.Fail("repository replacement did not resume")
	}
}

func createRefreshTestRepository(t *testing.T, remote, content string) string {
	t.Helper()
	root := filepath.Dir(remote)
	refreshTestGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	work, err := os.MkdirTemp(root, "work-*")
	require.NoError(t, err)
	require.NoError(t, os.Remove(work))
	refreshTestGit(t, root, "clone", remote, work)
	refreshTestGit(t, work, "config", "user.email", "alice@example.com")
	refreshTestGit(t, work, "config", "user.name", "Alice")
	require.NoError(t, os.WriteFile(filepath.Join(work, "README.md"), []byte(content), 0o644))
	refreshTestGit(t, work, "add", ".")
	refreshTestGit(t, work, "commit", "-m", "initial")
	refreshTestGit(t, work, "push", "origin", "main")
	return work
}

func refreshTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	runner := gitcmd.New().WithConfig("init.defaultBranch", "main")
	out, stderr, err := runner.Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
}

func refreshTestGitSHA(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := gitcmd.New().Output(t.Context(), dir, "rev-parse", ref)
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}
