package github

import (
	"context"
	"fmt"
	"testing"
	"time"

	gh "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/tokenauth"
)

func TestHTTPIdentityResolverResolvesStableUserID(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	source := identityTestSource(t, "TOKEN_A", "token-a")
	resolver := HTTPIdentityResolver{
		Lookup: func(ctx context.Context, host string, gotSource tokenauth.Source) (*gh.User, error) {
			assert.Equal("github.com", host)
			token, err := gotSource.Token(ctx)
			require.NoError(err)
			assert.Equal("token-a", token)
			return &gh.User{ID: new(int64(123)), Login: new("maintainer")}, nil
		},
	}

	got, token, err := resolver.ResolvePAT(t.Context(), "github.com", source)
	require.NoError(err)
	assert.Equal(IdentityKey{Host: "github.com", Principal: "user:123"}, got.Key)
	assert.Equal("maintainer", got.Login)
	assert.Equal("token-a", token)
}

func TestHTTPIdentityResolverUsesPATSideOfAppChain(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
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
		Lookup: func(ctx context.Context, _ string, source tokenauth.Source) (*gh.User, error) {
			token, err := source.Token(ctx)
			require.NoError(err)
			assert.Equal("user-token", token)
			return &gh.User{ID: new(int64(456)), Login: new("writer")}, nil
		},
	}

	got, token, err := resolver.ResolvePAT(
		tokenauth.WithGitHubOwner(t.Context(), "acme"), "github.com", source,
	)
	require.NoError(err)
	assert.Equal("user:456", got.Key.Principal)
	assert.Equal("user-token", token)
}

func TestHTTPIdentityResolverRejectsInvalidResponsesSafely(t *testing.T) {
	tests := []struct {
		name string
		user *gh.User
		err  error
		want string
	}{
		{name: "lookup error", err: fmt.Errorf("status 401"), want: "status 401"},
		{name: "missing id", user: &gh.User{Login: new("nobody")}, want: "positive numeric user id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source := identityTestSource(t, "SECRET_TOKEN", "ghp_sentinel_secret")
			resolver := HTTPIdentityResolver{
				Lookup: func(context.Context, string, tokenauth.Source) (*gh.User, error) {
					return tc.user, tc.err
				},
			}

			_, _, err := resolver.ResolvePAT(t.Context(), "github.com", source)
			require.ErrorContains(t, err, tc.want)
			assert.NotContains(t, err.Error(), "ghp_sentinel_secret")
		})
	}
}

func TestHTTPIdentityResolverBoundsLookupDuration(t *testing.T) {
	resolver := HTTPIdentityResolver{
		Timeout: 20 * time.Millisecond,
		Lookup: func(ctx context.Context, _ string, _ tokenauth.Source) (*gh.User, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	started := time.Now()
	_, _, err := resolver.ResolvePAT(
		t.Context(), "github.com", identityTestSource(t, "TIMEOUT_TOKEN", "token"),
	)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(started), time.Second)
}

func TestIdentityBoundSourceRejectsCrossUserRotation(t *testing.T) {
	require := require.New(t)
	source := &mutableIdentityTestSource{token: "same-user-token"}
	resolver := identityResolverFunc(func(_ context.Context, _ string, source tokenauth.Source) (GitHubIdentity, string, error) {
		token, err := source.Token(t.Context())
		require.NoError(err)
		principal := "user:123"
		if token == "different-user-token" {
			principal = "user:456"
		}
		return GitHubIdentity{Key: IdentityKey{Host: "github.com", Principal: principal}}, token, nil
	})
	bound := BindSourceIdentity(
		source, "github.com", IdentityKey{Host: "github.com", Principal: "user:123"},
		"same-user-token", resolver,
	)

	_, err := bound.Token(tokenauth.WithMutationAuth(t.Context()))
	require.NoError(err)
	source.token = "different-user-token"
	bound.Invalidate("same-user-token")
	_, err = bound.Token(tokenauth.WithMutationAuth(t.Context()))
	assert.ErrorIs(t, err, ErrIdentityChanged)
}

type identityResolverFunc func(context.Context, string, tokenauth.Source) (GitHubIdentity, string, error)

func (f identityResolverFunc) ResolvePAT(ctx context.Context, host string, source tokenauth.Source) (GitHubIdentity, string, error) {
	return f(ctx, host, source)
}

type mutableIdentityTestSource struct{ token string }

func (s *mutableIdentityTestSource) Token(context.Context) (string, error) { return s.token, nil }
func (s *mutableIdentityTestSource) Invalidate(string)                     {}
func (s *mutableIdentityTestSource) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{Key: tokenauth.Key{Platform: "github", Host: "github.com"}}
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
