package gitclone

import (
	"path/filepath"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	providerplatform "go.kenn.io/forge/platform"
)

func TestClonePathPartitionsBitbucketCloudRouteReuseByUUID(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mgr := New(t.TempDir(), nil)
	cloud := func(id string) providerplatform.RepositoryIdentity {
		return providerplatform.RepositoryIdentity{
			Provider: "bitbucket", PlatformHost: "bitbucket.org",
			BitbucketRepositoryUUID: uuid.MustParse(id),
		}
	}
	pathFor := func(identity providerplatform.RepositoryIdentity) string {
		path, err := mgr.ClonePathForContext(
			WithRepositoryIdentity(t.Context(), identity),
			"bitbucket", "bitbucket.org", "team", "widgets",
		)
		require.NoError(err)
		return path
	}
	routePath, err := mgr.ClonePathForContext(
		t.Context(), "bitbucket", "bitbucket.org", "team", "widgets",
	)
	require.NoError(err)

	original := pathFor(cloud("11111111-1111-4111-8111-111111111111"))
	replacement := pathFor(cloud("22222222-2222-4222-8222-222222222222"))

	assert.NotEqual(original, replacement)
	assert.NotEqual(routePath, original)
	assert.Equal(original, pathFor(cloud("11111111-1111-4111-8111-111111111111")))
}

func TestClonePathKeepsIntegerIdentityLayout(t *testing.T) {
	baseDir := t.TempDir()
	mgr := New(baseDir, nil)

	path, err := mgr.ClonePathForContext(
		WithRepositoryIdentity(t.Context(), providerplatform.RepositoryIdentity{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: 1001,
		}),
		"github", "github.com", "acme", "widgets",
	)

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(
		baseDir, "repo-fe675fe7aaee830b6fed09b64e034f84",
		"github.com", "acme", "widgets.git",
	), path)
}
