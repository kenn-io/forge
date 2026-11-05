package github

import (
	"context"
	"fmt"

	"go.kenn.io/forge/platform"
)

const pullRequestNodeIDQuery = `
query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      id
    }
  }
}`

const dequeuePullRequestMutation = `
mutation($id: ID!) {
  dequeuePullRequest(input: {id: $id}) {
    mergeQueueEntry {
      id
    }
  }
}`

// DequeuePullRequest removes a pull request from its base branch's merge
// queue as the write credential. It always sends the mutation: a pull request
// GitHub has just enqueued has no mergeQueueEntry until the queue lists it,
// so a missing entry does not mean it is out of the queue. GitHub refuses a
// pull request that is not queued with UNPROCESSABLE (observed live), which
// returns as a platform.ErrCodeConflict error like any other refusal.
func (c *Client) DequeuePullRequest(ctx context.Context, owner, repo string, number int) error {
	var state struct {
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
		Query:     pullRequestNodeIDQuery,
		Variables: map[string]any{"owner": owner, "repo": repo, "number": number},
	}, &state); err != nil {
		return fmt.Errorf("reading the node ID of %s/%s#%d: %w", owner, repo, number, err)
	}
	if len(state.Errors) > 0 {
		return fmt.Errorf(
			"reading the node ID of %s/%s#%d: %s",
			owner, repo, number, joinGraphQLErrorMessages(state.Errors),
		)
	}
	if state.Data.Repository == nil || state.Data.Repository.PullRequest == nil ||
		state.Data.Repository.PullRequest.ID == "" {
		return fmt.Errorf("reading the node ID of %s/%s#%d: pull request not found", owner, repo, number)
	}
	pr := state.Data.Repository.PullRequest
	var result struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			DequeuePullRequest *struct{} `json:"dequeuePullRequest"`
		} `json:"data"`
	}
	if err := c.postWriteGraphQL(ctx, graphQLRequest{
		Query:     dequeuePullRequestMutation,
		Variables: map[string]any{"id": pr.ID},
	}, &result); err != nil {
		return fmt.Errorf("removing %s/%s#%d from the merge queue: %w", owner, repo, number, err)
	}
	if len(result.Errors) == 0 && result.Data.DequeuePullRequest != nil {
		return nil
	}
	if len(result.Errors) > 0 {
		err := fmt.Errorf(
			"removing %s/%s#%d from the merge queue: %s",
			owner, repo, number, joinGraphQLErrorMessages(result.Errors),
		)
		if !graphQLRefusal(result.Errors) {
			return err
		}
		return &platform.Error{
			Code: platform.ErrCodeConflict, Provider: platform.KindGitHub,
			PlatformHost: c.platformHost, Capability: "merge_queue_mutation",
			Err: err,
		}
	}
	return fmt.Errorf("removing %s/%s#%d from the merge queue: GitHub's answer does not confirm the removal", owner, repo, number)
}

// DequeueMergeRequest removes the pull request from its base branch's merge
// queue.
func (p *Provider) DequeueMergeRequest(ctx context.Context, ref platform.RepoRef, number int) error {
	client, ok := p.client.(MergeQueueAPI)
	if !ok || p.host != "github.com" {
		return platform.UnsupportedCapability(platform.KindGitHub, p.host, "merge_queue_mutation")
	}
	return client.DequeuePullRequest(ctx, ref.Owner, ref.Name, number)
}
