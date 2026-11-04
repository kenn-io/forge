package github

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
)

const pullRequestAutoMergeStateQuery = `
query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    autoMergeAllowed
    pullRequest(number: $number) {
      id
      mergeStateStatus
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

// AutoMergeAPI reads and enables GitHub auto-merge on a pull request.
type AutoMergeAPI interface {
	PullRequestAutoMergeState(ctx context.Context, owner, repo string, number int) (platform.AutoMergeState, error)
	EnablePullRequestAutoMerge(
		ctx context.Context, owner, repo string, number int,
		commitTitle, commitMessage, method, expectedHeadSHA string,
	) (time.Time, error)
}

type graphQLAutoMergeRequest struct {
	EnabledAt time.Time `json:"enabledAt"`
}

type pullRequestAutoMergeStateResponse struct {
	Errors []graphQLError `json:"errors"`
	Data   struct {
		Repository *struct {
			AutoMergeAllowed bool `json:"autoMergeAllowed"`
			PullRequest      *struct {
				ID               string                   `json:"id"`
				MergeStateStatus string                   `json:"mergeStateStatus"`
				AutoMergeRequest *graphQLAutoMergeRequest `json:"autoMergeRequest"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// readPullRequestAutoMergeState reads the repository's auto-merge setting and
// the pull request's node ID and auto-merge state, as the write credential
// that would enable it.
func (c *Client) readPullRequestAutoMergeState(
	ctx context.Context, owner, repo string, number int,
) (pullRequestAutoMergeStateResponse, error) {
	var state pullRequestAutoMergeStateResponse
	if err := c.postWriteGraphQL(ctx, graphQLRequest{
		Query:     pullRequestAutoMergeStateQuery,
		Variables: map[string]any{"owner": owner, "repo": repo, "number": number},
	}, &state); err != nil {
		return state, fmt.Errorf("reading auto-merge on %s/%s#%d: %w", owner, repo, number, err)
	}
	if len(state.Errors) > 0 {
		return state, fmt.Errorf(
			"reading auto-merge on %s/%s#%d: %s",
			owner, repo, number, joinGraphQLErrorMessages(state.Errors),
		)
	}
	if state.Data.Repository == nil || state.Data.Repository.PullRequest == nil ||
		state.Data.Repository.PullRequest.ID == "" {
		return state, fmt.Errorf("reading auto-merge on %s/%s#%d: pull request not found", owner, repo, number)
	}
	return state, nil
}

// PullRequestAutoMergeState reports the repository's auto-merge setting,
// whether GitHub could merge the pull request now, and whether auto-merge is
// on. GitHub refuses auto-merge for a pull request it can already merge
// ("Pull request is in clean status"), for example one whose pending checks
// are not required.
func (c *Client) PullRequestAutoMergeState(
	ctx context.Context, owner, repo string, number int,
) (platform.AutoMergeState, error) {
	state, err := c.readPullRequestAutoMergeState(ctx, owner, repo, number)
	if err != nil {
		return platform.AutoMergeState{}, err
	}
	pr := state.Data.Repository.PullRequest
	return platform.AutoMergeState{
		RepositoryAllows: state.Data.Repository.AutoMergeAllowed,
		MergeableNow:     mergeableNow(pr.MergeStateStatus),
		Enabled:          pr.AutoMergeRequest != nil,
		EnabledAt:        autoMergeEnabledAt(pr.AutoMergeRequest),
	}, nil
}

// autoMergeEnabledAt is when GitHub turned the auto-merge request on, or zero
// when there is none.
func autoMergeEnabledAt(request *graphQLAutoMergeRequest) time.Time {
	if request == nil {
		return time.Time{}
	}
	return request.EnabledAt.UTC()
}

// mergeableNow reports a merge state in which GitHub would merge the pull
// request at once and so refuses to enable auto-merge for it.
func mergeableNow(mergeStateStatus string) bool {
	switch mergeStateStatus {
	case "CLEAN", "HAS_HOOKS", "UNSTABLE":
		return true
	default:
		return false
	}
}

// EnablePullRequestAutoMerge asks GitHub to merge the pull request once its
// requirements pass. On a branch with a merge queue GitHub adds it to the
// queue instead and ignores the method, title, and message. Auto-merge that
// is already on counts as success. A platform.ErrCodeConflict error carries
// GitHub's refusal, such as a pull request that can already merge.
func (c *Client) EnablePullRequestAutoMerge(
	ctx context.Context, owner, repo string, number int,
	commitTitle, commitMessage, method, expectedHeadSHA string,
) (time.Time, error) {
	state, err := c.readPullRequestAutoMergeState(ctx, owner, repo, number)
	if err != nil {
		return time.Time{}, err
	}
	if on := state.Data.Repository.PullRequest.AutoMergeRequest; on != nil {
		return on.EnabledAt, nil
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
		return time.Time{}, fmt.Errorf("enabling auto-merge on %s/%s#%d: %w", owner, repo, number, err)
	}
	enabled := result.Data.EnablePullRequestAutoMerge
	if enabled != nil && enabled.PullRequest != nil && enabled.PullRequest.AutoMergeRequest != nil {
		return enabled.PullRequest.AutoMergeRequest.EnabledAt, nil
	}
	if len(result.Errors) > 0 {
		err := fmt.Errorf(
			"enabling auto-merge on %s/%s#%d: %s",
			owner, repo, number, joinGraphQLErrorMessages(result.Errors),
		)
		if !graphQLRefusal(result.Errors) {
			return time.Time{}, err
		}
		return time.Time{}, &platform.Error{
			Code:         platform.ErrCodeConflict,
			Provider:     platform.KindGitHub,
			PlatformHost: c.platformHost,
			Capability:   "auto_merge_mutation",
			Err:          err,
		}
	}
	return time.Time{}, fmt.Errorf("enabling auto-merge on %s/%s#%d: GitHub's answer shows no auto-merge request", owner, repo, number)
}

// graphQLRefusal reports that every error is one GitHub returns when it
// refuses a mutation without applying it. GitHub refuses auto-merge it cannot
// apply with UNPROCESSABLE ("Pull request is already merged"); NOT_FOUND and
// FORBIDDEN fail before anything runs. Any other error, or one without a
// type, may come from a mutation that ran, so its outcome stays unknown.
func graphQLRefusal(errs []graphQLError) bool {
	for _, e := range errs {
		switch e.Type {
		case "UNPROCESSABLE", "NOT_FOUND", "FORBIDDEN":
		default:
			return false
		}
	}
	return true
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

// AutoMergeState reports whether the repository allows auto-merge and
// whether it is on for the pull request.
func (p *Provider) AutoMergeState(
	ctx context.Context, ref platform.RepoRef, number int,
) (platform.AutoMergeState, error) {
	client, ok := p.client.(AutoMergeAPI)
	if !ok {
		return platform.AutoMergeState{}, platform.UnsupportedCapability(platform.KindGitHub, p.host, "auto_merge_mutation")
	}
	return client.PullRequestAutoMergeState(ctx, ref.Owner, ref.Name, number)
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
) (time.Time, error) {
	client, ok := p.client.(AutoMergeAPI)
	if !ok {
		return time.Time{}, platform.UnsupportedCapability(platform.KindGitHub, p.host, "auto_merge_mutation")
	}
	return client.EnablePullRequestAutoMerge(
		ctx, ref.Owner, ref.Name, number, commitTitle, commitMessage, method, expectedHeadSHA,
	)
}

var (
	_ AutoMergeAPI              = (*Client)(nil)
	_ platform.AutoMergeMutator = (*Provider)(nil)
)
