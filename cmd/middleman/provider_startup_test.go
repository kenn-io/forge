package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
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
