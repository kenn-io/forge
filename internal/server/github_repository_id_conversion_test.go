package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func TestFederationResolveGitHubRepositoryIDReportsHostWithoutCredential(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	rr := testutil.DoJSON(t, srv, http.MethodPost,
		"/api/v1/federation/provider/github-repository-id",
		GitHubRepositoryIDRequest{
			PlatformHost: "github.example.com", Owner: "acme", Name: "widget", NodeID: "R_node",
		},
	)

	require.Equal(http.StatusNotFound, rr.Code, rr.Body.String())
	require.Contains(rr.Body.String(), "no github credential covers this repository")
}

func TestSpokeConvertsPendingGitHubRepositoriesThroughHub(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	nodeIDs := map[string]string{"widget": "R_found", "gone": "R_gone", "flaky": "R_flaky"}
	repoIDs := map[string]int64{}
	for name := range nodeIDs {
		repoID, err := reposeed.Seed(ctx, database, db.GitHubRepoIdentity("github.com", "acme", name))
		require.NoError(err)
		repoIDs[name] = repoID
	}
	for name, nodeID := range nodeIDs {
		_, err := database.WriteDB().ExecContext(ctx, `
			UPDATE forge_repos
			SET platform_repo_id = 0, github_node_id = ?, lifecycle_state = 'inactive'
			WHERE id = ?`, nodeID, repoIDs[name],
		)
		require.NoError(err)
	}

	client := providerPlaneClientFunc(func(
		_ context.Context, scope federationauth.Scope, request *http.Request,
	) (*http.Response, error) {
		assert.Equal(federationauth.ScopeProviderRead, scope)
		assert.Equal("/api/v1/federation/provider/github-repository-id", request.URL.Path)
		var body GitHubRepositoryIDRequest
		require.NoError(json.NewDecoder(request.Body).Decode(&body))
		status, payload := http.StatusOK, `{"platform_repo_id":0,"found":false}`
		switch body.NodeID {
		case "R_found":
			payload = `{"platform_repo_id":1001,"found":true}`
		case "R_flaky":
			status = http.StatusBadGateway
			payload = `{"title":"Bad Gateway","status":502,"code":"upstreamError"}`
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader([]byte(payload))),
			Request:    request,
		}, nil
	})
	source := &hubProviderSource{client: client, db: database}

	source.convertPendingGitHubRepositories(ctx)

	stored := func(repoID int64) (int64, string) {
		var platformRepoID int64
		var nodeID string
		require.NoError(database.ReadDB().QueryRowContext(ctx, `
			SELECT platform_repo_id, github_node_id FROM forge_repos WHERE id = ?`, repoID,
		).Scan(&platformRepoID, &nodeID))
		return platformRepoID, nodeID
	}
	platformRepoID, nodeID := stored(repoIDs["widget"])
	assert.Equal(int64(1001), platformRepoID, "the hub's integer ID replaces the node ID")
	assert.Empty(nodeID)
	platformRepoID, nodeID = stored(repoIDs["gone"])
	assert.Zero(platformRepoID)
	assert.Empty(nodeID, "an unresolvable node stops blocking observations for its host")

	pending, err := database.ListPendingGitHubRepositories(ctx)
	require.NoError(err)
	require.Len(pending, 1, "a failed hub lookup leaves the row pending for the next connection")
	assert.Equal(repoIDs["flaky"], pending[0].RepoID)
}
