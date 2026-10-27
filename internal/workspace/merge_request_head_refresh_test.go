package workspace

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/platform"
)

const forkRefreshPRNumber = 245

type forkPullRefFixture struct {
	db       *db.DB
	mgr      *Manager
	ws       *Workspace
	repoID   int64
	remote   string
	fork     string
	cloneDir string
	branch   string
	pullRef  string
	spec     WorkspaceLaunchSpec
}

// newForkPullRefFixture creates a ready workspace for a fork PR in a managed
// clone, with separate bare repositories for the base and the fork.
func newForkPullRefFixture(t *testing.T) *forkPullRefFixture {
	t.Helper()
	require := require.New(t)
	d := openTestDB(t)
	cloneBaseDir := gitfixture.ScratchDir(t)
	const (
		host   = "github.com"
		owner  = "acme"
		name   = "widget"
		branch = "fork/thing"
	)
	repoID := seedRepo(t, d, host, owner, name)
	remote, _ := setupRemoteForForkPRWorktreeTest(t, branch, forkRefreshPRNumber)
	fork := filepath.Join(gitfixture.ScratchDir(t), "fork.git")
	runWorkspaceTestGit(t, gitfixture.ScratchDir(t), "clone", "--bare", remote, fork)
	const forkURL = "https://github.com/contributor/widget.git"
	seedMRWithHeadRepo(t, d, repoID, forkRefreshPRNumber, branch, forkURL)
	clones := gitclone.New(cloneBaseDir, nil)
	cloneDir, err := clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(t.Context(), platform.RepositoryIdentity{Key: platform.RepositoryIDKey(testRepoID(owner, name))}),
		"github", host, owner, name,
	)
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(cloneDir), 0o755))
	runWorkspaceTestGit(t, cloneBaseDir, "clone", "--bare", remote, cloneDir)
	runWorkspaceTestGit(t, cloneDir, "remote", "set-url", "origin", "https://github.com/acme/widget.git")
	for _, url := range []string{"https://github.com/acme/widget.git", "https://github.com/acme/renamed.git"} {
		runWorkspaceTestGit(t, cloneDir, "config", "--add", "url."+remote+".insteadOf", url)
	}
	runWorkspaceTestGit(t, cloneDir, "config", "--add", "url."+fork+".insteadOf", forkURL)
	runWorkspaceTestGit(t, cloneDir, "update-ref", "-d", "refs/heads/"+branch)
	tmuxScript, _ := writeRecorderScript(t)

	mgr := newTestManager(t, d, gitfixture.ScratchDir(t))
	mgr.SetClones(clones)
	mgr.SetTmuxCommand([]string{tmuxScript})
	spec, err := databaseLaunchSpecResolver{db: d}.ResolveWorkspaceLaunchSpec(
		t.Context(), providerplane.WorkspaceLaunchRequest{
			Repository: providerplane.RepositoryRoute{
				Provider: "github", PlatformHost: host, Owner: owner, Name: name,
			},
			ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: forkRefreshPRNumber,
		},
	)
	require.NoError(err)
	spec.Pull.HeadRepoKind = "fork"
	spec.Pull.HeadRepoCloneURL = forkURL
	ws, err := mgr.CreateFromLaunchSpec(t.Context(), spec)
	require.NoError(err)
	require.NoError(mgr.Setup(t.Context(), ws))
	_, hasUpstream, err := WorktreeDivergence(t.Context(), ws.WorktreePath)
	require.NoError(err)
	require.False(hasUpstream, "a fork head must stay untracked")
	runWorkspaceTestGit(t, ws.WorktreePath, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, ws.WorktreePath, "config", "user.name", "Test")
	return &forkPullRefFixture{
		db: d, mgr: mgr, ws: ws, repoID: repoID, remote: remote, fork: fork,
		cloneDir: cloneDir, branch: branch, spec: spec,
		pullRef: "refs/pull/" + strconv.Itoa(forkRefreshPRNumber) + "/head",
	}
}

// pushFromWorkspace commits in the workspace and pushes to the fork, then
// moves the base repository's pull ref as the provider would. Nothing in the
// push touches the workspace's local pull ref.
func (f *forkPullRefFixture) pushFromWorkspace(t *testing.T) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.ws.WorktreePath, "pushed.txt"), []byte("pushed\n"), 0o644))
	runWorkspaceTestGit(t, f.ws.WorktreePath, "add", ".")
	runWorkspaceTestGit(t, f.ws.WorktreePath, "commit", "-m", "pushed")
	runWorkspaceTestGit(t, f.ws.WorktreePath, "push", f.fork, "HEAD:refs/heads/"+f.branch)
	f.providerMovesPullRef(t)
	return strings.TrimSpace(string(runWorkspaceTestGit(t, f.ws.WorktreePath, "rev-parse", "HEAD")))
}

func (f *forkPullRefFixture) providerMovesPullRef(t *testing.T) {
	t.Helper()
	runWorkspaceTestGit(t, f.remote, "fetch", f.fork, "+refs/heads/"+f.branch+":"+f.pullRef)
}

// setProviderHead stands in for the provider sync that records the PR's new head.
func (f *forkPullRefFixture) setProviderHead(t *testing.T, sha string) {
	t.Helper()
	mr, err := f.db.GetMergeRequestByRepoIDAndNumber(t.Context(), f.repoID, forkRefreshPRNumber)
	require.NoError(t, err)
	require.NotNil(t, mr)
	mr.PlatformHeadSHA = sha
	_, err = f.db.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
}

func (f *forkPullRefFixture) divergence(t *testing.T) Divergence {
	t.Helper()
	div, ok, err := WorktreeDivergenceFromRef(t.Context(), f.ws.WorktreePath, f.pullRef)
	require.NoError(t, err)
	require.True(t, ok)
	return div
}

// A fork PR workspace counts commits against its fetched pull ref. Pushing to
// the fork never moves that ref, so without a refresh the pushed commits read
// as unpushed, and a push by someone else never shows the workspace as behind.
func TestPushedHeadObserverKeepsForkPullRefAtProviderHead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newForkPullRefFixture(t)
	observer := NewPushedHeadObserver(f.db, f.mgr, PRMonitorOptions{LaunchSpecs: f.mgr})

	pushed := f.pushFromWorkspace(t)
	require.Equal(Divergence{Ahead: 1}, f.divergence(t), "the stale ref reports the push as unpushed")

	f.setProviderHead(t, pushed)
	result, err := observer.RunOnce(t.Context())
	require.NoError(err)
	assert.Equal([]string{f.ws.ID}, result.HeadRefRefreshes)
	assert.Equal(Divergence{}, f.divergence(t))

	// Another maintainer pushes to the same PR from their own checkout.
	other := filepath.Join(gitfixture.ScratchDir(t), "other")
	runWorkspaceTestGit(t, gitfixture.ScratchDir(t), "clone", "--branch", f.branch, f.fork, other)
	runWorkspaceTestGit(t, other, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, other, "config", "user.name", "Test")
	require.NoError(os.WriteFile(filepath.Join(other, "other.txt"), []byte("other\n"), 0o644))
	runWorkspaceTestGit(t, other, "add", ".")
	runWorkspaceTestGit(t, other, "commit", "-m", "other maintainer")
	runWorkspaceTestGit(t, other, "push", "origin", "HEAD:refs/heads/"+f.branch)
	f.providerMovesPullRef(t)
	otherHead := strings.TrimSpace(string(runWorkspaceTestGit(t, other, "rev-parse", "HEAD")))

	f.setProviderHead(t, otherHead)
	result, err = observer.RunOnce(t.Context())
	require.NoError(err)
	assert.Equal([]string{f.ws.ID}, result.HeadRefRefreshes)
	assert.Equal(Divergence{Behind: 1}, f.divergence(t))
}

// A daemon that must authenticate provider Git fails the refresh closed when
// its credential route is gone, rather than fetching anonymously.
func TestRefreshWorkspaceMergeRequestHeadRefRequiresConfiguredCredential(t *testing.T) {
	require := require.New(t)
	f := newForkPullRefFixture(t)
	f.pushFromWorkspace(t)
	f.mgr.SetRequireProviderCredential(true)

	err := f.mgr.RefreshWorkspaceMergeRequestHeadRef(t.Context(), f.ws)

	require.ErrorIs(err, gitclone.ErrCredentialUnavailable)
	require.Equal(Divergence{Ahead: 1}, f.divergence(t))
}

// A repository rename leaves the managed clone's origin on the old route.
// The refresh must retarget it like workspace reuse does, or every fetch
// fails the origin identity check.
func TestRefreshWorkspaceMergeRequestHeadRefRetargetsRenamedOrigin(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newForkPullRefFixture(t)
	f.pushFromWorkspace(t)
	const renamedURL = "https://github.com/acme/renamed.git"
	_, err := f.db.ObserveRepository(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Key: f.spec.Repository.Key, Owner: "acme", Name: "renamed",
	})
	require.NoError(err)
	renamed := f.spec
	renamed.Repository.Name = "renamed"
	renamed.Repository.CloneURL = renamedURL
	renamed.IssuedAt = f.spec.IssuedAt.Add(time.Minute)
	renamed.SourceVisibleUntil = renamed.IssuedAt.Add(WorkspaceLaunchSpecVisibilityLease)
	f.mgr.SetLaunchSpecResolver(&staticLaunchSpecResolver{spec: renamed})
	f.mgr.SetNow(func() time.Time { return renamed.IssuedAt })
	ws, err := f.db.GetWorkspace(t.Context(), f.ws.ID)
	require.NoError(err)
	require.NotNil(ws)
	require.Equal("renamed", ws.RepoName)

	require.NoError(f.mgr.RefreshWorkspaceMergeRequestHeadRef(t.Context(), ws))

	assert.Equal(Divergence{}, f.divergence(t))
	originURLs, err := gitConfigValues(t.Context(), f.cloneDir, "remote.origin.url")
	require.NoError(err)
	assert.Equal([]string{renamedURL}, originURLs)
}
