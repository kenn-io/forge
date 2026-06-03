package github

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

const liveGraphQLTestsEnv = "MIDDLEMAN_LIVE_GITHUB_TESTS"

func TestLiveGraphQLQueriesValidateAgainstGitHub(t *testing.T) {
	if os.Getenv(liveGraphQLTestsEnv) != "1" {
		t.Skipf("set %s=1 to validate GraphQL queries against GitHub", liveGraphQLTestsEnv)
	}

	token := os.Getenv("MIDDLEMAN_GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	require.NotEmpty(t, token, "set MIDDLEMAN_GITHUB_TOKEN or GITHUB_TOKEN")

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
