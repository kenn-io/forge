package github

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestLiveGraphQLQueriesValidateAgainstGitHub(t *testing.T) {
	skipUnlessLiveGitHubTests(t)
	token := requireLiveGitHubToken(t)
	require := require.New(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := githubv4.NewClient(oauth2.NewClient(
		context.Background(),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
	))
	repository := strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY"))
	if repository == "" {
		repository = "kenn-io/middleman"
	}
	owner, name, ok := strings.Cut(repository, "/")
	require.True(ok, "GITHUB_REPOSITORY must use owner/name form")
	require.NotEmpty(owner, "GITHUB_REPOSITORY owner must not be empty")
	require.NotEmpty(name, "GITHUB_REPOSITORY name must not be empty")

	var prQuery gqlPRQuery[gqlPR]
	vars := map[string]any{
		"owner":    githubv4.String(owner),
		"name":     githubv4.String(name),
		"pageSize": githubv4.Int(1),
		"cursor":   (*githubv4.String)(nil),
	}
	err := client.Query(ctx, &prQuery, vars)
	require.NoError(err, "bulk PR GraphQL query should validate against GitHub")

	var issueQuery gqlIssueQuery
	err = client.Query(ctx, &issueQuery, vars)
	require.NoError(err, "bulk issue GraphQL query should validate against GitHub")
}
