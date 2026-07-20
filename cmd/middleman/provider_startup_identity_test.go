package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/tokenauth"
)

type fakeGitHubIdentityResolver struct {
	byEnv map[string]github.GitHubIdentity
	err   map[string]error
	calls *atomic.Int32
}

func (r fakeGitHubIdentityResolver) ResolvePAT(
	ctx context.Context, host string, source tokenauth.Source,
) (github.GitHubIdentity, string, error) {
	if r.calls != nil {
		r.calls.Add(1)
	}
	desc := source.Descriptor()
	for _, candidate := range desc.Candidates {
		if candidate.Kind != tokenauth.SourceKindEnv {
			continue
		}
		if err := r.err[candidate.EnvName]; err != nil {
			return github.GitHubIdentity{}, "", err
		}
		if identity, ok := r.byEnv[candidate.EnvName]; ok {
			token, err := source.Token(tokenauth.WithMutationAuth(ctx))
			return identity, token, err
		}
	}
	return github.GitHubIdentity{}, "", fmt.Errorf(
		"no fake identity for %s on %s: %w",
		desc.SafeString(), host, tokenauth.ErrMissingToken,
	)
}

func TestBuildProviderStartupRetainsVerifiedPATForExactRoutes(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	t.Setenv("SHARED_PAT", "shared-secret")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos: []config.Repo{
			{Owner: "org-a", Name: "one", TokenEnv: "SHARED_PAT"},
			{Owner: "org-a", Name: "two", TokenEnv: "SHARED_PAT"},
		},
	}
	require.NoError(cfg.Validate())
	set := tokenauth.NewSourceSet(tokenauth.Options{})
	sources, err := collectProviderTokenSources(t.Context(), cfg, set)
	require.NoError(err)
	var identityLookups atomic.Int32
	startup, err := buildProviderStartup(
		t.Context(), database, cfg, set, sources, defaultProviderFactories(),
		fakeGitHubIdentityResolver{
			byEnv: map[string]github.GitHubIdentity{
				"SHARED_PAT": {
					Key: github.IdentityKey{Host: "github.com", Principal: "user:123"},
				},
			},
			calls: &identityLookups,
		},
	)
	require.NoError(err)
	startupLookups := identityLookups.Load()
	assert.Positive(startupLookups)

	for _, repo := range []string{"one", "two"} {
		source := startup.SourceForRepo("github", "github.com", "org-a", repo)
		require.NotNil(source)
		token, tokenErr := source.Token(t.Context())
		require.NoError(tokenErr)
		assert.Equal("shared-secret", token)
	}
	assert.Equal(startupLookups, identityLookups.Load(),
		"the startup-verified PAT must not be resolved again on each exact route's first request")
}

type tokenGitHubIdentityResolver map[string]github.GitHubIdentity

func (r tokenGitHubIdentityResolver) ResolvePAT(
	ctx context.Context, host string, source tokenauth.Source,
) (github.GitHubIdentity, string, error) {
	token, err := source.Token(tokenauth.WithMutationAuth(ctx))
	if err != nil {
		return github.GitHubIdentity{}, "", err
	}
	if identity, ok := r[token]; ok {
		return identity, token, nil
	}
	return github.GitHubIdentity{}, "", fmt.Errorf(
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

func TestBuildProviderStartupSkipsImplicitOptionalFallbackIdentityLookup(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	t.Setenv("ORG_A_PAT", "org-a-token")
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "implicit-fallback-token")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos:    []config.Repo{{Owner: "org-a", Name: "one"}},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{{
			Host: "github.com", Owner: "org-a", TokenEnv: "ORG_A_PAT",
		}},
	}
	require.NoError(cfg.Validate())
	set := tokenauth.NewSourceSet(tokenauth.Options{})
	sources, err := collectProviderTokenSources(t.Context(), cfg, set)
	require.NoError(err)
	var identityLookups atomic.Int32

	startup, err := buildProviderStartup(
		t.Context(), database, cfg, set, sources, defaultProviderFactories(),
		fakeGitHubIdentityResolver{
			byEnv: map[string]github.GitHubIdentity{
				"ORG_A_PAT": {
					Key: github.IdentityKey{Host: "github.com", Principal: "user:123"},
				},
			},
			calls: &identityLookups,
		},
	)
	require.NoError(err)
	assert.Equal(int32(1), identityLookups.Load())
	assert.NotContains(startup.githubRoutes, tokenauth.Key{
		Platform: "github", Host: "github.com",
	})
}

func TestProductionStartupRoutesTwoOwnersThroughSyncAndMutationAPI(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("ORG_A_PAT", "token-a")
	t.Setenv("ORG_B_PAT", "token-b")

	var authMu sync.Mutex
	authByCall := make(map[string]string)
	record := func(key string, r *http.Request) {
		authMu.Lock()
		defer authMu.Unlock()
		if _, exists := authByCall[key]; !exists {
			authByCall[key] = r.Header.Get("Authorization")
		}
	}
	repoIDs := map[string]int64{"org-a/one": 101, "org-b/two": 202}
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "2000000000")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/user":
			record("identity:"+r.Header.Get("Authorization"), r)
			switch r.Header.Get("Authorization") {
			case "Bearer token-a":
				_, _ = io.WriteString(w, `{"id":11,"login":"owner-a-user"}`)
			case "Bearer token-b":
				_, _ = io.WriteString(w, `{"id":22,"login":"owner-b-user"}`)
			default:
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"message":"bad credentials"}`)
			}
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/rate_limit":
			_, _ = io.WriteString(w, `{"resources":{"core":{"limit":5000,"remaining":4999,"reset":2000000000}}}`)
			return
		case r.Method == http.MethodPost && r.URL.Path == "/api/graphql":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"force REST fallback"}`)
			return
		}

		if !strings.HasPrefix(r.URL.Path, "/api/v3/repos/") {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v3/repos/"), "/")
		if len(parts) < 2 {
			http.NotFound(w, r)
			return
		}
		fullName := parts[0] + "/" + parts[1]
		repoID, ok := repoIDs[fullName]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 2 && r.Method == http.MethodGet {
			record("sync:repo:"+fullName, r)
			_, _ = fmt.Fprintf(w, `{
				"id":%d,"node_id":%q,"name":%q,"full_name":%q,
				"owner":{"login":%q},"default_branch":"main",
				"html_url":"https://example.invalid/%s",
				"clone_url":"https://example.invalid/%s.git",
				"permissions":{"push":true}
			}`, repoID, fmt.Sprintf("R_%d", repoID), parts[1], fullName, parts[0], fullName, fullName)
			return
		}
		if len(parts) == 3 && parts[2] == "pulls" && r.Method == http.MethodGet {
			record("sync:pulls:"+fullName, r)
			_, _ = fmt.Fprintf(w, `[{
				"id":%d,"number":1,"state":"open","title":%q,
				"html_url":"https://example.invalid/%s/pull/1",
				"user":{"login":"author"},"draft":false,
				"created_at":"2026-07-17T12:00:00Z","updated_at":"2026-07-17T12:00:00Z",
				"head":{"sha":%q,"ref":"feature","repo":{"id":%d,"full_name":%q}},
				"base":{"sha":%q,"ref":"main","repo":{"id":%d,"full_name":%q}}
			}]`, repoID*10+1, fullName+" PR", fullName, "head-"+parts[0], repoID, fullName, "base-"+parts[0], repoID, fullName)
			return
		}
		if len(parts) == 5 && parts[2] == "issues" && parts[3] == "1" &&
			parts[4] == "comments" && r.Method == http.MethodPost {
			record("write:comment:"+fullName, r)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{
				"id":%d,"body":"routed comment","user":{"login":"maintainer"},
				"created_at":"2026-07-17T12:01:00Z",
				"html_url":"https://example.invalid/%s/pull/1#issuecomment-1"
			}`, repoID*100, fullName)
			return
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(api.Close)
	originalTransport := http.DefaultTransport
	http.DefaultTransport = api.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	host := strings.TrimPrefix(api.URL, "https://")

	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos: []config.Repo{
			{Platform: "github", PlatformHost: host, Owner: "org-a", Name: "one"},
			{Platform: "github", PlatformHost: host, Owner: "org-b", Name: "two"},
		},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{
			{Host: host, Owner: "org-a", TokenEnv: "ORG_A_PAT"},
			{Host: host, Owner: "org-b", TokenEnv: "ORG_B_PAT"},
		},
	}
	require.NoError(cfg.Validate())
	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubCLI: func(context.Context, string) (string, error) {
			return "", tokenauth.ErrMissingToken
		},
	})
	sources, err := collectProviderTokenSources(t.Context(), cfg, set)
	require.NoError(err)
	database := dbtest.Open(t)
	startup, err := buildProviderStartup(
		t.Context(), database, cfg, set, sources, defaultProviderFactories(),
		github.HTTPIdentityResolver{},
	)
	require.NoError(err)

	repos := []github.RepoRef{
		{Platform: "github", PlatformHost: host, Owner: "org-a", Name: "one"},
		{Platform: "github", PlatformHost: host, Owner: "org-b", Name: "two"},
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
	middleman := httptest.NewServer(srv)
	t.Cleanup(middleman.Close)

	syncReq, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, middleman.URL+"/api/v1/sync", nil,
	)
	require.NoError(err)
	syncReq.Header.Set("Content-Type", "application/json")
	syncResp, err := middleman.Client().Do(syncReq)
	require.NoError(err)
	syncBody, err := io.ReadAll(syncResp.Body)
	require.NoError(err)
	require.NoError(syncResp.Body.Close())
	require.Equal(http.StatusAccepted, syncResp.StatusCode, string(syncBody))

	require.Eventually(func() bool {
		for _, repo := range repos {
			row, rowErr := database.GetMergeRequest(
				t.Context(), "github", host, repo.Owner, repo.Name, 1,
			)
			if rowErr != nil || row == nil || row.Title != repo.Owner+"/"+repo.Name+" PR" {
				return false
			}
		}
		return true
	}, 5*time.Second, 20*time.Millisecond)

	for _, repo := range repos {
		url := fmt.Sprintf(
			"%s/api/v1/host/%s/pulls/gh/%s/%s/1/comments",
			middleman.URL, host, repo.Owner, repo.Name,
		)
		commentReq, reqErr := http.NewRequestWithContext(
			t.Context(), http.MethodPost, url,
			strings.NewReader(`{"body":"routed comment"}`),
		)
		require.NoError(reqErr)
		commentReq.Header.Set("Content-Type", "application/json")
		commentResp, reqErr := middleman.Client().Do(commentReq)
		require.NoError(reqErr)
		commentBody, readErr := io.ReadAll(commentResp.Body)
		require.NoError(readErr)
		require.NoError(commentResp.Body.Close())
		require.Equal(http.StatusCreated, commentResp.StatusCode, string(commentBody))
	}

	authMu.Lock()
	defer authMu.Unlock()
	assert.Equal("Bearer token-a", authByCall["sync:pulls:org-a/one"])
	assert.Equal("Bearer token-b", authByCall["sync:pulls:org-b/two"])
	assert.Equal("Bearer token-a", authByCall["write:comment:org-a/one"])
	assert.Equal("Bearer token-b", authByCall["write:comment:org-b/two"])
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
	var requestCount atomic.Int32
	auth := make(chan string, 4)
	gitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if username, password, ok := r.BasicAuth(); ok {
			select {
			case auth <- username + "\x00" + password:
			default:
			}
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer gitServer.Close()
	host := gitServer.Listener.Addr().String()
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Platforms: []config.PlatformConfig{{
			Type: "github", Host: host, TokenEnv: "LATE_PAT",
		}},
		Repos: []config.Repo{{
			Platform: "github", PlatformHost: host, Owner: "org-app", Name: "one",
		}},
		GitHubApps: []config.GitHubAppConfig{{
			Host: host, AppID: 7, PrivateKeyPath: "/keys/app.pem",
			InstallationID: 789, InstallationAccount: "org-app",
			RepositorySelection: "all",
		}},
	}
	require.NoError(cfg.Validate())
	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubCLI: func(context.Context, string) (string, error) {
			return "", tokenauth.ErrMissingToken
		},
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
		Platform: "github", Host: host, Scope: "owner:org-app",
	}]
	assert.Equal("installation:789", route.readIdentity.Principal)
	assert.Empty(route.writeIdentity.Principal)

	t.Setenv("LATE_PAT", "appeared-after-startup")
	manager := gitclone.New(t.TempDir(), &startup)
	_, err = manager.RunGitForRepo(
		t.Context(), "github", host, "org-app", "one", "",
		"ls-remote", gitServer.URL+"/org-app/one.git",
	)
	require.ErrorContains(err, github.ErrMissingWriteIdentity.Error(),
		"managed Git must wait for restart to bind a newly available PAT identity")
	assert.Zero(requestCount.Load(), "managed Git must not contact the remote before restart")

	restartedSet := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubCLI: func(context.Context, string) (string, error) {
			return "", tokenauth.ErrMissingToken
		},
		GitHubApp: func(context.Context, tokenauth.Candidate) (string, time.Time, error) {
			return "app-token", time.Now().Add(time.Hour), nil
		},
	})
	restartedSources, err := collectProviderTokenSources(t.Context(), cfg, restartedSet)
	require.NoError(err)
	restarted, err := buildProviderStartup(
		t.Context(), database, cfg, restartedSet, restartedSources,
		defaultProviderFactories(), tokenGitHubIdentityResolver{
			"appeared-after-startup": {
				Key: github.IdentityKey{Host: host, Principal: "user:123"},
			},
		},
	)
	require.NoError(err)

	restartedManager := gitclone.New(t.TempDir(), &restarted)
	_, err = restartedManager.RunGitForRepo(
		t.Context(), "github", host, "org-app", "one", "",
		"ls-remote", gitServer.URL+"/org-app/one.git",
	)
	require.Error(err, "the controlled endpoint rejects the authenticated fetch")
	assert.NotContains(err.Error(), github.ErrMissingWriteIdentity.Error())
	assert.Positive(requestCount.Load())
	select {
	case got := <-auth:
		assert.Equal("x-access-token\x00appeared-after-startup", got)
	default:
		require.Fail("managed Git did not send Basic authentication after restart")
	}
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
