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

const pullRequestAutoMergeStateQuery = `
query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      id
      autoMergeRequest {
        enabledAt
      }
    }
  }
}`

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

type graphQLAutoMergeRequest struct {
	EnabledAt string `json:"enabledAt"`
}

// EnablePullRequestAutoMerge asks GitHub to merge the pull request once its
// requirements pass. On a branch with a merge queue GitHub adds it to the
// queue instead and ignores the method, title, and message. GitHub refuses
// when the repository does not allow auto-merge or the pull request can
// already merge. Auto-merge that is already on counts as success, so a retry
// after a lost answer records it. A platform.ErrCodeConflict error means
// GitHub answered that it did not enable auto-merge; any other error leaves
// auto-merge's state unknown.
func (c *Client) EnablePullRequestAutoMerge(
	ctx context.Context, owner, repo string, number int,
	commitTitle, commitMessage, method, expectedHeadSHA string,
) error {
	var state struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Repository *struct {
				PullRequest *struct {
					ID               string                   `json:"id"`
					AutoMergeRequest *graphQLAutoMergeRequest `json:"autoMergeRequest"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := c.postWriteGraphQL(ctx, graphQLRequest{
		Query:     pullRequestAutoMergeStateQuery,
		Variables: map[string]any{"owner": owner, "repo": repo, "number": number},
	}, &state); err != nil {
		return fmt.Errorf("enabling auto-merge on %s/%s#%d: read pull request: %w", owner, repo, number, err)
	}
	if len(state.Errors) > 0 {
		return fmt.Errorf(
			"enabling auto-merge on %s/%s#%d: read pull request: %s",
			owner, repo, number, joinGraphQLErrorMessages(state.Errors),
		)
	}
	if state.Data.Repository == nil || state.Data.Repository.PullRequest == nil ||
		state.Data.Repository.PullRequest.ID == "" {
		return fmt.Errorf("enabling auto-merge on %s/%s#%d: pull request not found", owner, repo, number)
	}
	if state.Data.Repository.PullRequest.AutoMergeRequest != nil {
		return nil
	}

	variables := map[string]any{
		"pullRequestId":   state.Data.Repository.PullRequest.ID,
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
					AutoMergeRequest *graphQLAutoMergeRequest `json:"autoMergeRequest"`
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
	enabled := result.Data.EnablePullRequestAutoMerge
	if enabled != nil && enabled.PullRequest != nil && enabled.PullRequest.AutoMergeRequest != nil {
		return nil
	}
	if len(result.Errors) > 0 {
		return &platform.Error{
			Code:         platform.ErrCodeConflict,
			Provider:     platform.KindGitHub,
			PlatformHost: c.platformHost,
			Capability:   "auto_merge_mutation",
			Err: fmt.Errorf(
				"enabling auto-merge on %s/%s#%d: %s",
				owner, repo, number, joinGraphQLErrorMessages(result.Errors),
			),
		}
	}
	return fmt.Errorf("enabling auto-merge on %s/%s#%d: GitHub's answer shows no auto-merge request", owner, repo, number)
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
