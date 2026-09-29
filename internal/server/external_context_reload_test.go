package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/externalcontext"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/httpapi"
)

func TestConfigReloadPublishesExternalContextSources(t *testing.T) {
	require := require.New(t)
	srv, _, _ := setupTestServerWithConfigContent(t, validReloadConfig, &mockGH{})
	require.Empty(srv.externalContext.Sources())
	executable, err := os.Executable()
	require.NoError(err)
	reloadPath := filepath.Join(t.TempDir(), "reload.toml")
	srv.cfgPath = reloadPath
	writeConfigToml(t, reloadPath, validReloadConfig+fmt.Sprintf(`
[[external_context]]
id = "checks"
name = "Private checks"
command = [%q]
`, executable))
	event := srv.applyConfigChange(t.Context())
	require.True(event.Valid, event.Error)
	require.Equal([]externalcontext.ExternalContextSourceInfo{{ID: "checks", Name: "Private checks"}}, srv.externalContext.Sources())
	writeConfigToml(t, reloadPath, malformedTomlConfig)
	require.False(srv.applyConfigChange(t.Context()).Valid)
	require.Len(srv.externalContext.Sources(), 1)
	writeConfigToml(t, reloadPath, validReloadConfig)
	require.True(srv.applyConfigChange(t.Context()).Valid)
	require.Empty(srv.externalContext.Sources())
}

func TestExternalContextUsesHubSyncedPull(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 42, func(pull *db.MergeRequest) {
		pull.PlatformHeadSHA = "local-stale-head"
		pull.PlatformBaseSHA = "local-stale-base"
	})
	srv.providerSource = &hubProviderSource{client: providerPlaneClientFunc(func(
		_ context.Context, scope federationauth.Scope, request *http.Request,
	) (*http.Response, error) {
		require.Equal(federationauth.ScopeProviderRead, scope)
		require.Equal(http.MethodGet, request.Method)
		require.Equal("/api/v1/host/github.com/pulls/github/acme/widget/42", request.URL.Path)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"repo":{"provider":"github","platform_host":"github.com","platform_repo_id":7001,"repo_path":"acme/widget","owner":"acme","name":"widget"},
				"merge_request":{"Number":42,"URL":"https://github.com/acme/widget/pull/42","State":"merged"},
				"platform_host":"github.com","platform_head_sha":"hub-synced-head","platform_base_sha":"hub-synced-base"
			}`)),
			Request: request,
		}, nil
	})}
	input := repoNumberInput{Provider: "github", PlatformHost: "github.com", Owner: "acme", Name: "widget", Number: 42}
	pull, err := srv.externalContextPull(t.Context(), input, 7001)
	require.NoError(err)
	assert.Equal(externalcontext.PullRequest{
		Provider: "github", PlatformHost: "github.com", PlatformRepoID: 7001, RepoPath: "acme/widget",
		Number: 42, URL: "https://github.com/acme/widget/pull/42", State: "merged", HeadSHA: "hub-synced-head", BaseSHA: "hub-synced-base",
	}, pull)

	_, err = srv.externalContextPull(t.Context(), input, 7002)
	var problem *httpapi.ProblemError
	require.ErrorAs(err, &problem)
	assert.Equal(http.StatusConflict, problem.Status)
	assert.Equal(httpapi.CodeConflict, problem.Code)
}
