package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server"
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

type tokenGitHubIdentityResolver map[string]github.GitHubIdentity

func (r tokenGitHubIdentityResolver) ResolvePAT(
	ctx context.Context, host string, source tokenauth.Source,
) (github.GitHubIdentity, error) {
	token, err := source.Token(tokenauth.WithMutationAuth(ctx))
	if err != nil {
		return github.GitHubIdentity{}, err
	}
	if identity, ok := r[token]; ok {
		return identity, nil
	}
	return github.GitHubIdentity{}, fmt.Errorf(
		"no fake identity for token on %s: %w", host, tokenauth.ErrMissingToken,
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
	t.Setenv("DEFAULT_PAT", "fallback-token")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity:       config.Activity{ViewMode: "flat", TimeRange: "7d"},
		GitHubTokenEnv: "DEFAULT_PAT",
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{{
			Host: "github.com", Owner: "org-a", TokenEnv: "ORG_A_PAT",
		}},
		GitHubApps: []config.GitHubAppConfig{{
			Host: "github.com", AppID: 7, PrivateKeyPath: "/keys/app.pem",
			InstallationID: 789, InstallationAccount: "org-a",
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
		fakeGitHubIdentityResolver{byEnv: map[string]github.GitHubIdentity{
			"ORG_A_PAT":   {Key: github.IdentityKey{Host: "github.com", Principal: "user:123"}},
			"DEFAULT_PAT": {Key: github.IdentityKey{Host: "github.com", Principal: "user:999"}},
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
	assert.NotContains(fallback.Descriptor().CanonicalSourceString(), "github_app")
	fallbackToken, err := fallback.Token(t.Context())
	require.NoError(err)
	assert.Equal("fallback-token", fallbackToken)

	router := startup.githubRouters["github.com"]
	require.NotNil(router)
	fallbackRoute, err := router.RouteForRepo("unconfigured", "repo")
	require.NoError(err)
	assert.Equal("user:999", fallbackRoute.ReadIdentity.Principal)
}

func TestProductionStartupRoutesExposeRotatedPATThroughRepoAPI(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	t.Setenv("ACME_PAT", "writer-a")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos: []config.Repo{
			{Owner: "acme", Name: "covered"},
			{Owner: "acme", Name: "uncovered"},
		},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{{
			Host: "github.com", Owner: "acme", TokenEnv: "ACME_PAT",
		}},
		GitHubApps: []config.GitHubAppConfig{{
			Host: "github.com", AppID: 7, PrivateKeyPath: "/keys/app.pem",
			InstallationID: 789, InstallationAccount: "acme",
			RepositorySelection: "selected", SelectedRepos: []string{"acme/covered"},
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
	resolver := tokenGitHubIdentityResolver{
		"writer-a": {Key: github.IdentityKey{Host: "github.com", Principal: "user:123"}},
		"writer-b": {Key: github.IdentityKey{Host: "github.com", Principal: "user:456"}},
	}
	startup, err := buildProviderStartup(
		t.Context(), database, cfg, set, sources, defaultProviderFactories(), resolver,
	)
	require.NoError(err)

	coveredRoute := startup.githubRoutes[tokenauth.Key{
		Platform: "github", Host: "github.com", Scope: "repo:acme/covered",
	}]
	uncoveredRoute := startup.githubRoutes[tokenauth.Key{
		Platform: "github", Host: "github.com", Scope: "owner:acme",
	}]
	assert.Equal("installation:789", coveredRoute.readIdentity.Principal)
	assert.Equal("user:123", coveredRoute.writeIdentity.Principal)
	assert.Equal("user:123", uncoveredRoute.readIdentity.Principal)
	assert.Equal("user:123", uncoveredRoute.writeIdentity.Principal)

	repos := []github.RepoRef{
		{Owner: "acme", Name: "covered", PlatformHost: "github.com"},
		{Owner: "acme", Name: "uncovered", PlatformHost: "github.com"},
	}
	for _, repo := range repos {
		_, err := database.UpsertRepo(
			t.Context(), db.GitHubRepoIdentity(repo.PlatformHost, repo.Owner, repo.Name),
		)
		require.NoError(err)
	}
	syncer := github.NewSyncerWithRegistry(
		startup.registry, database, nil, repos, time.Minute,
		startup.rateTrackers, startup.budgets,
	)
	t.Cleanup(syncer.Stop)
	syncer.SetGitHubRouters(startup.githubRouters)
	syncer.SetWriteRateTrackers(startup.writeRateTrackers)
	syncer.SetWriteGQLRateTrackers(startup.writeGQLRateTrackers)
	srv := server.New(database, syncer, nil, "/", cfg, server.ServerOptions{
		TokenSources: set, HostCheckAllowLoopbackAnyPort: true,
	})
	httpServer := httptest.NewServer(srv)
	t.Cleanup(httpServer.Close)

	// The bounded routes keep user:123 until restart, so rotating the live PAT
	// to user:456 must disable writes for both the App-covered exact route and
	// the PAT-backed owner route through the real repository API.
	t.Setenv("ACME_PAT", "writer-b")
	for _, name := range []string{"covered", "uncovered"} {
		resp, err := http.Get(httpServer.URL + "/api/v1/repo/github/acme/" + name)
		require.NoError(err)
		var body struct {
			Operations struct {
				AddComment struct {
					Code string `json:"code"`
				} `json:"add_comment"`
			} `json:"operations"`
		}
		require.NoError(json.NewDecoder(resp.Body).Decode(&body))
		require.NoError(resp.Body.Close())
		assert.Equal(http.StatusOK, resp.StatusCode)
		assert.Equal("write_credential_error", body.Operations.AddComment.Code)
	}
}

func TestBuildProviderStartupAllowsAppOnlyReadRouteButRequiresRestartForManagedGit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	t.Setenv("LATE_PAT", "")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity:       config.Activity{ViewMode: "flat", TimeRange: "7d"},
		GitHubTokenEnv: "LATE_PAT",
		Repos:          []config.Repo{{Owner: "org-app", Name: "one"}},
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

	t.Setenv("LATE_PAT", "appeared-after-startup")
	gitSource := startup.SourceForRepo("github", "github.com", "org-app", "one")
	require.NotNil(gitSource)
	_, err = gitSource.Token(t.Context())
	assert.ErrorIs(err, github.ErrMissingWriteIdentity,
		"managed Git must wait for restart to bind a newly available PAT identity")
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
