package workspace

import (
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
)

func TestValidateWorkspaceLaunchSpecRejectsOtherBitbucketCloudUUID(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	repoUUID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	observed, err := database.ObserveRepository(t.Context(), db.RepoIdentity{
		Platform: "bitbucket", PlatformHost: "bitbucket.org",
		BitbucketRepositoryUUID: repoUUID, Owner: "team", Name: "widgets",
	})
	require.NoError(err)
	manager := NewManager(database, t.TempDir())
	issuedAt := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	workspace := &Workspace{
		ID: "ws-cloud-uuid", Platform: "bitbucket", PlatformHost: "bitbucket.org",
		RepoOwner: "team", RepoName: "widgets", RepoID: observed.Repository.ID,
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7,
		ItemKey: "7", GitHeadRef: "feature/seven",
	}
	spec := WorkspaceLaunchSpec{
		Version: db.WorkspaceLaunchSpecVersion,
		Repository: db.WorkspaceLaunchRepository{
			Provider: "bitbucket", PlatformHost: "bitbucket.org",
			BitbucketRepositoryUUID: uuid.MustParse("22222222-2222-4222-8222-222222222222"),
			Owner:                   "team", Name: "widgets",
			CloneURL: "https://bitbucket.org/team/widgets.git", DefaultBranch: "main",
		},
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7,
		ItemKey: "7", GitHeadRef: "feature/seven",
		Pull: &db.WorkspaceLaunchPull{
			HeadBranch: "feature/seven", HeadRepoKind: "same_repo", SnapshotRevision: 1,
		},
		SourceVisible: true, IssuedAt: issuedAt,
		SourceVisibleUntil: issuedAt.Add(db.WorkspaceLaunchSpecVisibilityLease),
	}

	_, err = manager.validateWorkspaceLaunchSpec(t.Context(), workspace, spec)
	require.ErrorIs(err, db.ErrRepositoryIdentityChanged)

	spec.Repository.BitbucketRepositoryUUID = repoUUID
	_, err = manager.validateWorkspaceLaunchSpec(t.Context(), workspace, spec)
	require.NoError(err)
}
