package github

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v93/github"
)

func (c *Client) GetCombinedStatus(
	ctx context.Context, owner, repo, ref string,
) (*gh.CombinedStatus, error) {
	status, resp, err := c.gh.Repositories.GetCombinedStatus(ctx, owner, repo, ref, nil)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf("getting combined status for %s/%s@%s: %w", owner, repo, ref, err)
	}
	return status, nil
}

func (c *Client) ListCheckRunsForRef(
	ctx context.Context, owner, repo, ref string,
) ([]*gh.CheckRun, error) {
	opts := &gh.ListCheckRunsOptions{
		PerPage: 100,
	}
	all, err := collectPages(ctx, func(pageOpts *gh.ListOptions) ([]*gh.CheckRun, *gh.Response, error) {
		opts.ListOptions = *pageOpts
		result, resp, err := c.gh.Checks.ListCheckRunsForRef(
			ctx, owner, repo, ref, opts,
		)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"listing check runs for %s/%s@%s: %w",
				owner, repo, ref, err,
			)
		}
		return result.CheckRuns, resp, nil
	}, c.trackRate)
	if err != nil {
		return nil, err
	}
	return all, nil
}

func (c *Client) ListWorkflowRunsForHeadSHA(
	ctx context.Context, owner, repo, headSHA string,
) ([]*gh.WorkflowRun, error) {
	opts := &gh.ListWorkflowRunsOptions{
		HeadSHA: headSHA,
		Status:  "action_required",
		PerPage: 100,
	}
	all, err := collectPages(ctx, func(pageOpts *gh.ListOptions) ([]*gh.WorkflowRun, *gh.Response, error) {
		opts.ListOptions = *pageOpts
		result, resp, err := c.gh.Actions.ListRepositoryWorkflowRuns(
			ctx, owner, repo, opts,
		)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"listing workflow runs for %s/%s@%s: %w",
				owner, repo, headSHA, err,
			)
		}
		return result.WorkflowRuns, resp, nil
	}, c.trackRate)
	if err != nil {
		return nil, err
	}
	return all, nil
}

func (c *Client) ApproveWorkflowRun(
	ctx context.Context, owner, repo string, runID int64,
) error {
	req, err := c.writeGH().NewRequest(
		ctx,
		"POST",
		fmt.Sprintf("repos/%s/%s/actions/runs/%d/approve", owner, repo, runID),
		nil,
	)
	if err != nil {
		return fmt.Errorf(
			"building workflow approval request for %s/%s run %d: %w",
			owner, repo, runID, err,
		)
	}

	resp, err := c.writeGH().Do(req, nil)
	c.trackWriteRate(resp)
	if err != nil {
		return fmt.Errorf(
			"approving workflow run %s/%s#%d: %w",
			owner, repo, runID, err,
		)
	}
	return nil
}
