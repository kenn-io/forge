package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/tokenauth"
)

type mutableRuntimeAuthTokenSource struct {
	mu          sync.Mutex
	token       string
	invalidates int
}

func newMutableRuntimeAuthTokenSource(token string) *mutableRuntimeAuthTokenSource {
	return &mutableRuntimeAuthTokenSource{token: token}
}

func (s *mutableRuntimeAuthTokenSource) Token(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token, nil
}

func (s *mutableRuntimeAuthTokenSource) Invalidate() {
	s.mu.Lock()
	s.token = "second-token"
	s.invalidates++
	s.mu.Unlock()
}

func (s *mutableRuntimeAuthTokenSource) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "github", Host: "github.example.com"},
		Candidates: []tokenauth.Candidate{{
			Kind:    tokenauth.SourceKindEnv,
			EnvName: "MIDDLEMAN_TEST_TOKEN",
		}},
	}
}

func (s *mutableRuntimeAuthTokenSource) SetToken(token string) {
	s.mu.Lock()
	s.token = token
	s.mu.Unlock()
}

func (s *mutableRuntimeAuthTokenSource) Invalidates() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.invalidates
}

func withGitHubAuthTLSServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	originalTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
	})

	return strings.TrimPrefix(srv.URL, "https://")
}

func TestNewClientReadsRotatedTokenOnNextRequest(t *testing.T) {
	assert := assert.New(t)
	source := newMutableRuntimeAuthTokenSource("first-token")
	var authorizations []string
	host := withGitHubAuthTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"acme"}`))
	}))
	client, err := NewClient(source, host, nil, nil)
	require.NoError(t, err)

	first, err := client.GetUser(t.Context(), "acme")
	require.NoError(t, err)
	source.SetToken("second-token")
	second, err := client.GetUser(t.Context(), "acme")
	require.NoError(t, err)

	assert.Equal("acme", first.GetLogin())
	assert.Equal("acme", second.GetLogin())
	assert.Equal([]string{"Bearer first-token", "Bearer second-token"}, authorizations)
	assert.Equal(0, source.Invalidates())
}

func TestNewClientRetriesUnauthorizedWithFreshToken(t *testing.T) {
	assert := assert.New(t)
	source := newMutableRuntimeAuthTokenSource("first-token")
	var authorizations []string
	host := withGitHubAuthTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if len(authorizations) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"bad credentials"}`))
			return
		}
		_, _ = w.Write([]byte(`{"login":"acme"}`))
	}))
	client, err := NewClient(source, host, nil, nil)
	require.NoError(t, err)

	user, err := client.GetUser(t.Context(), "acme")
	require.NoError(t, err)

	assert.Equal("acme", user.GetLogin())
	assert.Equal([]string{"Bearer first-token", "Bearer second-token"}, authorizations)
	assert.Equal(1, source.Invalidates())
}

func TestNewClientDoesNotRetryForbidden(t *testing.T) {
	assert := assert.New(t)
	source := newMutableRuntimeAuthTokenSource("first-token")
	calls := 0
	host := withGitHubAuthTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"forbidden"}`))
	}))
	client, err := NewClient(source, host, nil, nil)
	require.NoError(t, err)

	_, err = client.GetUser(t.Context(), "acme")

	require.Error(t, err)
	assert.Equal(1, calls)
	assert.Equal(0, source.Invalidates())
}

type runtimeAuthViewerQuery struct {
	Viewer struct {
		Login githubv4.String
	}
}

func queryRuntimeAuthViewer(t *testing.T, fetcher *GraphQLFetcher) (string, error) {
	t.Helper()
	var query runtimeAuthViewerQuery
	err := fetcher.client.Query(t.Context(), &query, nil)
	return string(query.Viewer.Login), err
}

func TestNewGraphQLFetcherReadsRotatedTokenOnNextRequest(t *testing.T) {
	assert := assert.New(t)
	source := newMutableRuntimeAuthTokenSource("first-token")
	var authorizations []string
	host := withGitHubAuthTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"octocat"}}}`))
	}))
	fetcher := NewGraphQLFetcher(source, host, nil, nil)

	first, err := queryRuntimeAuthViewer(t, fetcher)
	require.NoError(t, err)
	source.SetToken("second-token")
	second, err := queryRuntimeAuthViewer(t, fetcher)
	require.NoError(t, err)

	assert.Equal("octocat", first)
	assert.Equal("octocat", second)
	assert.Equal([]string{"Bearer first-token", "Bearer second-token"}, authorizations)
	assert.Equal(0, source.Invalidates())
}

func TestNewGraphQLFetcherRetriesUnauthorizedWithFreshToken(t *testing.T) {
	assert := assert.New(t)
	source := newMutableRuntimeAuthTokenSource("first-token")
	var authorizations []string
	host := withGitHubAuthTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if len(authorizations) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"bad credentials"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"octocat"}}}`))
	}))
	fetcher := NewGraphQLFetcher(source, host, nil, nil)

	login, err := queryRuntimeAuthViewer(t, fetcher)
	require.NoError(t, err)

	assert.Equal("octocat", login)
	assert.Equal([]string{"Bearer first-token", "Bearer second-token"}, authorizations)
	assert.Equal(1, source.Invalidates())
}

func TestNewGraphQLFetcherDoesNotRetryForbidden(t *testing.T) {
	assert := assert.New(t)
	source := newMutableRuntimeAuthTokenSource("first-token")
	calls := 0
	host := withGitHubAuthTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"forbidden"}`))
	}))
	fetcher := NewGraphQLFetcher(source, host, nil, nil)

	_, err := queryRuntimeAuthViewer(t, fetcher)

	require.Error(t, err)
	assert.Equal(1, calls)
	assert.Equal(0, source.Invalidates())
}
