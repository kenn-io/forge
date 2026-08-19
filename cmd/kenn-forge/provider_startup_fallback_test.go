package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/githubapp"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/tokenauth"
)

func TestTransientGitHubStartupErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "network", err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("offline")}, want: true},
		{name: "primary rate limit", err: &gh.RateLimitError{}, want: true},
		{name: "secondary rate limit", err: &gh.AbuseRateLimitError{}, want: true},
		{name: "request timeout", err: &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusRequestTimeout}}, want: true},
		{name: "too many requests", err: &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusTooManyRequests}}, want: true},
		{name: "app rate limit", err: &githubapp.StatusError{StatusCode: http.StatusTooManyRequests}, want: true},
		{
			name: "app headerless secondary rate limit",
			err: &githubapp.StatusError{
				StatusCode: http.StatusForbidden,
				Body: `{"message":"You have exceeded a secondary rate limit.",` +
					`"documentation_url":"https://docs.github.com/rest/using-the-rest-api/` +
					`rate-limits-for-the-rest-api#about-secondary-rate-limits"}`,
			},
			want: true,
		},
		{name: "app forbidden", err: &githubapp.StatusError{StatusCode: http.StatusForbidden}, want: false},
		{
			name: "app unrelated structured forbidden",
			err: &githubapp.StatusError{
				StatusCode: http.StatusForbidden,
				Body: `{"message":"Resource not accessible by integration",` +
					`"documentation_url":"https://docs.github.com/rest/apps/apps"}`,
			},
			want: false,
		},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "unauthorized", err: &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusUnauthorized}}, want: false},
		{name: "permanent", err: errors.New("invalid configuration"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTransientGitHubStartupError(tt.err))
		})
	}

	for _, tt := range []struct {
		name   string
		header http.Header
		want   bool
	}{
		{
			name:   "app primary rate limit",
			header: http.Header{"X-RateLimit-Remaining": {"0"}},
			want:   true,
		},
		{
			name:   "app secondary rate limit",
			header: http.Header{"Retry-After": {"60"}},
			want:   true,
		},
		{
			name:   "app forbidden with remaining budget",
			header: http.Header{"X-RateLimit-Remaining": {"4999"}},
			want:   false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTransientGitHubStartupError(
				githubAppStatusError(t, http.StatusForbidden, tt.header),
			))
		})
	}
}

func githubAppStatusError(t *testing.T, status int, header http.Header) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for name, values := range header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		http.Error(w, http.StatusText(status), status)
	}))
	t.Cleanup(server.Close)

	_, err := githubapp.NewClientWithBase(server.URL).CoreRateLimit(t.Context(), "app-token")
	require.Error(t, err)
	var statusErr *githubapp.StatusError
	require.ErrorAs(t, err, &statusErr)
	for name, values := range header {
		assert.Equal(t, values, statusErr.Header.Values(name))
	}
	return err
}

func TestBuildProviderStartupWithFallbackKeepsRoutesAfterGitHubUnavailable(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("ORG_A_PAT", "org-a-token")
	t.Setenv("ORG_B_PAT", "org-b-token")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos: []config.Repo{
			{Owner: "org-a", Name: "one"},
			{Owner: "org-b", Name: "two"},
		},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{
			{Host: "github.com", Owner: "org-a", TokenEnv: "ORG_A_PAT"},
			{Host: "github.com", Owner: "org-b", TokenEnv: "ORG_B_PAT"},
		},
	}
	require.NoError(cfg.Validate())

	startup, err := buildProviderStartupWithFallback(
		t.Context(), dbtest.Open(t), cfg,
		tokenauth.NewSourceSet(tokenauth.Options{}),
		defaultProviderFactories(),
		fakeGitHubIdentityResolver{err: map[string]error{
			"ORG_A_PAT": &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusServiceUnavailable},
				Message:  "GitHub is unavailable",
			},
			"ORG_B_PAT": &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusServiceUnavailable},
				Message:  "GitHub is unavailable",
			},
		}},
	)
	require.NoError(err)
	assert.Contains(
		startup.degradedProviderHosts,
		providerHostKey(string(platform.KindGitHub), "github.com"),
	)

	router := startup.githubRouters["github.com"]
	require.NotNil(router)
	wantIdentity := github.IdentityKey{Host: "github.com", Principal: "host"}
	for _, tc := range []struct {
		owner string
		name  string
		token string
	}{
		{owner: "org-a", name: "one", token: "org-a-token"},
		{owner: "org-b", name: "two", token: "org-b-token"},
	} {
		route, routeErr := router.RouteForRepo(tc.owner, tc.name)
		require.NoError(routeErr)
		assert.Equal(wantIdentity, route.ReadIdentity)
		assert.Equal(wantIdentity, route.WriteIdentity)

		source := startup.SourceForRepo("github", "github.com", tc.owner, tc.name)
		require.NotNil(source)
		gotToken, tokenErr := source.Token(t.Context())
		require.NoError(tokenErr)
		assert.Equal(tc.token, gotToken)
	}
}

func TestBuildProviderStartupWithFallbackBindsLiveSourcesToPrimaryResolver(t *testing.T) {
	require := require.New(t)
	t.Setenv("ORG_PAT", "startup-token")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos:    []config.Repo{{Owner: "org", Name: "repo"}},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{{
			Host: "github.com", Owner: "org", TokenEnv: "ORG_PAT",
		}},
	}
	require.NoError(cfg.Validate())
	upstreamErr := &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusServiceUnavailable},
		Message:  "GitHub is unavailable",
	}

	startup, err := buildProviderStartupWithFallback(
		t.Context(), dbtest.Open(t), cfg,
		tokenauth.NewSourceSet(tokenauth.Options{}),
		defaultProviderFactories(),
		fakeGitHubIdentityResolver{err: map[string]error{"ORG_PAT": upstreamErr}},
	)
	require.NoError(err)

	// A changed live token must be revalidated by the primary resolver. The
	// startup-only fallback must not remain attached to the source and mutate
	// degradedProviderHosts after concurrent sync has begun.
	t.Setenv("ORG_PAT", "live-token")
	source := startup.SourceForRepo("github", "github.com", "org", "repo")
	require.NotNil(source)
	_, err = source.Token(t.Context())
	require.ErrorIs(err, upstreamErr)
}

func TestBuildProviderStartupWithFallbackKeepsPermanentErrorsFatal(t *testing.T) {
	require := require.New(t)
	t.Setenv("ORG_PAT", "org-token")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos:    []config.Repo{{Owner: "org", Name: "repo"}},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{{
			Host: "github.com", Owner: "org", TokenEnv: "ORG_PAT",
		}},
	}
	require.NoError(cfg.Validate())

	_, err := buildProviderStartupWithFallback(
		t.Context(), dbtest.Open(t), cfg,
		tokenauth.NewSourceSet(tokenauth.Options{}),
		defaultProviderFactories(),
		fakeGitHubIdentityResolver{err: map[string]error{
			"ORG_PAT": &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusUnauthorized},
				Message:  "Bad credentials",
			},
		}},
	)
	require.Error(err)
	var responseErr *gh.ErrorResponse
	require.ErrorAs(err, &responseErr)
	assert.Equal(t, http.StatusUnauthorized, responseErr.Response.StatusCode)
}

func TestBuildProviderStartupWithFallbackPermanentErrorsOverrideTransientRoutes(t *testing.T) {
	require := require.New(t)
	t.Setenv("TRANSIENT_PAT", "transient-token")
	t.Setenv("PERMANENT_PAT", "permanent-token")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos: []config.Repo{
			{Owner: "transient-org", Name: "one"},
			{Owner: "permanent-org", Name: "two"},
		},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{
			{Host: "github.com", Owner: "transient-org", TokenEnv: "TRANSIENT_PAT"},
			{Host: "github.com", Owner: "permanent-org", TokenEnv: "PERMANENT_PAT"},
		},
	}
	require.NoError(cfg.Validate())
	var calls atomic.Int32

	_, err := buildProviderStartupWithFallback(
		t.Context(), dbtest.Open(t), cfg,
		tokenauth.NewSourceSet(tokenauth.Options{}),
		defaultProviderFactories(),
		fakeGitHubIdentityResolver{
			err: map[string]error{
				"TRANSIENT_PAT": &gh.ErrorResponse{
					Response: &http.Response{StatusCode: http.StatusServiceUnavailable},
					Message:  "GitHub is unavailable",
				},
				"PERMANENT_PAT": &gh.ErrorResponse{
					Response: &http.Response{StatusCode: http.StatusUnauthorized},
					Message:  "Bad credentials",
				},
			},
			calls: &calls,
		},
	)
	require.Error(err)
	var responseErr *gh.ErrorResponse
	require.ErrorAs(err, &responseErr)
	assert.Equal(t, http.StatusUnauthorized, responseErr.Response.StatusCode)
	assert.Equal(t, int32(2), calls.Load())
}

func TestBuildProviderStartupWithFallbackHandlesAppRateLimitWithoutRemint(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos:    []config.Repo{{Owner: "org", Name: "repo"}},
		GitHubApps: []config.GitHubAppConfig{{
			Host: "github.com", AppID: 7, PrivateKeyPath: "/keys/app.pem",
			InstallationID: 789, InstallationAccount: "org",
			RepositorySelection: "all",
		}},
	}
	require.NoError(cfg.Validate())
	var mints atomic.Int32
	rateLimitErr := githubAppStatusError(t, http.StatusForbidden, http.Header{
		"X-RateLimit-Remaining": {"0"},
	})
	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubApp: func(context.Context, tokenauth.Candidate) (string, time.Time, error) {
			mints.Add(1)
			return "", time.Time{}, rateLimitErr
		},
		GitHubCLI: func(context.Context, string) (string, error) {
			return "", tokenauth.ErrMissingToken
		},
	})

	startup, err := buildProviderStartupWithFallback(
		t.Context(), dbtest.Open(t), cfg, set,
		defaultProviderFactories(), fakeGitHubIdentityResolver{},
	)
	require.NoError(err)
	assert.Equal(int32(1), mints.Load())

	router := startup.githubRouters["github.com"]
	require.NotNil(router)
	route, err := router.RouteForRepo("org", "repo")
	require.NoError(err)
	assert.Equal(
		github.IdentityKey{Host: "github.com", Principal: "installation:789"},
		route.ReadIdentity,
	)
	assert.Equal(github.IdentityKey{}, route.WriteIdentity)
}
