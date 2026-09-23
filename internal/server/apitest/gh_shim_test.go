package apitest

import (
	"testing"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
)

func TestGHShimHydratesTrackedPullAndDelegatesOtherQueries(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	srv, database, provider, _ := setupTestServerWithFixtureClient(t)
	seedPR(t, database, "acme", "widget", 7)
	provider.PRs["acme/widget"] = []*gh.PullRequest{{Number: new(7), Title: new("Provider title"), State: new("open")}}
	client := setupTestClient(t, srv)
	body := generated.QueryGhBody{Command: "view", Host: "github.com", Owner: "acme", Repo: "widget", Number: 7, State: "open", Limit: 30, Fields: []string{"number", "title", "state"}}
	response, err := client.HTTP.QueryGhWithResponse(t.Context(), &generated.QueryGhRequestOptions{Body: &body})
	require.NoError(err)
	require.NotNil(response.JSON200)
	assert := assert.New(t)
	assert.True(response.JSON200.Handled)
	assert.JSONEq(`{"number":7,"state":"OPEN","title":"Provider title"}`, response.JSON200.Output)
	// Removing the provider response proves the next query uses the daemon cache.
	provider.PRs["acme/widget"] = nil
	response, err = client.HTTP.QueryGhWithResponse(t.Context(), &generated.QueryGhRequestOptions{Body: &body})
	require.NoError(err)
	require.NotNil(response.JSON200)
	assert.True(response.JSON200.Handled)
	assert.Contains(response.JSON200.Output, "Provider title")
	for _, tc := range []struct {
		name   string
		change func(*generated.QueryGhBody)
		reason string
	}{
		{"untracked", func(q *generated.QueryGhBody) { q.Repo = "other" }, "untracked"},
		{"different host", func(q *generated.QueryGhBody) { q.Host = "code.example" }, "untracked"},
		{"unknown field", func(q *generated.QueryGhBody) { q.Fields = []string{"reviews"} }, "unsupported"},
		{"missing pull", func(q *generated.QueryGhBody) { q.Number = 99 }, "hydration_failed"},
	} {
		candidate := body
		tc.change(&candidate)
		result, err := client.HTTP.QueryGhWithResponse(t.Context(), &generated.QueryGhRequestOptions{Body: &candidate})
		require.NoError(err)
		require.NotNil(result.JSON200)
		assert.False(result.JSON200.Handled)
		assert.Empty(result.JSON200.Output)
		assert.Equal(tc.reason, result.JSON200.Reason, tc.name)
	}
}
