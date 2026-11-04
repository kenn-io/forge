package github

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strings"

	"go.kenn.io/forge/platform"
)

const enablePullRequestAutoMergeMutation = `
mutation($pullRequestId: ID!, $mergeMethod: PullRequestMergeMethod, $commitHeadline: String, $commitBody: String, $expectedHeadOid: GitObjectID) {
  enablePullRequestAutoMerge(input: {pullRequestId: $pullRequestId, mergeMethod: $mergeMethod, commitHeadline: $commitHeadline, commitBody: $commitBody, expectedHeadOid: $expectedHeadOid}) {
    pullRequest {
      autoMergeRequest {
        enabledAt
      }
    }
  }
}`

// AutoMergeAPI enables GitHub auto-merge on a pull request.
type AutoMergeAPI interface {
	EnablePullRequestAutoMerge(
		ctx context.Context, owner, repo string, number int,
		commitTitle, commitMessage, method, expectedHeadSHA string,
	) error
}

// EnablePullRequestAutoMerge asks GitHub to merge the pull request once its
// requirements pass. On a branch with a merge queue GitHub adds it to the
// queue instead and ignores the method, title, and message. GitHub refuses
// when the repository does not allow auto-merge or the pull request can
// already merge.
func (c *Client) EnablePullRequestAutoMerge(
	ctx context.Context, owner, repo string, number int,
	commitTitle, commitMessage, method, expectedHeadSHA string,
) error {
	var idResult struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Repository *struct {
				PullRequest *struct {
					ID string `json:"id"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := c.postWriteGraphQL(ctx, graphQLRequest{
		Query:     readyForReviewIDQuery,
		Variables: map[string]any{"owner": owner, "repo": repo, "number": number},
	}, &idResult); err != nil {
		return fmt.Errorf("enabling auto-merge on %s/%s#%d: resolve pull request id: %w", owner, repo, number, err)
	}
	if len(idResult.Errors) > 0 {
		return fmt.Errorf(
			"enabling auto-merge on %s/%s#%d: resolve pull request id: %s",
			owner, repo, number, joinGraphQLErrorMessages(idResult.Errors),
		)
	}
	if idResult.Data.Repository == nil || idResult.Data.Repository.PullRequest == nil ||
		idResult.Data.Repository.PullRequest.ID == "" {
		return fmt.Errorf("enabling auto-merge on %s/%s#%d: pull request not found", owner, repo, number)
	}

	variables := map[string]any{
		"pullRequestId":   idResult.Data.Repository.PullRequest.ID,
		"mergeMethod":     strings.ToUpper(method),
		"commitHeadline":  nil,
		"commitBody":      nil,
		"expectedHeadOid": nil,
	}
	if commitTitle != "" {
		variables["commitHeadline"] = commitTitle
	}
	if commitMessage != "" {
		variables["commitBody"] = commitMessage
	}
	if expectedHeadSHA != "" {
		variables["expectedHeadOid"] = expectedHeadSHA
	}
	var result struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			EnablePullRequestAutoMerge *struct {
				PullRequest *struct {
					AutoMergeRequest *struct {
						EnabledAt string `json:"enabledAt"`
					} `json:"autoMergeRequest"`
				} `json:"pullRequest"`
			} `json:"enablePullRequestAutoMerge"`
		} `json:"data"`
	}
	if err := c.postWriteGraphQL(ctx, graphQLRequest{
		Query:     enablePullRequestAutoMergeMutation,
		Variables: variables,
	}, &result); err != nil {
		return fmt.Errorf("enabling auto-merge on %s/%s#%d: %w", owner, repo, number, err)
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf(
			"enabling auto-merge on %s/%s#%d: %s",
			owner, repo, number, joinGraphQLErrorMessages(result.Errors),
		)
	}
	enabled := result.Data.EnablePullRequestAutoMerge
	if enabled == nil || enabled.PullRequest == nil || enabled.PullRequest.AutoMergeRequest == nil {
		return fmt.Errorf("enabling auto-merge on %s/%s#%d: GitHub did not enable auto-merge", owner, repo, number)
	}
	return nil
}

// postWriteGraphQL sends a GraphQL request as the write credential.
func (c *Client) postWriteGraphQL(ctx context.Context, payload graphQLRequest, dest any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(
		c.graphQLContext(ctx), http.MethodPost, c.graphQLEndpoint, bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.writeHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	c.trackWriteGraphQLRateHeaders(resp)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graphql status %s", resp.Status)
	}
	return json.UnmarshalRead(resp.Body, dest, json.MatchCaseInsensitiveNames(true))
}

// EnableAutoMerge asks GitHub to merge the pull request once its
// requirements pass.
func (p *Provider) EnableAutoMerge(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commitTitle string,
	commitMessage string,
	method string,
	expectedHeadSHA string,
) error {
	client, ok := p.client.(AutoMergeAPI)
	if !ok {
		return platform.UnsupportedCapability(platform.KindGitHub, p.host, "auto_merge_mutation")
	}
	return client.EnablePullRequestAutoMerge(
		ctx, ref.Owner, ref.Name, number, commitTitle, commitMessage, method, expectedHeadSHA,
	)
}

var (
	_ AutoMergeAPI              = (*Client)(nil)
	_ platform.AutoMergeMutator = (*Provider)(nil)
)
