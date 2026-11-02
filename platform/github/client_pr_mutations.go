package github

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"time"

	gh "github.com/google/go-github/v93/github"
)

func (c *Client) MarkPullRequestReadyForReview(
	ctx context.Context, owner, repo string, number int,
) (*gh.PullRequest, error) {
	type readyForReviewIDResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Repository *struct {
				PullRequest *struct {
					ID string `json:"id"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	type readyForReviewMutationResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			MarkPullRequestReadyForReview *struct {
				PullRequest *GraphQLPR `json:"pullRequest"`
			} `json:"markPullRequestReadyForReview"`
		} `json:"data"`
	}

	postGraphQL := func(payload any, dest any) (*http.Response, error) {
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(
			c.graphQLContext(ctx),
			http.MethodPost,
			c.graphQLEndpoint,
			bytes.NewReader(body),
		)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.writeHTTPClient().Do(req)
		if err != nil {
			return nil, err
		}
		c.trackWriteGraphQLRateHeaders(resp)
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return resp, newReadyForReviewError(
				fmt.Errorf("graphql status %s", resp.Status),
				resp.StatusCode,
				resp.StatusCode == http.StatusNotFound,
			)
		}
		if err := json.UnmarshalRead(resp.Body, dest, json.MatchCaseInsensitiveNames(true)); err != nil {
			_ = resp.Body.Close()
			return resp, err
		}
		_ = resp.Body.Close()
		return resp, nil
	}

	idPayload := graphQLRequest{
		Query: readyForReviewIDQuery,
		Variables: map[string]any{
			"owner":  owner,
			"repo":   repo,
			"number": number,
		},
	}
	var idResult readyForReviewIDResponse
	resp, err := postGraphQL(idPayload, &idResult)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf(
			"marking %s/%s#%d ready for review: resolve pull request id: %w",
			owner, repo, number, err,
		)
	}
	if len(idResult.Errors) > 0 {
		statusCode, staleState := readyForReviewGraphQLErrorMeta(idResult.Errors)
		return nil, newReadyForReviewError(fmt.Errorf(
			"marking %s/%s#%d ready for review: resolve pull request id: graphql errors: %s",
			owner, repo, number, joinGraphQLErrorMessages(idResult.Errors),
		), statusCode, staleState)
	}
	if idResult.Data.Repository == nil || idResult.Data.Repository.PullRequest == nil || idResult.Data.Repository.PullRequest.ID == "" {
		return nil, newReadyForReviewError(
			fmt.Errorf(
				"marking %s/%s#%d ready for review: resolve pull request id: missing pull request in graphql response",
				owner, repo, number,
			),
			http.StatusNotFound,
			true,
		)
	}

	mutationPayload := graphQLRequest{
		Query: readyForReviewMutation,
		Variables: map[string]any{
			"pullRequestId": idResult.Data.Repository.PullRequest.ID,
		},
	}
	var mutationResult readyForReviewMutationResponse
	resp, err = postGraphQL(mutationPayload, &mutationResult)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf(
			"marking %s/%s#%d ready for review: %w",
			owner, repo, number, err,
		)
	}
	if len(mutationResult.Errors) > 0 {
		statusCode, staleState := readyForReviewGraphQLErrorMeta(mutationResult.Errors)
		return nil, newReadyForReviewError(fmt.Errorf(
			"marking %s/%s#%d ready for review: graphql errors: %s",
			owner, repo, number, joinGraphQLErrorMessages(mutationResult.Errors),
		), statusCode, staleState)
	}
	if mutationResult.Data.MarkPullRequestReadyForReview == nil || mutationResult.Data.MarkPullRequestReadyForReview.PullRequest == nil {
		return nil, newReadyForReviewError(
			fmt.Errorf(
				"marking %s/%s#%d ready for review: missing pull request in graphql response",
				owner, repo, number,
			),
			0,
			false,
		)
	}

	return AdaptPR(mutationResult.Data.MarkPullRequestReadyForReview.PullRequest), nil
}

func (c *Client) ConvertPullRequestToDraft(
	ctx context.Context, owner, repo string, number int,
) (*gh.PullRequest, error) {
	type draftIDResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Repository *struct {
				PullRequest *struct {
					ID string `json:"id"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	type draftMutationResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			ConvertPullRequestToDraft *struct {
				PullRequest *struct {
					ID        string    `json:"id"`
					UpdatedAt time.Time `json:"updatedAt"`
				} `json:"pullRequest"`
			} `json:"convertPullRequestToDraft"`
		} `json:"data"`
	}

	postGraphQL := func(payload any, dest any) (*http.Response, error) {
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(
			c.graphQLContext(ctx),
			http.MethodPost,
			c.graphQLEndpoint,
			bytes.NewReader(body),
		)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.writeHTTPClient().Do(req)
		if err != nil {
			return nil, err
		}
		c.trackWriteGraphQLRateHeaders(resp)
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return resp, newReadyForReviewError(
				fmt.Errorf("graphql status %s", resp.Status),
				resp.StatusCode,
				resp.StatusCode == http.StatusNotFound,
			)
		}
		if err := json.UnmarshalRead(resp.Body, dest, json.MatchCaseInsensitiveNames(true)); err != nil {
			_ = resp.Body.Close()
			return resp, err
		}
		_ = resp.Body.Close()
		return resp, nil
	}

	idPayload := graphQLRequest{
		Query: readyForReviewIDQuery,
		Variables: map[string]any{
			"owner":  owner,
			"repo":   repo,
			"number": number,
		},
	}
	var idResult draftIDResponse
	resp, err := postGraphQL(idPayload, &idResult)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf(
			"converting %s/%s#%d to draft: resolve pull request id: %w",
			owner, repo, number, err,
		)
	}
	if len(idResult.Errors) > 0 {
		statusCode, staleState := readyForReviewGraphQLErrorMeta(idResult.Errors)
		return nil, newReadyForReviewError(fmt.Errorf(
			"converting %s/%s#%d to draft: resolve pull request id: graphql errors: %s",
			owner, repo, number, joinGraphQLErrorMessages(idResult.Errors),
		), statusCode, staleState)
	}
	if idResult.Data.Repository == nil || idResult.Data.Repository.PullRequest == nil || idResult.Data.Repository.PullRequest.ID == "" {
		return nil, newReadyForReviewError(
			fmt.Errorf(
				"converting %s/%s#%d to draft: resolve pull request id: missing pull request in graphql response",
				owner, repo, number,
			),
			http.StatusNotFound,
			true,
		)
	}

	mutationPayload := graphQLRequest{
		Query: convertToDraftMutation,
		Variables: map[string]any{
			"pullRequestId": idResult.Data.Repository.PullRequest.ID,
		},
	}
	var mutationResult draftMutationResponse
	resp, err = postGraphQL(mutationPayload, &mutationResult)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf(
			"converting %s/%s#%d to draft: %w",
			owner, repo, number, err,
		)
	}
	if len(mutationResult.Errors) > 0 {
		statusCode, staleState := readyForReviewGraphQLErrorMeta(mutationResult.Errors)
		return nil, newReadyForReviewError(fmt.Errorf(
			"converting %s/%s#%d to draft: graphql errors: %s",
			owner, repo, number, joinGraphQLErrorMessages(mutationResult.Errors),
		), statusCode, staleState)
	}
	if mutationResult.Data.ConvertPullRequestToDraft == nil || mutationResult.Data.ConvertPullRequestToDraft.PullRequest == nil {
		return nil, newReadyForReviewError(
			fmt.Errorf(
				"converting %s/%s#%d to draft: missing pull request in graphql response",
				owner, repo, number,
			),
			0,
			false,
		)
	}

	draft := true
	state := "open"
	mutationPR := mutationResult.Data.ConvertPullRequestToDraft.PullRequest
	nodeID := mutationPR.ID
	updatedAt := gh.Timestamp{Time: mutationPR.UpdatedAt}
	return &gh.PullRequest{
		NodeID:    &nodeID,
		Number:    &number,
		State:     &state,
		Draft:     &draft,
		UpdatedAt: &updatedAt,
	}, nil
}

func (c *Client) EditPullRequest(
	ctx context.Context, owner, repo string, number int, opts EditPullRequestOpts,
) (*gh.PullRequest, error) {
	edit := &gh.PullRequest{}
	if opts.State != nil {
		edit.State = opts.State
	}
	if opts.Title != nil {
		edit.Title = opts.Title
	}
	if opts.Body != nil {
		edit.Body = opts.Body
	}
	pr, resp, err := c.writeGH().PullRequests.Edit(
		ctx, owner, repo, number, edit,
	)
	c.trackWriteRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"editing pull request %s/%s#%d: %w",
			owner, repo, number, err,
		)
	}
	return pr, nil
}

func (c *Client) EditIssue(
	ctx context.Context, owner, repo string, number int, state string,
) (*gh.Issue, error) {
	issue, resp, err := c.writeGH().Issues.Update(
		ctx, owner, repo, number, gh.UpdateIssueRequest{State: &state},
	)
	c.trackWriteRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"editing issue %s/%s#%d: %w",
			owner, repo, number, err,
		)
	}
	return issue, nil
}

func (c *Client) EditIssueContent(
	ctx context.Context, owner, repo string, number int, title *string, body *string,
) (*gh.Issue, error) {
	req := gh.UpdateIssueRequest{}
	if title != nil {
		req.Title = title
	}
	if body != nil {
		req.Body = body
	}
	issue, resp, err := c.writeGH().Issues.Update(ctx, owner, repo, number, req)
	c.trackWriteRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"editing issue %s/%s#%d: %w",
			owner, repo, number, err,
		)
	}
	return issue, nil
}
