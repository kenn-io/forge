package platformdb

import (
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"
)

func TestBitbucketUUIDSurvivesRepositoryRename(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := dbtest.Open(t)
	repositoryUUID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	ref := platform.RepoRef{
		Platform: platform.KindBitbucket, Host: "bitbucket.org",
		BitbucketRepositoryUUID: repositoryUUID,
		Owner:                   "team", Name: "widgets", RepoPath: "team/widgets",
	}
	first, err := d.ObserveRepository(t.Context(), DBRepoIdentity(ref))
	require.NoError(err)
	ref.Owner, ref.Name, ref.RepoPath = "new-team", "renamed", "new-team/renamed"
	renamed, err := d.ObserveRepository(t.Context(), DBRepoIdentity(ref))
	require.NoError(err)
	assert.Equal(first.Repository.ID, renamed.Repository.ID)
	stored, err := d.GetRepoByID(t.Context(), first.Repository.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal(repositoryUUID, stored.BitbucketRepositoryUUID)
	assert.Equal(int64(0), stored.PlatformRepoID)
	assert.Equal("new-team/renamed", stored.RepoPath)
}
