package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ghshim"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/testutil"
)

func TestGHShimSpokeUsesLocalDataThenExistingHubRead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hub, hubDB := setupTestServer(t)
	seedPR(t, hubDB, "acme", "widget", 8)
	var hubReads atomic.Int64
	hubHTTP := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/federation/events" || r.URL.Path == "/api/v1/sync/status" {
			http.Error(w, "background polling unavailable", http.StatusServiceUnavailable)
			return
		}
		hubReads.Add(1)
		assert.Equal(http.MethodGet, r.Method)
		assert.Contains(r.URL.Path, "/pulls/github/acme/widget/8")
		r.Header.Del("Authorization")
		hub.ServeHTTP(w, r)
	}))
	t.Cleanup(hubHTTP.Close)
	spoke, localDB := newFederatedProviderNodeForTest(t, proxyTestNodeID, hubHTTP.URL, hubHTTP.Client())
	spoke.pullAPI.ApplyConfig(pullapi.ConfigSnapshot{Repositories: []config.Repo{{Owner: "acme", Name: "widget"}}})
	seedPR(t, localDB, "acme", "widget", 7)
	repo, err := localDB.GetRepoByIdentity(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(localDB.UpdateRepoSyncCompleted(t.Context(), repo.ID, time.Now().UTC(), ""))
	for _, tc := range []struct {
		command  string
		number   int
		output   string
		hubReads int64
	}{
		{"view", 7, `{"number":7}`, 0},
		{"list", 0, `[{"number":7}]`, 0},
		{"view", 8, `{"number":8}`, 1},
	} {
		response := testutil.DoJSON(t, spoke, http.MethodPost, "/api/v1/gh/query", ghshim.Query{Command: tc.command, Host: "github.com", Owner: "acme", Repo: "widget", Number: tc.number, State: "open", Limit: 30, Fields: []string{"number"}})
		require.Equal(http.StatusOK, response.Code, response.Body.String())
		var result struct {
			Handled bool
			Output  string
			Reason  string
		}
		require.NoError(json.Unmarshal(response.Body.Bytes(), &result))
		assert.True(result.Handled, result.Reason)
		assert.JSONEq(tc.output, result.Output)
		assert.Equal(tc.hubReads, hubReads.Load())
	}
	spoke.pullAPI.ApplyConfig(pullapi.ConfigSnapshot{Repositories: []config.Repo{{Owner: "acme", Name: "other"}}})
	response := testutil.DoJSON(t, spoke, http.MethodPost, "/api/v1/gh/query", ghshim.Query{Command: "view", Host: "github.com", Owner: "acme", Repo: "widget", Number: 7, State: "open", Limit: 30, Fields: []string{"number"}})
	assert.Contains(response.Body.String(), `"reason":"untracked"`)
	assert.Equal(int64(1), hubReads.Load())
}

func TestGHShimHubRequiresConfiguredProviderIdentity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		providerID string
		handled    bool
		reason     string
	}{
		{"configured identity", "repo-acme-widget", true, "served"},
		{"reused route", "repo-original-widget", false, "untracked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			configured := defaultTestRepos[0]
			configured.PlatformExternalID = tc.providerID
			hub, database := setupTestServerWithRepos(t, &mockGH{}, []ghclient.RepoRef{configured})
			seedPR(t, database, "acme", "widget", 7)
			response := testutil.DoJSON(t, hub, http.MethodPost, "/api/v1/gh/query", ghshim.Query{Command: "view", Host: "github.com", Owner: "acme", Repo: "widget", Number: 7, State: "open", Limit: 30, Fields: []string{"number"}})
			require.Equal(http.StatusOK, response.Code, response.Body.String())
			var result struct {
				Handled bool
				Reason  string
			}
			require.NoError(json.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(tc.handled, result.Handled)
			assert.Equal(tc.reason, result.Reason)
		})
	}
}

func TestGHShimTreatsStoredMergeTimeAsMerged(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hub, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 7)
	mergedAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	seedPR(t, database, "acme", "widget", 8, func(pr *db.MergeRequest) { pr.MergedAt = &mergedAt })
	repo, err := database.GetRepoByIdentity(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateRepoSyncCompleted(t.Context(), repo.ID, time.Now().UTC(), ""))
	for _, tc := range []struct {
		command string
		number  int
		fields  []string
		output  string
	}{
		{"list", 0, []string{"number"}, `[{"number":7}]`},
		{"view", 8, []string{"state"}, `{"state":"MERGED"}`},
	} {
		response := testutil.DoJSON(t, hub, http.MethodPost, "/api/v1/gh/query", ghshim.Query{Command: tc.command, Host: "github.com", Owner: "acme", Repo: "widget", Number: tc.number, State: "open", Limit: 30, Fields: tc.fields})
		require.Equal(http.StatusOK, response.Code, response.Body.String())
		var result struct {
			Handled bool
			Output  string
			Reason  string
		}
		require.NoError(json.Unmarshal(response.Body.Bytes(), &result))
		require.True(result.Handled, result.Reason)
		assert.JSONEq(tc.output, result.Output)
	}
}
