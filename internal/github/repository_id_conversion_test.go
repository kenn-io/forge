package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

// markGitHubRepositoryPending puts a seeded GitHub repository in the state the
// integer-identity migration leaves it: inactive, keyed only by its node ID.
func markGitHubRepositoryPending(t *testing.T, database *db.DB, repoID int64, nodeID string) {
	t.Helper()
	_, err := database.WriteDB().ExecContext(t.Context(), `
		UPDATE forge_repos
		SET platform_repo_id = 0, github_node_id = ?, lifecycle_state = 'inactive'
		WHERE id = ?`, nodeID, repoID,
	)
	require.NoError(t, err)
}

func storedGitHubRepositoryIdentity(
	t *testing.T, database *db.DB, repoID int64,
) (int64, string, string) {
	t.Helper()
	var platformRepoID int64
	var nodeID, lifecycle string
	require.NoError(t, database.ReadDB().QueryRowContext(t.Context(), `
		SELECT platform_repo_id, github_node_id, lifecycle_state
		FROM forge_repos WHERE id = ?`, repoID,
	).Scan(&platformRepoID, &nodeID, &lifecycle))
	return platformRepoID, nodeID, lifecycle
}

func TestConvertPendingGitHubRepositoriesResolvesNodeIDs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	nodeIDs := map[string]string{"widget": "R_found", "gone": "R_gone", "flaky": "R_flaky"}
	repoIDs := map[string]int64{}
	for name := range nodeIDs {
		repoID, err := reposeed.Seed(
			t.Context(), database, db.GitHubRepoIdentity("github.com", "acme", name),
		)
		require.NoError(err)
		repoIDs[name] = repoID
	}
	for name, nodeID := range nodeIDs {
		markGitHubRepositoryPending(t, database, repoIDs[name], nodeID)
	}
	foundID, goneID, flakyID := repoIDs["widget"], repoIDs["gone"], repoIDs["flaky"]

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&request)) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Variables["id"] {
		case "R_found":
			_, _ = w.Write([]byte(`{"data":{"node":{"databaseId":1001}}}`))
		case "R_gone":
			_, _ = w.Write([]byte(`{"data":{"node":null},"errors":[{"type":"NOT_FOUND","path":["node"],"message":"Could not resolve to a node with the global id of 'R_gone'"}]}`))
		default:
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		}
	}))
	t.Cleanup(server.Close)

	syncer := NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(server.URL, server.Client()), nil,
		),
	})

	syncer.convertPendingGitHubRepositories(t.Context())

	platformRepoID, nodeID, lifecycle := storedGitHubRepositoryIdentity(t, database, foundID)
	assert.Equal(int64(1001), platformRepoID, "GitHub's integer ID replaces the node ID")
	assert.Empty(nodeID)
	assert.Equal(string(db.RepositoryLifecycleInactive), lifecycle,
		"conversion alone does not reactivate; the next observation does")

	platformRepoID, nodeID, lifecycle = storedGitHubRepositoryIdentity(t, database, goneID)
	assert.Zero(platformRepoID)
	assert.Empty(nodeID, "an unresolvable node stops blocking observations for its host")
	assert.Equal(string(db.RepositoryLifecycleInactive), lifecycle)

	pending, err := database.ListPendingGitHubRepositories(t.Context())
	require.NoError(err)
	require.Len(pending, 1, "a failed lookup leaves the repository pending for the next pass")
	assert.Equal(flakyID, pending[0].RepoID)
	assert.Equal("R_flaky", pending[0].NodeID)
}
