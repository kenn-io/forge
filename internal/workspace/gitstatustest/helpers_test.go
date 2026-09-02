package gitstatustest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/workspace"
	gitcmd "go.kenn.io/kit/git/cmd"
)

type Manager = workspace.Manager
type Divergence = workspace.Divergence
type WorkspaceLaunchSpec = db.WorkspaceLaunchSpec
type WorkspaceLaunchRepository = db.WorkspaceLaunchRepository
type WorkspaceLaunchPull = db.WorkspaceLaunchPull

var (
	ErrWorktreeDirty                   = workspace.ErrWorktreeDirty
	ErrWorktreeDiverged                = workspace.ErrWorktreeDiverged
	ErrWorktreeNoUpstream              = workspace.ErrWorktreeNoUpstream
	ErrLaunchSpecRefreshRequired       = workspace.ErrLaunchSpecRefreshRequired
	ErrWorktreeInSync                  = workspace.ErrWorktreeInSync
	LaunchSpecErrorRetryable           = workspace.LaunchSpecErrorRetryable
	NewManager                         = workspace.NewManager
	WorktreeDivergence                 = workspace.WorktreeDivergence
	WorktreeUnpushedSHAs               = workspace.WorktreeUnpushedSHAs
	WorkspaceLaunchSpecVersion         = db.WorkspaceLaunchSpecVersion
	WorkspaceLaunchSpecVisibilityLease = db.WorkspaceLaunchSpecVisibilityLease
)

func TestMain(m *testing.M) {
	os.Exit(gitsafe.RunIsolatedMain(m))
}

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.Open(t)
}

func runWorkspaceTestGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	runner := gitcmd.New().WithConfig("init.defaultBranch", "main")
	out, stderr, err := runner.Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
	return out
}

type unavailableLaunchSpecResolver struct{}

type staticLaunchSpecResolver struct {
	spec db.WorkspaceLaunchSpec
}

func (r *staticLaunchSpecResolver) ResolveWorkspaceLaunchSpec(
	context.Context, providerplane.WorkspaceLaunchRequest,
) (db.WorkspaceLaunchSpec, error) {
	return r.spec, nil
}

func (r *staticLaunchSpecResolver) RefreshWorkspaceLaunchSpec(
	context.Context, db.WorkspaceLaunchSpec,
) (db.WorkspaceLaunchSpec, error) {
	return r.spec, nil
}

func (unavailableLaunchSpecResolver) ResolveWorkspaceLaunchSpec(
	context.Context, providerplane.WorkspaceLaunchRequest,
) (db.WorkspaceLaunchSpec, error) {
	return db.WorkspaceLaunchSpec{}, providerplane.ErrHubUnavailable
}

func (unavailableLaunchSpecResolver) RefreshWorkspaceLaunchSpec(
	context.Context, db.WorkspaceLaunchSpec,
) (db.WorkspaceLaunchSpec, error) {
	return db.WorkspaceLaunchSpec{}, providerplane.ErrHubUnavailable
}

func launchSpecForTest() WorkspaceLaunchSpec {
	issuedAt := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	return WorkspaceLaunchSpec{
		Version: WorkspaceLaunchSpecVersion,
		Repository: WorkspaceLaunchRepository{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: "repo-1",
			Owner: "acme", Name: "widget", CloneURL: "https://github.com/acme/widget.git",
			DefaultBranch: "main",
		},
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7,
		ItemKey: "7", GitHeadRef: "feature/seven",
		Pull: &WorkspaceLaunchPull{
			HeadBranch: "feature/seven", HeadRepoKind: "same_repo", SnapshotRevision: 3,
		},
		SourceVisible: true, IssuedAt: issuedAt,
		SourceVisibleUntil: issuedAt.Add(WorkspaceLaunchSpecVisibilityLease),
	}
}

func seedLaunchSpecRepository(t *testing.T, database *db.DB, spec WorkspaceLaunchSpec) int64 {
	t.Helper()
	repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform: spec.Repository.Provider, PlatformHost: spec.Repository.PlatformHost,
		PlatformRepoID: spec.Repository.PlatformRepoID,
		Owner:          spec.Repository.Owner, Name: spec.Repository.Name,
	})
	require.NoError(t, err)
	return repoID
}
