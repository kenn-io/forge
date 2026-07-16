package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/tokenauth"
)

func TestHTTPIdentityResolverResolvesStableUserID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/user", r.URL.Path)
		assert.Equal(t, "Bearer token-a", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"id":123,"login":"maintainer"}`))
	}))
	t.Cleanup(server.Close)

	source := identityTestSource(t, "TOKEN_A", "token-a")
	resolver := HTTPIdentityResolver{
		Endpoint: func(string) string { return server.URL + "/user" },
		NewHTTPClient: func(_ string, source tokenauth.Source) *http.Client {
			return identityHTTPClient(server.URL, source)
		},
	}

	got, err := resolver.ResolvePAT(t.Context(), "github.com", source)
	require.NoError(t, err)
	assert.Equal(t, IdentityKey{Host: "github.com", Principal: "user:123"}, got.Key)
	assert.Equal(t, "maintainer", got.Login)
}

func TestHTTPIdentityResolverUsesPATSideOfAppChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"id":456,"login":"writer"}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("USER_PAT", "user-token")

	source := tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "github", Host: "github.com", Scope: "owner:acme"},
		Candidates: []tokenauth.Candidate{
			{
				Kind: tokenauth.SourceKindGitHubApp, Host: "github.com",
				AppID: 1, InstallationID: 2, InstallationAccount: "acme",
			},
			{Kind: tokenauth.SourceKindEnv, EnvName: "USER_PAT"},
		},
	}, tokenauth.Options{GitHubApp: func(
		context.Context, tokenauth.Candidate,
	) (string, time.Time, error) {
		return "installation-token", time.Now().Add(time.Hour), nil
	}})
	resolver := HTTPIdentityResolver{
		Endpoint: func(string) string { return server.URL + "/user" },
		NewHTTPClient: func(_ string, source tokenauth.Source) *http.Client {
			return identityHTTPClient(server.URL, source)
		},
	}

	got, err := resolver.ResolvePAT(
		tokenauth.WithGitHubOwner(t.Context(), "acme"), "github.com", source,
	)
	require.NoError(t, err)
	assert.Equal(t, "user:456", got.Key.Principal)
}

func TestHTTPIdentityResolverRejectsInvalidResponsesSafely(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       string
	}{
		{name: "non success", statusCode: http.StatusUnauthorized, body: `{"message":"bad credentials"}`, want: "status 401"},
		{name: "missing id", statusCode: http.StatusOK, body: `{"login":"nobody"}`, want: "positive numeric user id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)
			source := identityTestSource(t, "SECRET_TOKEN", "ghp_sentinel_secret")
			resolver := HTTPIdentityResolver{
				Endpoint: func(string) string { return server.URL + "/user" },
				NewHTTPClient: func(_ string, source tokenauth.Source) *http.Client {
					return identityHTTPClient(server.URL, source)
				},
			}

			_, err := resolver.ResolvePAT(t.Context(), "github.com", source)
			require.ErrorContains(t, err, tc.want)
			assert.NotContains(t, err.Error(), "ghp_sentinel_secret")
		})
	}
}

func TestInstallationIdentity(t *testing.T) {
	got := InstallationIdentity("GitHub.COM", 789)

	assert.Equal(t, IdentityKey{
		Host:      "github.com",
		Principal: "installation:789",
	}, got.Key)
	assert.Equal(t, "GitHub App installation 789", got.Label())
}

func identityTestSource(t *testing.T, envName, token string) tokenauth.Source {
	t.Helper()
	t.Setenv(envName, token)
	return tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "github", Host: "github.com", Scope: "owner:test"},
		Candidates: []tokenauth.Candidate{{
			Kind: tokenauth.SourceKindEnv, EnvName: envName,
		}},
	}, tokenauth.Options{})
}

func identityHTTPClient(origin string, source tokenauth.Source) *http.Client {
	return &http.Client{Transport: tokenauth.AuthTransport{
		Source:        source,
		Base:          http.DefaultTransport,
		SetHeader:     tokenauth.BearerAuthHeader,
		AllowedOrigin: origin,
	}}
}
