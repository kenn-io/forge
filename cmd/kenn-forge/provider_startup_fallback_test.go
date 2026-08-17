package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/githubapp"
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
		{name: "canceled", err: context.Canceled, want: false},
		{name: "unauthorized", err: &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusUnauthorized}}, want: false},
		{name: "permanent", err: errors.New("invalid configuration"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTransientGitHubStartupError(tt.err))
		})
	}
}

func TestBuildProviderStartupWithFallbackKeepsRoutesAfterGitHubUnavailable(t *testing.T) {
	assert := assert.New(t)
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
	require.NoError(t, cfg.Validate())

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
	require.NoError(t, err)

	router := startup.githubRouters["github.com"]
	require.NotNil(t, router)
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
		require.NoError(t, routeErr)
		assert.Equal(wantIdentity, route.ReadIdentity)
		assert.Equal(wantIdentity, route.WriteIdentity)

		source := startup.SourceForRepo("github", "github.com", tc.owner, tc.name)
		require.NotNil(t, source)
		gotToken, tokenErr := source.Token(t.Context())
		require.NoError(t, tokenErr)
		assert.Equal(tc.token, gotToken)
	}
}

func TestBuildProviderStartupWithFallbackKeepsPermanentErrorsFatal(t *testing.T) {
	t.Setenv("ORG_PAT", "org-token")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos:    []config.Repo{{Owner: "org", Name: "repo"}},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{{
			Host: "github.com", Owner: "org", TokenEnv: "ORG_PAT",
		}},
	}
	require.NoError(t, cfg.Validate())

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
	require.Error(t, err)
	var responseErr *gh.ErrorResponse
	require.ErrorAs(t, err, &responseErr)
	assert.Equal(t, http.StatusUnauthorized, responseErr.Response.StatusCode)
}

func TestBuildProviderStartupWithFallbackDoesNotRemintAppToken(t *testing.T) {
	assert := assert.New(t)
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
	require.NoError(t, cfg.Validate())
	var mints atomic.Int32
	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubApp: func(context.Context, tokenauth.Candidate) (string, time.Time, error) {
			mints.Add(1)
			return "", time.Time{}, &githubapp.StatusError{
				StatusCode: http.StatusServiceUnavailable,
				Body:       "GitHub is unavailable",
			}
		},
		GitHubCLI: func(context.Context, string) (string, error) {
			return "", tokenauth.ErrMissingToken
		},
	})

	startup, err := buildProviderStartupWithFallback(
		t.Context(), dbtest.Open(t), cfg, set,
		defaultProviderFactories(), fakeGitHubIdentityResolver{},
	)
	require.NoError(t, err)
	assert.Equal(int32(1), mints.Load())

	router := startup.githubRouters["github.com"]
	require.NotNil(t, router)
	route, err := router.RouteForRepo("org", "repo")
	require.NoError(t, err)
	assert.Equal(
		github.IdentityKey{Host: "github.com", Principal: "installation:789"},
		route.ReadIdentity,
	)
	assert.Equal(github.IdentityKey{}, route.WriteIdentity)
}
