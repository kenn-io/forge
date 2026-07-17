package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/tokenauth"
)

type fakeGitHubIdentityResolver struct {
	byEnv map[string]github.GitHubIdentity
	err   map[string]error
}

func (r fakeGitHubIdentityResolver) ResolvePAT(
	_ context.Context, host string, source tokenauth.Source,
) (github.GitHubIdentity, error) {
	desc := source.Descriptor()
	for _, candidate := range desc.Candidates {
		if candidate.Kind != tokenauth.SourceKindEnv {
			continue
		}
		if err := r.err[candidate.EnvName]; err != nil {
			return github.GitHubIdentity{}, err
		}
		if identity, ok := r.byEnv[candidate.EnvName]; ok {
			return identity, nil
		}
	}
	return github.GitHubIdentity{}, fmt.Errorf(
		"no fake identity for %s on %s: %w",
		desc.SafeString(), host, tokenauth.ErrMissingToken,
	)
}

func TestBuildProviderStartupDeduplicatesGitHubIdentityRuntimes(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	for _, env := range []string{"PAT_A", "PAT_B", "PAT_C", "APP_WRITE_PAT"} {
		t.Setenv(env, env+"-secret")
	}

	cfg := &config.Config{
		SyncBudgetPerHour: 200,
		SyncInterval:      "5m",
		Host:              "127.0.0.1",
		Port:              8091,
		BasePath:          "/",
		Activity: config.Activity{
			ViewMode: "flat", TimeRange: "7d",
		},
		Repos: []config.Repo{
			{Owner: "org-a", Name: "one"},
			{Owner: "org-b", Name: "two"},
			{Owner: "org-c", Name: "three"},
			{Owner: "org-d", Name: "four"},
		},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{
			{Host: "github.com", Owner: "org-a", TokenEnv: "PAT_A"},
			{Host: "github.com", Owner: "org-b", TokenEnv: "PAT_B"},
			{Host: "github.com", Owner: "org-c", TokenEnv: "PAT_C"},
			{Host: "github.com", Owner: "org-d", TokenEnv: "APP_WRITE_PAT"},
		},
		GitHubApps: []config.GitHubAppConfig{{
			Host: "github.com", AppID: 7, PrivateKeyPath: "/keys/app.pem",
			InstallationID: 789, InstallationAccount: "org-d",
			RepositorySelection: "all",
		}},
	}
	require.NoError(cfg.Validate())

	set := tokenauth.NewSourceSet(tokenauth.Options{})
	sources, err := collectProviderTokenSources(t.Context(), cfg, set)
	require.NoError(err)
	resolver := fakeGitHubIdentityResolver{byEnv: map[string]github.GitHubIdentity{
		"PAT_A":         {Key: github.IdentityKey{Host: "github.com", Principal: "user:123"}},
		"PAT_B":         {Key: github.IdentityKey{Host: "github.com", Principal: "user:123"}},
		"PAT_C":         {Key: github.IdentityKey{Host: "github.com", Principal: "user:456"}},
		"APP_WRITE_PAT": {Key: github.IdentityKey{Host: "github.com", Principal: "user:123"}},
	}}

	startup, err := buildProviderStartup(
		t.Context(), database, cfg, set, sources, defaultProviderFactories(), resolver,
	)
	require.NoError(err)

	routeA := startup.githubRoutes[tokenauth.Key{Platform: "github", Host: "github.com", Scope: "owner:org-a"}]
	routeB := startup.githubRoutes[tokenauth.Key{Platform: "github", Host: "github.com", Scope: "owner:org-b"}]
	routeC := startup.githubRoutes[tokenauth.Key{Platform: "github", Host: "github.com", Scope: "owner:org-c"}]
	routeD := startup.githubRoutes[tokenauth.Key{Platform: "github", Host: "github.com", Scope: "owner:org-d"}]

	assert.Equal("user:123", routeA.readIdentity.Principal)
	assert.Equal("user:123", routeB.readIdentity.Principal)
	assert.Equal("user:456", routeC.readIdentity.Principal)
	assert.Equal("installation:789", routeD.readIdentity.Principal)
	assert.Equal("user:123", routeD.writeIdentity.Principal)

	runtimeA := startup.githubIdentities[routeA.readIdentity.String()]
	runtimeB := startup.githubIdentities[routeB.readIdentity.String()]
	runtimeC := startup.githubIdentities[routeC.readIdentity.String()]
	runtimeDRead := startup.githubIdentities[routeD.readIdentity.String()]
	runtimeDWrite := startup.githubIdentities[routeD.writeIdentity.String()]
	require.NotNil(runtimeA)
	require.NotNil(runtimeB)
	require.NotNil(runtimeC)
	require.NotNil(runtimeDRead)
	require.NotNil(runtimeDWrite)
	assert.Same(runtimeA.budget, runtimeB.budget)
	assert.Same(runtimeA.rest, runtimeB.rest)
	assert.NotSame(runtimeA.budget, runtimeC.budget)
	assert.Same(runtimeA.budget, runtimeDWrite.budget)
	assert.NotSame(runtimeDRead.budget, runtimeDWrite.budget)
	writeBucket := github.RateBucketKey("github", "github.com", "user:123")
	assert.Same(runtimeDWrite.rest, startup.writeRateTrackers[writeBucket])
	assert.Same(runtimeDWrite.graphql, startup.writeGQLRateTrackers[writeBucket])
	assert.NotContains(startup.rateTrackers, github.RateBucketKey("github", "github.com", "host"))
	assert.NotContains(startup.budgets, github.RateBucketKey("github", "github.com", "host"))
	assert.Empty(startup.fetchers, "routed GitHub hosts must use route fetchers only")
	assert.NotSame(routeA.client, routeB.client)
	assert.Same(runtimeA.graphql, routeA.fetcher.RateTracker())
	assert.Same(runtimeB.graphql, routeB.fetcher.RateTracker())
	assert.Same(runtimeDRead.graphql, routeD.fetcher.RateTracker())
	router := startup.githubRouters["github.com"]
	require.NotNil(router)
	routedA, err := router.RouteForRepo("org-a", "one")
	require.NoError(err)
	routedB, err := router.RouteForRepo("org-b", "two")
	require.NoError(err)
	assert.Same(routeA.fetcher, routedA.Fetcher)
	assert.Same(routeB.fetcher, routedB.Fetcher)

	routed, ok := startup.githubClients["github.com"].(*github.RoutedClient)
	require.True(ok)
	assert.NotNil(routed)

	gitSource := startup.SourceForRepo("github", "github.com", "org-d", "four")
	require.NotNil(gitSource)
	gitToken, err := gitSource.Token(t.Context())
	require.NoError(err)
	assert.Equal("APP_WRITE_PAT-secret", gitToken,
		"managed Git must use the user PAT, never the App installation token")
}

func TestBuildProviderStartupRoutesUntrackedOwnerAndKeepsFallbackUnscoped(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	t.Setenv("ORG_A_PAT", "org-a-token")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{{
			Host: "github.com", Owner: "org-a", TokenEnv: "ORG_A_PAT",
		}},
	}
	require.NoError(cfg.Validate())
	set := tokenauth.NewSourceSet(tokenauth.Options{})
	sources, err := collectProviderTokenSources(t.Context(), cfg, set)
	require.NoError(err)

	startup, err := buildProviderStartup(
		t.Context(), database, cfg, set, sources, defaultProviderFactories(),
		fakeGitHubIdentityResolver{byEnv: map[string]github.GitHubIdentity{
			"ORG_A_PAT": {Key: github.IdentityKey{Host: "github.com", Principal: "user:123"}},
		}},
	)
	require.NoError(err)

	source := startup.SourceForRepo(
		"github", "github.com", "org-a", "first-repo",
	)
	require.NotNil(source)
	token, err := source.Token(t.Context())
	require.NoError(err)
	assert.Equal("org-a-token", token)

	nonGitHub := startup.SourceForRepo(
		"forgejo", "github.com", "org-a", "first-repo",
	)
	assert.NotContains(nonGitHub.Descriptor().CanonicalSourceString(), "ORG_A_PAT")

	fallback := startup.FallbackSource("github.com")
	require.NotNil(fallback)
	assert.NotContains(fallback.Descriptor().CanonicalSourceString(), "ORG_A_PAT")
}

func TestBuildProviderStartupAllowsAppOnlyReadRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos:    []config.Repo{{Owner: "org-app", Name: "one"}},
		GitHubApps: []config.GitHubAppConfig{{
			Host: "github.com", AppID: 7, PrivateKeyPath: "/keys/app.pem",
			InstallationID: 789, InstallationAccount: "org-app",
			RepositorySelection: "all",
		}},
	}
	require.NoError(cfg.Validate())
	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubApp: func(context.Context, tokenauth.Candidate) (string, time.Time, error) {
			return "app-token", time.Now().Add(time.Hour), nil
		},
	})
	sources, err := collectProviderTokenSources(t.Context(), cfg, set)
	require.NoError(err)

	startup, err := buildProviderStartup(
		t.Context(), database, cfg, set, sources, defaultProviderFactories(),
		fakeGitHubIdentityResolver{},
	)
	require.NoError(err)
	route := startup.githubRoutes[tokenauth.Key{
		Platform: "github", Host: "github.com", Scope: "owner:org-app",
	}]
	assert.Equal("installation:789", route.readIdentity.Principal)
	assert.Empty(route.writeIdentity.Principal)

	gitSource := startup.SourceForRepo("github", "github.com", "org-app", "one")
	require.NotNil(gitSource)
	_, err = gitSource.Token(t.Context())
	assert.ErrorIs(err, tokenauth.ErrMissingToken,
		"App-only routes must not expose installation tokens to managed Git")
}

func TestBuildProviderStartupReportsSafeGitHubIdentityResolutionFailure(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	t.Setenv("ORG_A_TOKEN", "super-secret-token")
	cfg := &config.Config{
		SyncInterval: "5m",
		Host:         "127.0.0.1",
		Port:         8091,
		BasePath:     "/",
		Activity: config.Activity{
			ViewMode: "flat", TimeRange: "7d",
		},
		Repos: []config.Repo{{Owner: "org-a", Name: "one"}},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{{
			Host: "github.com", Owner: "org-a", TokenEnv: "ORG_A_TOKEN",
		}},
	}
	require.NoError(cfg.Validate())
	set := tokenauth.NewSourceSet(tokenauth.Options{})
	sources, err := collectProviderTokenSources(t.Context(), cfg, set)
	require.NoError(err)

	_, err = buildProviderStartup(
		t.Context(), database, cfg, set, sources, defaultProviderFactories(),
		fakeGitHubIdentityResolver{err: map[string]error{
			"ORG_A_TOKEN": fmt.Errorf("identity lookup failed"),
		}},
	)
	require.Error(err)
	assert.Contains(t, err.Error(), "env:ORG_A_TOKEN")
	assert.NotContains(t, err.Error(), "super-secret-token")
}
