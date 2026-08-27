package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platform"
)

func TestDefaultProviderFactoryPassesGitLabSharedSyncBudget(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("factory-token", r.Header.Get("Private-Token"))
		assert.Equal("/api/v4/projects/42", r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"path":"project","path_with_namespace":"group/project","name":"Project"}`))
	}))
	defer server.Close()

	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	host := strings.TrimPrefix(server.URL, "https://")
	budget := github.NewSyncBudget(100)
	factory := defaultProviderFactories()[string(platform.KindGitLab)]
	built, err := factory(providerFactoryInput{
		host: host,
		tokenSource: mainTestTokenSource(
			t, string(platform.KindGitLab), host, "GITLAB_FACTORY_TOKEN", "factory-token",
		),
		budget: budget,
	})
	require.NoError(err)
	reader, ok := built.provider.(platform.RepositoryReader)
	require.True(ok)

	_, err = reader.GetRepository(github.WithSyncBudget(t.Context()), platform.RepoRef{
		Platform: platform.KindGitLab, Host: host,
		Owner: "group", Name: "project", RepoPath: "group/project", PlatformID: 42,
	})
	require.NoError(err)
	assert.Equal(1, budget.Spent())
}

func TestDefaultProviderFactoryUsesGiteaExplicitBaseURL(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("token factory-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v1/version":
			_, _ = w.Write([]byte(`{"version":"1.26.0"}`))
		case "/api/v1/repos/owner/repo":
			_, _ = w.Write([]byte(`{
				"id":42,"name":"repo","full_name":"owner/repo",
				"clone_url":"http://gitea.test/owner/repo.git",
				"owner":{"login":"owner"}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	factory := defaultProviderFactories()[string(platform.KindGitea)]
	built, err := factory(providerFactoryInput{
		host:          "gitea.test",
		baseURL:       server.URL,
		allowInsecure: true,
		tokenSource: mainTestTokenSource(
			t, string(platform.KindGitea), "gitea.test", "GITEA_FACTORY_TOKEN", "factory-token",
		),
	})
	require.NoError(err)
	reader, ok := built.provider.(platform.RepositoryReader)
	require.True(ok)

	repo, err := reader.GetRepository(t.Context(), platform.RepoRef{
		Platform: platform.KindGitea, Host: "gitea.test", Owner: "owner", Name: "repo",
	})
	require.NoError(err)
	assert.Equal("http://gitea.test/owner/repo.git", repo.CloneURL)
}
