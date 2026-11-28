package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/web"
)

func TestGitHubTeamFixture(t *testing.T) {
	assets, err := web.Assets()
	require.NoError(t, err)
	state, err := buildAppState(t.Context(), assets, appOptions{roborevEndpoint: defaultRoborevEndpoint})
	require.NoError(t, err)
	t.Cleanup(state.close)

	get := func(path string, target any) {
		t.Helper()
		response := httptest.NewRecorder()
		state.handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1"+path, nil))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), target))
	}
	var teams []struct{ Slug, Name string }
	get("/api/v1/repos/github-teams?platform_host=github.com&org=acme", &teams)
	require.Len(t, teams, 1)
	assert.Equal(t, "platform", teams[0].Slug)
	assert.Equal(t, "Platform", teams[0].Name)

	var repos []struct {
		ID   int64  `json:"platform_repo_id"`
		Path string `json:"repo_path"`
	}
	get("/api/v1/repos/github-teams/platform/repositories?platform_host=github.com&org=acme", &repos)
	require.Len(t, repos, 2)
	assert.Equal(t, []string{"acme/widgets", "acme/tools"}, []string{repos[0].Path, repos[1].Path})
	assert.Equal(t, testutil.FixtureRepoID("acme", "widgets"), repos[0].ID)
	assert.Equal(t, testutil.FixtureRepoID("acme", "tools"), repos[1].ID)

	client := newE2EWorkflowClient(&testutil.FixtureClient{})
	providerRepos, err := client.ListTeamRepositories(t.Context(), "acme", "platform")
	require.NoError(t, err)
	for _, repo := range providerRepos {
		assert.True(t, repo.Permissions.GetPull())
		assert.False(t, repo.Permissions.GetPush())
	}
	unknown, err := client.ListOrgTeams(t.Context(), "other")
	require.NoError(t, err)
	assert.Empty(t, unknown)
}
