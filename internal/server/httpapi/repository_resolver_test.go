package httpapi

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

func TestRepositoryResolverRejectsUnavailableStore(t *testing.T) {
	resolver := NewRepositoryResolver(RepositoryResolverDeps{})

	_, err := resolver.Lookup(t.Context(), "github", "github.com", "acme/widget")

	require.ErrorIs(t, err, ErrRepositoryStoreUnavailable)
}

func TestEmptyRegistryDoesNotAdvertiseProviderCapabilities(t *testing.T) {
	assert := assert.New(t)
	resolver := NewRepositoryResolver(RepositoryResolverDeps{
		ProviderCapabilities: func(platform.Kind, string) (platform.Capabilities, error) {
			return platform.Capabilities{}, errors.New("registry unavailable")
		},
	})

	github := resolver.Capabilities(platform.KindGitHub, platform.DefaultGitHubHost)
	gitlab := resolver.Capabilities(platform.KindGitLab, platform.DefaultGitLabHost)

	assert.False(github.ReadRepositories)
	assert.False(github.MergeMutation)
	assert.False(gitlab.ReadRepositories)
	assert.False(gitlab.MergeMutation)
}

func TestProviderCapabilitiesFromPlatformMapsWorkflowWireFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	response := ProviderCapabilitiesFromPlatform(platform.Capabilities{
		ReadWorkflows:    true,
		ReadWorkflowRuns: true,
		WorkflowDispatch: true,
	})

	encoded, err := json.Marshal(response)
	require.NoError(err)
	var fields map[string]any
	require.NoError(json.Unmarshal(encoded, &fields))

	assert.Equal(true, fields["read_workflows"])
	assert.Equal(true, fields["read_workflow_runs"])
	assert.Equal(true, fields["workflow_dispatch"])
}

func TestRepositoryResolverBuildsCanonicalRef(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	repoID, err := reposeed.Seed(t.Context(), database, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: 1001,
		Owner:          "group/subgroup",
		Name:           "widget",
		RepoPath:       "group/subgroup/widget",
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
	ref := resolver.Ref(repo.Repo)

	assert.Equal("gitlab", ref.Provider)
	assert.Equal("gitlab.example.com", ref.PlatformHost)
	assert.Equal("group/subgroup/widget", ref.RepoPath)
	assert.True(ref.Capabilities.ReadRepositories)
}

func TestPlatformRepoRefCarriesProviderIdentityAndIntegerID(t *testing.T) {
	tests := []struct {
		name     string
		repo     db.Repo
		wantKind platform.Kind
		wantHost string
		wantPath string
	}{
		{
			name: "gitlab nested path",
			repo: db.Repo{
				Platform:       string(platform.KindGitLab),
				PlatformHost:   "gitlab.example.com",
				PlatformRepoID: 4242,
				Owner:          "group",
				Name:           "project",
				RepoPath:       "group/project",
			},
			wantKind: platform.KindGitLab,
			wantHost: "gitlab.example.com",
			wantPath: "group/project",
		},
		{
			name: "github defaults",
			repo: db.Repo{
				PlatformRepoID: 4242,
				Owner:          "acme",
				Name:           "widget",
			},
			wantKind: platform.KindGitHub,
			wantHost: platform.DefaultGitHubHost,
			wantPath: "acme/widget",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)

			ref := PlatformRepoRef(tt.repo)

			assert.Equal(tt.wantKind, ref.Platform)
			assert.Equal(tt.wantHost, ref.Host)
			assert.Equal(tt.wantPath, ref.RepoPath)
			assert.Equal(int64(4242), ref.PlatformID)
		})
	}
}

func TestRepositoryResolverRequireRouteCapabilityUsesCanonicalContract(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	_, err := reposeed.Seed(t.Context(), database, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: 1002,
		Owner:          "group",
		Name:           "project",
		RepoPath:       "group/project",
	})
	require.NoError(err)
	resolver := NewRepositoryResolver(RepositoryResolverDeps{
		DB: database,
		ProviderCapabilities: func(platform.Kind, string) (platform.Capabilities, error) {
			return platform.Capabilities{IssueMutation: true}, nil
		},
	})

	repo, err := resolver.RequireRouteCapability(
		t.Context(), "gitlab", "gitlab.example.com", "group", "project", "issue_mutation",
	)
	require.NoError(err)
	require.Equal("group/project", repo.RepoPath)

	_, err = resolver.RequireRouteCapability(
		t.Context(), "gitlab", "gitlab.example.com", "group", "project", "merge_mutation",
	)
	var problem *ProblemError
	require.ErrorAs(err, &problem)
	require.Equal(CodeUnsupportedCapability, problem.Code)
}

func TestRepositoryResolverRefFromPartsAppliesCanonicalDefaults(t *testing.T) {
	resolver := NewRepositoryResolver(RepositoryResolverDeps{})

	ref := resolver.RefFromParts("", "", "acme", "widget")

	assert.Equal(t, string(platform.KindGitHub), ref.Provider)
	assert.Equal(t, platform.DefaultGitHubHost, ref.PlatformHost)
	assert.Equal(t, "acme/widget", ref.RepoPath)
}
