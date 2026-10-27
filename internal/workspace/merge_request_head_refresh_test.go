package workspace

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/platform"
)

// A fork PR workspace counts commits against its fetched pull ref. Pushing to
// the fork never moves that ref, so without a refresh the pushed commits read
// as unpushed, and a push by someone else never shows the workspace as behind.
func TestPushedHeadObserverKeepsForkPullRefAtProviderHead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	cloneBaseDir := t.TempDir()

	const (
		host     = "github.com"
		owner    = "acme"
		name     = "widget"
		prNumber = 245
		branch   = "fork/thing"
	)
	pullRef := "refs/pull/" + strconv.Itoa(prNumber) + "/head"
	repoID := seedRepo(t, d, host, owner, name)
	remote, _ := setupRemoteForForkPRWorktreeTest(t, branch, prNumber)
	seedMRWithHeadRepo(t, d, repoID, prNumber, branch, remote)
	clones := gitclone.New(cloneBaseDir, nil)
	cloneDir, err := clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(t.Context(), platform.RepositoryIdentity{Key: platform.RepositoryIDKey(testRepoID(owner, name))}),
		"github", host, owner, name,
	)
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(cloneDir), 0o755))
	runWorkspaceTestGit(t, cloneBaseDir, "clone", "--bare", remote, cloneDir)
	runWorkspaceTestGit(t, cloneDir, "remote", "set-url", "origin", "https://github.com/acme/widget.git")
	runWorkspaceTestGit(t, cloneDir, "config", "--add", "url."+remote+".insteadOf", "https://github.com/acme/widget.git")
	runWorkspaceTestGit(t, cloneDir, "update-ref", "-d", "refs/heads/"+branch)
	tmuxScript, _ := writeRecorderScript(t)

	mgr := newTestManager(t, d, t.TempDir())
	mgr.SetClones(clones)
	mgr.SetTmuxCommand([]string{tmuxScript})
	spec, err := databaseLaunchSpecResolver{db: d}.ResolveWorkspaceLaunchSpec(
		t.Context(), providerplane.WorkspaceLaunchRequest{
			Repository: providerplane.RepositoryRoute{
				Provider: "github", PlatformHost: host, Owner: owner, Name: name,
			},
			ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: prNumber,
		},
	)
	require.NoError(err)
	spec.Pull.HeadRepoKind = "fork"
	spec.Pull.HeadRepoCloneURL = remote
	ws, err := mgr.CreateFromLaunchSpec(t.Context(), spec)
	require.NoError(err)
	require.NoError(mgr.Setup(t.Context(), ws))
	_, hasUpstream, err := WorktreeDivergence(t.Context(), ws.WorktreePath)
	require.NoError(err)
	require.False(hasUpstream, "a fork head must stay untracked")

	observer := NewPushedHeadObserver(d, mgr, PRMonitorOptions{LaunchSpecs: mgr})
	// Stands in for the provider sync that records the PR's new head.
	setProviderHead := func(sha string) {
		t.Helper()
		mr, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, prNumber)
		require.NoError(err)
		require.NotNil(mr)
		mr.PlatformHeadSHA = sha
		_, err = d.UpsertMergeRequest(t.Context(), mr)
		require.NoError(err)
	}
	divergence := func() Divergence {
		t.Helper()
		div, ok, err := WorktreeDivergenceFromRef(t.Context(), ws.WorktreePath, pullRef)
		require.NoError(err)
		require.True(ok)
		return div
	}

	// Commit in the workspace and push to the fork. The provider moves its
	// pull ref, but nothing in the push touches the local one.
	runWorkspaceTestGit(t, ws.WorktreePath, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, ws.WorktreePath, "config", "user.name", "Test")
	require.NoError(os.WriteFile(filepath.Join(ws.WorktreePath, "pushed.txt"), []byte("pushed\n"), 0o644))
	runWorkspaceTestGit(t, ws.WorktreePath, "add", ".")
	runWorkspaceTestGit(t, ws.WorktreePath, "commit", "-m", "pushed")
	runWorkspaceTestGit(t, ws.WorktreePath, "push", remote, "HEAD:refs/heads/"+branch, "HEAD:"+pullRef)
	pushed := strings.TrimSpace(string(runWorkspaceTestGit(t, ws.WorktreePath, "rev-parse", "HEAD")))
	require.Equal(Divergence{Ahead: 1}, divergence(), "the stale ref reports the push as unpushed")

	setProviderHead(pushed)
	result, err := observer.RunOnce(t.Context())
	require.NoError(err)
	assert.Equal([]string{ws.ID}, result.HeadRefRefreshes)
	assert.Equal(Divergence{}, divergence())

	// Another maintainer pushes to the same PR from their own checkout.
	other := filepath.Join(t.TempDir(), "other")
	runWorkspaceTestGit(t, t.TempDir(), "clone", "--branch", branch, remote, other)
	runWorkspaceTestGit(t, other, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, other, "config", "user.name", "Test")
	require.NoError(os.WriteFile(filepath.Join(other, "other.txt"), []byte("other\n"), 0o644))
	runWorkspaceTestGit(t, other, "add", ".")
	runWorkspaceTestGit(t, other, "commit", "-m", "other maintainer")
	runWorkspaceTestGit(t, other, "push", "origin", "HEAD:refs/heads/"+branch, "HEAD:"+pullRef)
	otherHead := strings.TrimSpace(string(runWorkspaceTestGit(t, other, "rev-parse", "HEAD")))

	setProviderHead(otherHead)
	result, err = observer.RunOnce(t.Context())
	require.NoError(err)
	assert.Equal([]string{ws.ID}, result.HeadRefRefreshes)
	assert.Equal(Divergence{Behind: 1}, divergence())
}
