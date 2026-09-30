package settingsapi

import (
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"
)

func cloudTrackedRef(id, name string) ghclient.RepoRef {
	return ghclient.RepoRef{
		Platform: platform.KindBitbucket, PlatformHost: platform.DefaultBitbucketHost,
		Owner: "team", Name: name, RepoPath: "team/" + name,
		Key: platform.RepositoryUUIDKey(uuid.MustParse(id)),
	}
}

const (
	cloudRepoUUID      = "11111111-1111-4111-8111-111111111111"
	otherCloudRepoUUID = "22222222-2222-4222-8222-222222222222"
)

func TestTrackedProvenanceFollowsBitbucketCloudUUID(t *testing.T) {
	assert := assert.New(t)
	tracked := cloudTrackedRef(cloudRepoUUID, "widgets")
	tracked.ConfiguredRepoPath = "team/widgets"
	provenance := trackedRepoProvenance([]ghclient.RepoRef{tracked})

	renamed := withTrackedProvenance(provenance, cloudTrackedRef(cloudRepoUUID, "gadgets"))
	reused := withTrackedProvenance(provenance, cloudTrackedRef(otherCloudRepoUUID, "widgets"))

	assert.Equal("team/widgets", renamed.ConfiguredRepoPath)
	assert.Empty(reused.ConfiguredRepoPath)
}

func TestTrackedRepoIndexFindsRenamedBitbucketCloudRepo(t *testing.T) {
	byRoute, byIdentity := map[string]int{}, map[string]int{}
	indexTrackedRepo(byRoute, byIdentity, cloudTrackedRef(cloudRepoUUID, "widgets"), 0)

	slot, ok := trackedRepoIndex(byRoute, byIdentity, cloudTrackedRef(cloudRepoUUID, "gadgets"))
	_, otherOK := trackedRepoIndex(byRoute, byIdentity, cloudTrackedRef(otherCloudRepoUUID, "gadgets"))

	require.True(t, ok)
	assert.Equal(t, 0, slot)
	assert.False(t, otherOK)
}

func TestPersistResolvedReposCataloguesBitbucketCloudRepo(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	srv := &Handlers{Db: database}

	require.NoError(srv.PersistResolvedRepos(
		t.Context(), []ghclient.RepoRef{cloudTrackedRef(cloudRepoUUID, "widgets")},
	))

	entry, err := database.GetRepositoryByProviderID(t.Context(), platform.RepositoryIdentity{
		Provider: "bitbucket", PlatformHost: platform.DefaultBitbucketHost,
		Key: platform.RepositoryUUIDKey(uuid.MustParse(cloudRepoUUID)),
	})
	require.NoError(err)
	require.NotNil(entry)
	assert.Equal(t, "widgets", entry.Repository.Name)
}
