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
	"go.kenn.io/forge/internal/ghshim"
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
