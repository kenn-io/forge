package github

import (
	"context"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestLiveGraphQLQueriesValidateAgainstGitHub(t *testing.T) {
	skipUnlessLiveGitHubTests(t)
	token := requireLiveGitHubToken(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	httpClient := oauth2.NewClient(
		context.Background(),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
	)
	client := githubv4.NewClient(httpClient)

	var prQuery gqlPRQuery[gqlPR]
	vars := map[string]any{
		"owner":    githubv4.String("kenn-io"),
		"name":     githubv4.String("middleman"),
		"pageSize": githubv4.Int(1),
		"cursor":   (*githubv4.String)(nil),
	}
	err := client.Query(ctx, &prQuery, vars)
	require.NoError(t, err, "bulk PR GraphQL query should validate against GitHub")

	var issueQuery gqlIssueQuery
	err = client.Query(ctx, &issueQuery, vars)
	require.NoError(t, err, "bulk issue GraphQL query should validate against GitHub")

	reviewClient := &liveClient{
		httpClient:      httpClient,
		platformHost:    "github.com",
		graphQLEndpoint: graphQLEndpointForHost("github.com"),
	}
	threads, _, _, err := reviewClient.ListInventoryReviewThreadsPage(
		ctx, "github.com", "kenn-io", "forge", 830, "",
	)
	require.NoError(t, err, "review-thread GraphQL query should validate against GitHub")
	require.NotEmpty(t, threads, "live review fixture should contain a review thread")

	comments, _, _, err := reviewClient.listReviewThreadCommentsPage(
		ctx, "kenn-io", "forge", 830,
		githubArchiveReviewCursor{
			Host: "github.com", Owner: "kenn-io", Repo: "forge", Number: 830,
			Phase: "comments", Thread: archiveReviewThreadCursor(threads[0]),
		},
	)
	require.NoError(t, err, "review-thread comment GraphQL query should validate against GitHub")
	require.NotEmpty(t, comments, "live review fixture should return its review thread")
	require.NotEmpty(t, comments[0].Comments, "live review fixture should contain a review comment")
}
