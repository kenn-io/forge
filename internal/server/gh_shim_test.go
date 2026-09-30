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
	"go.kenn.io/forge/internal/testutil/reposeed"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/platform"
)

func TestGHShimSpokeUsesLocalDataThenExistingHubRead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hub, hubDB, _ := setupTestServer(t)
	serverfake.SeedPR(t, hubDB, "acme", "widget", 8)
	var hubReads atomic.Int64
	hubHTTP := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/federation/events" || r.URL.Path == "/api/v1/sync/status" {
			http.Error(w, "background polling unavailable", http.StatusServiceUnavailable)
			return
		}
		hubReads.Add(1)
		assert.Equal(http.MethodGet, r.Method)
		assert.Contains(r.URL.Path, "/pulls/github/acme/widget/")
		r.Header.Del("Authorization")
		hub.ServeHTTP(w, r)
	}))
	t.Cleanup(hubHTTP.Close)
	spoke, localDB := newFederatedProviderNodeForTest(t, proxyTestNodeID, hubHTTP.URL, hubHTTP.Client())
	spoke.pullAPI.ApplyConfig(pullapi.ConfigSnapshot{Repositories: []config.Repo{{Owner: "acme", Name: "widget"}}})
	serverfake.SeedPR(t, localDB, "acme", "widget", 7)
	repo, err := localDB.GetRepoByIdentity(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
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
	// Reuse the hub's route for a different repository: the spoke must not
	// serve that repository's pull request as its tracked one.
	replacement := db.GitHubRepoIdentity("github.com", "acme", "widget")
	widgetID, ok := serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget").Key.ID()
	require.True(ok)
	replacement.Key = platform.RepositoryIDKey(widgetID + 1)
	replacementID, err := reposeed.Seed(t.Context(), hubDB, replacement)
	require.NoError(err)
	serverfake.SeedPR(t, hubDB, "acme", "widget", 9, func(pr *db.MergeRequest) { pr.RepoID = replacementID })
	_, err = reposeed.Seed(t.Context(), hubDB, replacement)
	require.NoError(err)
	response := testutil.DoJSON(t, spoke, http.MethodPost, "/api/v1/gh/query", ghshim.Query{Command: "view", Host: "github.com", Owner: "acme", Repo: "widget", Number: 9, State: "open", Limit: 30, Fields: []string{"number"}})
	assert.Contains(response.Body.String(), `"reason":"data_unavailable"`)
	assert.Equal(int64(2), hubReads.Load())
	spoke.pullAPI.ApplyConfig(pullapi.ConfigSnapshot{Repositories: []config.Repo{{Owner: "acme", Name: "other"}}})
	response = testutil.DoJSON(t, spoke, http.MethodPost, "/api/v1/gh/query", ghshim.Query{Command: "view", Host: "github.com", Owner: "acme", Repo: "widget", Number: 7, State: "open", Limit: 30, Fields: []string{"number"}})
	assert.Contains(response.Body.String(), `"reason":"untracked"`)
	assert.Equal(int64(2), hubReads.Load())
}

func TestGHShimHubRequiresConfiguredProviderIdentity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		providerID platform.RepositoryKey
		handled    bool
		reason     string
	}{
		{"configured identity", serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget").Key, true, "served"},
		{"reused route", replacedWidgetKey(t), false, "untracked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			configured := serverfake.DefaultTestRepos[0]
			configured.Key = tc.providerID
			hub, database, _ := setupTestServerWithRepos(t, &serverfake.MockGH{}, []ghclient.RepoRef{configured})
			serverfake.SeedPR(t, database, "acme", "widget", 7)
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
	hub, database, _ := setupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 7)
	mergedAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	serverfake.SeedPR(t, database, "acme", "widget", 8, func(pr *db.MergeRequest) { pr.MergedAt = &mergedAt })
	repo, err := database.GetRepoByIdentity(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
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

// Archive inventory only discovers pull requests; item sync loads their rows
// later. Historical lists are served only after the initial full archive has
// loaded every discovered pull request.
func TestGHShimServesHistoricalListsOnlyAfterFullArchiveLoads(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hub, database, _ := setupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 7)
	closedAt := time.Now().UTC().Add(-time.Hour)
	serverfake.SeedPR(t, database, "acme", "widget", 8, func(pr *db.MergeRequest) {
		pr.State = db.MergeRequestStateClosed
		pr.ClosedAt = &closedAt
	})
	repo, err := database.GetRepoByIdentity(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateRepoSyncCompleted(t.Context(), repo.ID, time.Now().UTC(), ""))
	require.NoError(database.EnsureDiscoveryArchives(t.Context(), []int64{repo.ID}, time.Now().UTC()))
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE forge_archive_repo_scans SET status = 'complete'
		WHERE repo_id = ? AND scan = 'merge_request_inventory'`, repo.ID)
	require.NoError(err)

	listClosed := func() (bool, string, string) {
		response := testutil.DoJSON(t, hub, http.MethodPost, "/api/v1/gh/query", ghshim.Query{Command: "list", Host: "github.com", Owner: "acme", Repo: "widget", State: "closed", Limit: 30, Fields: []string{"number"}})
		require.Equal(http.StatusOK, response.Code, response.Body.String())
		var result struct {
			Handled bool
			Output  string
			Reason  string
		}
		require.NoError(json.Unmarshal(response.Body.Bytes(), &result))
		return result.Handled, result.Output, result.Reason
	}

	handled, _, reason := listClosed()
	assert.False(handled, "discovery-only archives never load older closed pull requests")
	assert.Equal("data_unavailable", reason)

	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE forge_archive_repos SET collection_mode = 'full' WHERE repo_id = ?`, repo.ID)
	require.NoError(err)
	handled, _, reason = listClosed()
	assert.False(handled, "discovered pull requests may still be waiting to load")
	assert.Equal("data_unavailable", reason)

	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE forge_archive_repos SET initial_completed_at = ? WHERE repo_id = ?`,
		time.Now().UTC().Format(time.RFC3339), repo.ID)
	require.NoError(err)
	handled, output, reason := listClosed()
	require.True(handled, reason)
	assert.JSONEq(`[{"number":8}]`, output)
}

// replacedWidgetKey is the key of a different repository at the widget route.
func replacedWidgetKey(t *testing.T) platform.RepositoryKey {
	t.Helper()
	widgetID, ok := serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget").Key.ID()
	require.True(t, ok)
	return platform.RepositoryIDKey(widgetID + 1)
}
