package github

import (
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

func TestSyncRepoFollowsBitbucketCloudRenameByUUID(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repositoryUUID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	original, err := d.ObserveRepository(ctx, db.RepoIdentity{
		Platform:                "bitbucket",
		PlatformHost:            platform.DefaultBitbucketHost,
		BitbucketRepositoryUUID: repositoryUUID,
		Owner:                   "team",
		Name:                    "widgets",
		RepoPath:                "team/widgets",
	})
	require.NoError(err)
	originalID := original.Repository.ID
	repo := RepoRef{
		Platform:                platform.KindBitbucket,
		PlatformHost:            platform.DefaultBitbucketHost,
		Owner:                   "team",
		Name:                    "widgets",
		RepoPath:                "team/widgets",
		BitbucketRepositoryUUID: repositoryUUID,
	}
	provider := &syncTestRepositoryReadProvider{
		syncTestReadProvider: &syncTestReadProvider{
			kind: platform.KindBitbucket,
			host: platform.DefaultBitbucketHost,
		},
		repository: platform.Repository{Ref: platform.RepoRef{
			Platform:                platform.KindBitbucket,
			Host:                    platform.DefaultBitbucketHost,
			Owner:                   "team",
			Name:                    "gadgets",
			RepoPath:                "team/gadgets",
			BitbucketRepositoryUUID: repositoryUUID,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(registry, d, nil, []RepoRef{repo}, time.Minute, nil, nil)

	require.NoError(syncer.syncRepo(ctx, repo))

	repos, err := d.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 1)
	assert.Equal(originalID, repos[0].ID)
	assert.Equal("gadgets", repos[0].Name)
	assert.Equal(int64(0), repos[0].PlatformRepoID)
	assert.Equal(repositoryUUID, repos[0].BitbucketRepositoryUUID)
	tracked := syncer.TrackedRepos()
	require.Len(tracked, 1)
	assert.Equal("gadgets", tracked[0].Name)
	assert.Equal(repositoryUUID, tracked[0].BitbucketRepositoryUUID)
}
