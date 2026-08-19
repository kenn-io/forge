package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/testutil/dbtest"
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
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "repo-group-subgroup-widget",
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
	ref := resolver.Ref(*repo)

	assert.Equal("gitlab", ref.Provider)
	assert.Equal("gitlab.example.com", ref.PlatformHost)
	assert.Equal("group/subgroup/widget", ref.RepoPath)
	assert.True(ref.Capabilities.ReadRepositories)
}

func TestRepositoryResolverGuardRepositoryRouteFenceBlocksReconciliation(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "provider-old",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)
	repo, err := database.GetRepoByID(t.Context(), repoID)
	require.NoError(err)
	resolver := NewRepositoryResolver(RepositoryResolverDeps{DB: database})
	fence, found, err := resolver.CaptureRepositoryRouteFence(t.Context(), *repo)
	require.NoError(err)
	require.True(found)

	publishStarted := make(chan struct{})
	releasePublish := make(chan struct{})
	type guardResult struct {
		matches bool
		err     error
	}
	guardDone := make(chan guardResult, 1)
	go func() {
		matches, guardErr := resolver.GuardRepositoryRouteFence(
			context.Background(), *repo, fence, func() error {
				close(publishStarted)
				<-releasePublish
				return nil
			},
		)
		guardDone <- guardResult{matches: matches, err: guardErr}
	}()
	<-publishStarted

	writerWaiting := make(chan struct{})
	restoreHook := database.SetBeforeRepositoryReconciliationWriteLockForTest(func() {
		close(writerWaiting)
	})
	t.Cleanup(restoreHook)
	writerDone := make(chan error, 1)
	go func() {
		_, _, reconcileErr := database.ReconcileRepositoryObservation(
			context.Background(),
			db.RepoIdentity{
				Platform:       "github",
				PlatformHost:   "github.com",
				PlatformRepoID: "provider-new",
				Owner:          "acme",
				Name:           "widget",
				RepoPath:       "acme/widget",
			},
			time.Now().UTC().Add(time.Hour),
		)
		writerDone <- reconcileErr
	}()
	<-writerWaiting
	select {
	case writerErr := <-writerDone:
		require.Fail("reconciliation completed while publication guard was held", writerErr)
	default:
	}

	close(releasePublish)
	guarded := <-guardDone
	require.NoError(guarded.err)
	assert.True(guarded.matches)
	require.NoError(<-writerDone)

	stalePublishCalled := false
	matches, err := resolver.GuardRepositoryRouteFence(
		t.Context(), *repo, fence, func() error {
			stalePublishCalled = true
			return nil
		},
	)
	require.NoError(err)
	assert.False(matches)
	assert.False(stalePublishCalled)
}

func TestPlatformRepoRefRestoresProviderIdentityAndNumericID(t *testing.T) {
	assert := assert.New(t)

	ref := PlatformRepoRef(db.Repo{
		Platform:       string(platform.KindGitLab),
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "4242",
		Owner:          "group",
		Name:           "project",
		RepoPath:       "group/project",
	})

	assert.Equal(platform.KindGitLab, ref.Platform)
	assert.Equal("gitlab.example.com", ref.Host)
	assert.Equal("group/project", ref.RepoPath)
	assert.Equal(int64(4242), ref.PlatformID)
	assert.Equal("4242", ref.PlatformExternalID)
}

func TestPlatformRepoRefPreservesExternalIDAndGitHubDefaults(t *testing.T) {
	assert := assert.New(t)

	ref := PlatformRepoRef(db.Repo{
		PlatformRepoID: "gid://github/Repository/4242",
		Owner:          "acme",
		Name:           "widget",
	})

	assert.Equal(platform.KindGitHub, ref.Platform)
	assert.Equal(platform.DefaultGitHubHost, ref.Host)
	assert.Equal("acme/widget", ref.RepoPath)
	assert.Zero(ref.PlatformID)
	assert.Equal("gid://github/Repository/4242", ref.PlatformExternalID)
}

func TestRepositoryResolverRequireRouteCapabilityUsesCanonicalContract(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	_, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "repo-group-project",
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

func TestRequireRouteCapabilitySuspendsProviderWritesForDegradedHost(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	_, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "repo-acme-widgets",
		Owner:          "acme",
		Name:           "widgets",
		RepoPath:       "acme/widgets",
	})
	require.NoError(err)
	suspended := true
	resolver := NewRepositoryResolver(RepositoryResolverDeps{
		DB: database,
		ProviderCapabilities: func(platform.Kind, string) (platform.Capabilities, error) {
			return platform.Capabilities{
				CommentMutation: true, ReadReviewThreads: true,
			}, nil
		},
		ProviderWritesSuspended: func(kind platform.Kind, host string) bool {
			return suspended && kind == platform.KindGitHub && host == "github.com"
		},
	})

	_, err = resolver.RequireRouteCapability(
		t.Context(), "github", "github.com", "acme", "widgets", "comment_mutation",
	)
	var problem *ProblemError
	require.ErrorAs(err, &problem)
	assert.Equal(CodeServiceUnavailable, problem.Code)

	// Reads keep serving the local archive while writes are suspended.
	repo, err := resolver.RequireRouteCapability(
		t.Context(), "github", "github.com", "acme", "widgets", "read_review_threads",
	)
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(resolver.RequireProviderWritable(db.Repo{
		Platform: "github", PlatformHost: "github.example",
	}), "other hosts stay writable")

	suspended = false
	_, err = resolver.RequireRouteCapability(
		t.Context(), "github", "github.com", "acme", "widgets", "comment_mutation",
	)
	require.NoError(err)
}

func TestRepositoryResolverRefFromPartsAppliesCanonicalDefaults(t *testing.T) {
	resolver := NewRepositoryResolver(RepositoryResolverDeps{})

	ref := resolver.RefFromParts("", "", "acme", "widget")

	assert.Equal(t, string(platform.KindGitHub), ref.Provider)
	assert.Equal(t, platform.DefaultGitHubHost, ref.PlatformHost)
	assert.Equal(t, "acme/widget", ref.RepoPath)
}
