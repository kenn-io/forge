package httpapi

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestRepositoryResolverRejectsUnavailableStore(t *testing.T) {
	resolver := NewRepositoryResolver(RepositoryResolverDeps{})

	_, err := resolver.Lookup(t.Context(), "github", "github.com", "acme/widget")

	require.ErrorIs(t, err, ErrRepositoryStoreUnavailable)
}

func TestRepositoryResolverOwnsCapabilityFallbackPolicy(t *testing.T) {
	assert := assert.New(t)
	resolver := NewRepositoryResolver(RepositoryResolverDeps{
		ProviderCapabilities: func(platform.Kind, string) (platform.Capabilities, error) {
			return platform.Capabilities{}, errors.New("registry unavailable")
		},
	})

	github := resolver.Capabilities(platform.KindGitHub, platform.DefaultGitHubHost)
	gitlab := resolver.Capabilities(platform.KindGitLab, platform.DefaultGitLabHost)

	assert.True(github.ReadRepositories)
	assert.True(github.MergeMutation)
	assert.False(gitlab.ReadRepositories)
	assert.False(gitlab.MergeMutation)
}

func TestRepositoryResolverBuildsCanonicalRef(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "group/subgroup",
		Name:         "widget",
		RepoPath:     "group/subgroup/widget",
	})
	require.NoError(err)
	require.Positive(repoID)
	resolver := NewRepositoryResolver(RepositoryResolverDeps{
		DB: database,
		ProviderCapabilities: func(kind platform.Kind, host string) (platform.Capabilities, error) {
			assert.Equal(platform.KindGitLab, kind)
			assert.Equal("gitlab.example.com", host)
			return platform.Capabilities{ReadRepositories: true}, nil
		},
	})

	repo, err := resolver.Lookup(t.Context(), "gitlab", "gitlab.example.com", "group/subgroup/widget")
	require.NoError(err)
	ref := resolver.Ref(*repo)

	assert.Equal("gitlab", ref.Provider)
	assert.Equal("gitlab.example.com", ref.PlatformHost)
	assert.Equal("group/subgroup/widget", ref.RepoPath)
	assert.True(ref.Capabilities.ReadRepositories)
}
