package github

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/platform"
)

func (c *Client) ListRepositoryWorkflows(
	ctx context.Context, owner, repo string,
) ([]*gh.Workflow, error) {
	return collectPages(ctx, func(opts *gh.ListOptions) ([]*gh.Workflow, *gh.Response, error) {
		workflows, resp, err := c.gh.Actions.ListWorkflows(ctx, owner, repo, opts)
		if err != nil {
			return nil, resp, err
		}
		return workflows.Workflows, resp, nil
	}, c.trackRate)
}

func (c *Client) GetWorkflowDefinition(
	ctx context.Context, owner, repo, path, ref string,
) (string, string, error) {
	file, directory, resp, err := c.gh.Repositories.GetContents(
		ctx, owner, repo, path, &gh.RepositoryContentGetOptions{Ref: ref},
	)
	c.trackRate(resp)
	if err != nil {
		return "", "", err
	}
	if file == nil || directory != nil {
		return "", "", fmt.Errorf("workflow definition %q is not a file", path)
	}
	content, err := file.GetContent()
	if err != nil {
		return "", "", fmt.Errorf("decode workflow definition %q: %w", path, err)
	}
	if len(content) > MaxWorkflowDefinitionBytes {
		return "", "", fmt.Errorf(
			"workflow definition exceeds %d-byte limit",
			MaxWorkflowDefinitionBytes,
		)
	}
	return content, file.GetSHA(), nil
}

func (c *Client) ListRepositoryEnvironments(
	ctx context.Context, owner, repo string,
) ([]*gh.Environment, error) {
	return collectPages(ctx, func(opts *gh.ListOptions) ([]*gh.Environment, *gh.Response, error) {
		environments, resp, err := c.gh.Repositories.ListEnvironments(
			ctx, owner, repo, &gh.EnvironmentListOptions{ListOptions: *opts},
		)
		if err != nil {
			return nil, resp, err
		}
		return environments.Environments, resp, nil
	}, c.trackRate)
}

func (c *Client) ListManualWorkflowRuns(
	ctx context.Context,
	owner, repo string,
	workflowID int64,
	query platform.WorkflowRunQuery,
) (platform.Page[*gh.WorkflowRun], error) {
	page := 0
	if query.Cursor != "" {
		parsed, err := strconv.Atoi(query.Cursor)
		if err != nil || parsed <= 0 {
			return platform.Page[*gh.WorkflowRun]{}, &platform.Error{
				Code: platform.ErrCodeInvalidArgument, Provider: platform.KindGitHub,
				PlatformHost: c.platformHost, Field: "cursor",
				Err: errors.New("cursor must be a positive decimal GitHub page number"),
			}
		}
		page = parsed
	}
	runs, resp, err := c.gh.Actions.ListWorkflowRunsByID(
		ctx, owner, repo, workflowID, &gh.ListWorkflowRunsOptions{
			Event: query.Event, Branch: query.Branch,
			Page: page, PerPage: query.PerPage,
		},
	)
	c.trackRate(resp)
	if err != nil {
		return platform.Page[*gh.WorkflowRun]{}, err
	}
	result := platform.Page[*gh.WorkflowRun]{Items: runs.WorkflowRuns}
	if resp != nil && resp.NextPage > 0 {
		result.NextCursor = strconv.Itoa(resp.NextPage)
	} else {
		result.Exhausted = true
	}
	return result, nil
}

func (c *Client) GetManualWorkflowRun(ctx context.Context, owner, repo string, runID int64) (*gh.WorkflowRun, error) {
	run, resp, err := c.gh.Actions.GetWorkflowRunByID(ctx, owner, repo, runID)
	c.trackRate(resp)
	return run, err
}

func (c *Client) ListManualWorkflowJobs(
	ctx context.Context, owner, repo string, runID int64,
) ([]*gh.WorkflowJob, error) {
	return collectPages(ctx, func(opts *gh.ListOptions) ([]*gh.WorkflowJob, *gh.Response, error) {
		jobs, resp, err := c.gh.Actions.ListWorkflowJobs(
			ctx, owner, repo, runID, &gh.ListWorkflowJobsOptions{ListOptions: *opts},
		)
		if err != nil {
			return nil, resp, err
		}
		return jobs.Jobs, resp, nil
	}, c.trackRate)
}

func (c *Client) DispatchManualWorkflow(
	ctx context.Context,
	owner, repo string,
	workflowID int64,
	request gh.CreateWorkflowDispatchEventRequest,
) (*gh.WorkflowDispatchRunDetails, error) {
	request.ReturnRunDetails = new(true)
	details, resp, err := c.writeGH().Actions.CreateWorkflowDispatchEventByID(
		ctx, owner, repo, workflowID, request,
	)
	c.trackWriteRate(resp)
	return details, err
}
