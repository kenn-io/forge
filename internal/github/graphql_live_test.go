package github

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/require"
	platformgithub "go.kenn.io/forge/platform/github"
	"golang.org/x/oauth2"
)

func TestLiveGraphQLQueriesValidateAgainstGitHub(t *testing.T) {
	t.Parallel()

	skipUnlessLiveGitHubTests(t)
	require := require.New(t)
	token := requireLiveGitHubToken(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	httpClient := oauth2.NewClient(
		t.Context(),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
	)
	client := githubv4.NewClient(httpClient)

	var prQuery gqlPRQuery[platformgithub.GraphQLPR]
	vars := map[string]any{
		"owner":    githubv4.String("kenn-io"),
		"name":     githubv4.String("middleman"),
		"pageSize": githubv4.Int(topLevelPageSize),
		"cursor":   (*githubv4.String)(nil),
	}
	err := client.Query(ctx, &prQuery, vars)
	require.NoError(err, "bulk PR GraphQL query should validate against GitHub")

	type prDetailQuery struct {
		Repository struct {
			PullRequest *platformgithub.GraphQLPR `graphql:"pullRequest(number: $number)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	var detailQuery prDetailQuery
	err = client.Query(ctx, &detailQuery, map[string]any{
		"owner":  githubv4.String("kenn-io"),
		"name":   githubv4.String("forge"),
		"number": githubv4.Int(830),
	})
	require.NoError(err, "combined PR detail GraphQL query should validate against GitHub")
	require.NotNil(detailQuery.Repository.PullRequest, "live PR fixture should exist")
	require.NotEmpty(detailQuery.Repository.PullRequest.Comments.Nodes,
		"combined live query should return conversation comments")
	require.NotEmpty(detailQuery.Repository.PullRequest.ReviewThreads.Nodes,
		"combined live query should return review threads")
	require.NotEmpty(detailQuery.Repository.PullRequest.ReviewThreads.Nodes[0].Comments.Nodes,
		"combined live query should return review-thread comments")
	require.False(
		detailQuery.Repository.PullRequest.ReviewThreads.Nodes[0].Comments.Nodes[0].CreatedAt.IsZero(),
		"combined live query should return review-comment creation time",
	)
	require.False(
		detailQuery.Repository.PullRequest.ReviewThreads.Nodes[0].Comments.Nodes[0].UpdatedAt.IsZero(),
		"combined live query should return review-comment update time",
	)

	var issueQuery gqlIssueQuery
	err = client.Query(ctx, &issueQuery, vars)
	require.NoError(err, "bulk issue GraphQL query should validate against GitHub")

	var pullRequestQueueQuery gqlPullRequestMergeQueueQuery
	err = client.Query(ctx, &pullRequestQueueQuery, map[string]any{
		"owner":  githubv4.String("kenn-io"),
		"name":   githubv4.String("forge"),
		"number": githubv4.Int(830),
	})
	require.NoError(err, "pull request merge queue GraphQL query should validate against GitHub")
	require.NotNil(pullRequestQueueQuery.Repository.PullRequest, "live PR fixture should exist")

	var rulesQuery gqlPullRequestMergeRulesQuery
	err = client.Query(ctx, &rulesQuery, map[string]any{
		"owner":  githubv4.String("kenn-io"),
		"name":   githubv4.String("forge"),
		"number": githubv4.Int(830),
	})
	require.NoError(err, "pull request merge rules GraphQL query should validate against GitHub")
	require.NotNil(rulesQuery.Repository.PullRequest, "live PR fixture should exist")

	var configurationQuery gqlMergeQueueConfigurationQuery
	err = client.Query(ctx, &configurationQuery, map[string]any{
		"owner":  githubv4.String("kenn-io"),
		"name":   githubv4.String("forge"),
		"branch": githubv4.String("main"),
	})
	// GitHub refuses the queue configuration to some integration tokens,
	// including the Actions token, as mergeQueueMergeMethod expects. The
	// refusal is an execution error, so the query already validated.
	if err != nil && isMergeQueueReadRefused(err) {
		err = nil
	}
	require.NoError(err, "merge queue configuration GraphQL query should validate against GitHub")

	// The freshness probe is a raw aliased query, so it validates through
	// the fetcher that builds it. Any error other than NOT_FOUND fails it.
	fetcher := &GraphQLFetcher{
		client: client, httpClient: httpClient,
		endpoint: graphQLEndpointForHost("github.com"), host: "github.com",
	}
	_, err = fetcher.FetchPullRequestFreshness(ctx, []PullRequestFreshnessRef{
		{Owner: "kenn-io", Name: "forge", Number: 830},
		{Owner: "kenn-io", Name: "forge", Number: 1647},
	})
	require.NoError(err, "aliased pull request freshness probe should validate against GitHub")
	var numbers struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct{ Number int }
			} `graphql:"pullRequests(first: 25, states: [OPEN, CLOSED, MERGED])"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	err = client.Query(ctx, &numbers, map[string]any{
		"owner": githubv4.String("kenn-io"), "name": githubv4.String("forge"),
	})
	require.NoError(err)
	require.Len(numbers.Repository.PullRequests.Nodes, 25)
	refs := make([]PullRequestFreshnessRef, 0, 25)
	for _, pr := range numbers.Repository.PullRequests.Nodes {
		refs = append(refs, PullRequestFreshnessRef{Owner: "kenn-io", Name: "forge", Number: pr.Number})
	}
	query, variables := prFreshnessQuery(refs, prCompleteFreshnessFields)
	query = strings.Replace(query, ") {", ") { rateLimit { cost }", 1)
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	require.NoError(err)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fetcher.endpoint, bytes.NewReader(payload))
	require.NoError(err)
	request.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(request)
	require.NoError(err)
	defer response.Body.Close()
	require.Equal(http.StatusOK, response.StatusCode)
	var complete struct {
		Data struct {
			RateLimit struct {
				Cost int `json:"cost"`
			} `json:"rateLimit"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	require.NoError(json.UnmarshalRead(response.Body, &complete))
	require.Empty(complete.Errors, "complete PR freshness fragment should validate against GitHub")
	t.Logf("complete freshness probe for 25 PRs costs %d GraphQL points", complete.Data.RateLimit.Cost)
	require.Positive(complete.Data.RateLimit.Cost, "live response must report the probe's cost")
	require.LessOrEqual(complete.Data.RateLimit.Cost, 10, "revisit savings estimate above 10 points")

	require.NotEmpty(issueQuery.Repository.Issues.Nodes, "live issue fixture should exist")
	_, err = fetcher.fetchFreshness(ctx, []PullRequestFreshnessRef{
		{Owner: "kenn-io", Name: "middleman", Number: int(issueQuery.Repository.Issues.Nodes[0].Number), IsIssue: true},
	}, prCompleteFreshnessFields, true)
	require.NoError(err, "issue freshness fragment should validate against GitHub")
}
