package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"time"

	gh "github.com/google/go-github/v93/github"

	"go.kenn.io/forge/platform"
)

const (
	githubAsyncMergeAPIVersion = "2026-03-10"
	mergeAsyncInitialPollDelay = 100 * time.Millisecond
	mergeAsyncMaxPollDelay     = 2 * time.Second
	mergeAsyncMaxPolls         = 120
)

type mergeAsyncRequest struct {
	CommitTitle   string `json:"commit_title,omitempty"`
	CommitMessage string `json:"commit_message,omitempty"`
	SHA           string `json:"sha,omitempty"`
	MergeMethod   string `json:"merge_method,omitempty"`
	MergeAction   string `json:"merge_action"`
}

type mergeAsyncResponse struct {
	Status  string            `json:"status"`
	Details mergeAsyncDetails `json:"details"`
}

type mergeAsyncDetails struct {
	Message         string `json:"message"`
	UUID            string `json:"uuid"`
	SHA             string `json:"sha"`
	MergeMethod     string `json:"merge_method"`
	MergeAction     string `json:"merge_action"`
	ExpectedHeadSHA string `json:"expected_head_sha"`
}

func (c *Client) MergePullRequest(
	ctx context.Context, owner, repo string, number int,
	commitTitle, commitMessage, method, expectedHeadSHA string,
) (*gh.PullRequestMergeResult, error) {
	if c.platformHost != "github.com" {
		return c.mergePullRequestSync(
			ctx, owner, repo, number,
			commitTitle, commitMessage, method, expectedHeadSHA,
		)
	}
	return c.mergePullRequestAsync(
		ctx, owner, repo, number,
		commitTitle, commitMessage, method, expectedHeadSHA,
	)
}

func (c *Client) mergePullRequestSync(
	ctx context.Context, owner, repo string, number int,
	commitTitle, commitMessage, method, expectedHeadSHA string,
) (*gh.PullRequestMergeResult, error) {
	opts := &gh.PullRequestOptions{
		CommitTitle: commitTitle,
		MergeMethod: method,
		// When set, GitHub rejects the merge if the PR head moved past the
		// reviewed commit.
		SHA: expectedHeadSHA,
	}
	result, resp, err := c.writeGH().PullRequests.Merge(
		ctx, owner, repo, number, commitMessage, opts,
	)
	c.trackWriteRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"merging %s/%s#%d: %w", owner, repo, number, err,
		)
	}
	return result, nil
}

func (c *Client) mergePullRequestAsync(
	ctx context.Context, owner, repo string, number int,
	commitTitle, commitMessage, method, expectedHeadSHA string,
) (*gh.PullRequestMergeResult, error) {
	result, _, err := c.runMergeAsync(ctx, owner, repo, number, mergeAsyncRequest{
		CommitTitle:   commitTitle,
		CommitMessage: commitMessage,
		SHA:           expectedHeadSHA,
		MergeMethod:   method,
		MergeAction:   "direct_merge",
	}, false)
	if err != nil {
		return nil, fmt.Errorf("merging %s/%s#%d: %w", owner, repo, number, err)
	}
	return result.pullRequestMergeResult(), nil
}

// EnqueuePullRequest adds a pull request to its base branch's merge queue
// through the asynchronous merge API. The queue merges with its configured
// method, so GitHub accepts no commit title, message, or method here. Only
// GitHub.com offers the asynchronous API.
func (c *Client) EnqueuePullRequest(
	ctx context.Context, owner, repo string, number int, expectedHeadSHA string,
) (platform.MergeResult, error) {
	if c.platformHost != "github.com" {
		return platform.MergeResult{}, fmt.Errorf(
			"enqueuing %s/%s#%d: %s does not provide GitHub's asynchronous merge API",
			owner, repo, number, c.platformHost,
		)
	}
	result, immediate, err := c.runMergeAsync(ctx, owner, repo, number, mergeAsyncRequest{
		SHA:         expectedHeadSHA,
		MergeAction: "merge_queue",
	}, true)
	if err != nil {
		return platform.MergeResult{}, fmt.Errorf("enqueuing %s/%s#%d: %w", owner, repo, number, err)
	}
	queued := result.Status == "enqueued"
	return platform.MergeResult{
		Merged:        result.Status == "merged",
		Queued:        queued,
		AlreadyQueued: queued && immediate,
		SHA:           result.Details.SHA,
		Message:       result.Details.Message,
	}, nil
}

// runMergeAsync submits an asynchronous merge request and polls it to a
// terminal status. "enqueued" is terminal success only when enqueueAccepted;
// a direct merge that GitHub queued instead did not merge. immediate reports
// that the submission itself was terminal: GitHub answers 200 instead of 202
// when the pull request is already merged or already in the merge queue.
func (c *Client) runMergeAsync(
	ctx context.Context, owner, repo string, number int,
	body mergeAsyncRequest, enqueueAccepted bool,
) (result mergeAsyncResponse, immediate bool, err error) {
	requestPath := fmt.Sprintf(
		"repos/%s/%s/pulls/%d/merge-async", owner, repo, number,
	)
	result, resp, err := c.doMergeAsyncRequest(
		ctx, http.MethodPut, requestPath, body,
	)
	if err != nil {
		return result, false, err
	}
	if terminal, err := mergeAsyncTerminalResult(resp, result, enqueueAccepted); terminal {
		return result, true, err
	}
	if resp == nil || resp.StatusCode != http.StatusAccepted {
		return result, false, errors.New("GitHub returned pending without accepting an asynchronous merge")
	}
	if result.Details.UUID == "" {
		return result, false, errors.New("GitHub accepted an asynchronous merge without an operation UUID")
	}

	pollPath := requestPath + "/" + result.Details.UUID
	for poll := range mergeAsyncMaxPolls {
		if err := waitForMergeAsyncPoll(ctx, poll); err != nil {
			return result, false, err
		}
		result, resp, err = c.doMergeAsyncRequest(ctx, http.MethodGet, pollPath, nil)
		if err != nil {
			return result, false, err
		}
		if terminal, err := mergeAsyncTerminalResult(resp, result, enqueueAccepted); terminal {
			return result, false, err
		}
	}

	return result, false, fmt.Errorf(
		"GitHub asynchronous merge did not complete after %d polls",
		mergeAsyncMaxPolls,
	)
}

func (c *Client) doMergeAsyncRequest(
	ctx context.Context,
	method string,
	requestPath string,
	body any,
) (mergeAsyncResponse, *gh.Response, error) {
	var result mergeAsyncResponse
	req, err := c.writeGH().NewRequest(
		ctx, method, requestPath, body, gh.WithVersion(githubAsyncMergeAPIVersion),
	)
	if err != nil {
		return result, nil, err
	}
	resp, requestErr := c.writeGH().BareDo(req)
	c.trackWriteRate(resp)
	if resp == nil {
		return result, nil, requestErr
	}
	if requestErr != nil {
		if accepted, ok := errors.AsType[*gh.AcceptedError](requestErr); ok && resp.StatusCode == http.StatusAccepted {
			if err := json.Unmarshal(accepted.Raw, &result); err != nil {
				return result, resp, err
			}
			return result, resp, nil
		}
		if githubError, ok := errors.AsType[*gh.ErrorResponse](requestErr); ok &&
			resp.StatusCode == http.StatusConflict &&
			githubError.Message == "" {
			githubError.Message = "an asynchronous merge request is already in progress"
		}
		return result, resp, requestErr
	}
	defer resp.Body.Close()
	decodeErr := json.UnmarshalRead(resp.Body, &result)
	if decodeErr != nil {
		return result, resp, decodeErr
	}
	return result, resp, nil
}

func mergeAsyncTerminalResult(
	resp *gh.Response,
	result mergeAsyncResponse,
	enqueueAccepted bool,
) (bool, error) {
	switch result.Status {
	case "pending":
		return false, nil
	case "merged":
		return true, nil
	case "enqueued":
		if enqueueAccepted {
			return true, nil
		}
		fallthrough
	case "failed":
		message := result.Details.Message
		if message == "" {
			message = "GitHub asynchronous merge ended in state " + result.Status
		}
		conflict := mergeAsyncConflictResponse(resp)
		_ = conflict.Body.Close()
		return true, &gh.ErrorResponse{
			Response: conflict,
			Message:  message,
		}
	default:
		return true, fmt.Errorf(
			"GitHub asynchronous merge returned unknown status %q", result.Status,
		)
	}
}

func mergeAsyncConflictResponse(resp *gh.Response) *http.Response {
	conflict := &http.Response{
		StatusCode: http.StatusConflict,
		Status:     http.StatusText(http.StatusConflict),
		Header:     make(http.Header),
		Body:       http.NoBody,
	}
	if resp == nil || resp.Response == nil {
		return conflict
	}
	conflict.Request = resp.Request
	conflict.Header = resp.Header.Clone()
	return conflict
}

func (r mergeAsyncResponse) pullRequestMergeResult() *gh.PullRequestMergeResult {
	merged := true
	return &gh.PullRequestMergeResult{
		Merged:  &merged,
		SHA:     &r.Details.SHA,
		Message: &r.Details.Message,
	}
}

func waitForMergeAsyncPoll(ctx context.Context, poll int) error {
	delay := mergeAsyncInitialPollDelay
	for i := 0; i < poll && delay < mergeAsyncMaxPollDelay; i++ {
		delay *= 2
		if delay > mergeAsyncMaxPollDelay {
			delay = mergeAsyncMaxPollDelay
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var _ MergeQueueAPI = (*Client)(nil)
