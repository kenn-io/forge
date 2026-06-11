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

	client := githubv4.NewClient(oauth2.NewClient(
		context.Background(),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
	))

	var prQuery gqlPRQuery
	vars := map[string]any{
		"owner":    githubv4.String("wesm"),
		"name":     githubv4.String("middleman"),
		"pageSize": githubv4.Int(1),
		"cursor":   (*githubv4.String)(nil),
	}
	err := client.Query(ctx, &prQuery, vars)
	require.NoError(t, err, "bulk PR GraphQL query should validate against GitHub")

	var issueQuery gqlIssueQuery
	err = client.Query(ctx, &issueQuery, vars)
	require.NoError(t, err, "bulk issue GraphQL query should validate against GitHub")
}
