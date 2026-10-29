package github

import (
	"context"

	gh "github.com/google/go-github/v93/github"
)

func (c *Client) ReplaceIssueLabels(
	ctx context.Context, owner, repo string, number int, names []string,
) ([]*gh.Label, error) {
	labels, resp, err := c.writeGH().Issues.ReplaceLabelsForIssue(ctx, owner, repo, number, names)
	c.trackWriteRate(resp)
	if err != nil {
		return nil, err
	}
	return labels, nil
}

// ReplaceIssueAssignees replaces the full assignee set on an issue or
// pull request (GitHub assigns PRs through the issues API).
func (c *Client) ReplaceIssueAssignees(
	ctx context.Context, owner, repo string, number int, usernames []string,
) (*gh.Issue, error) {
	if usernames == nil {
		usernames = []string{}
	}
	issue, resp, err := c.writeGH().Issues.Update(ctx, owner, repo, number, gh.UpdateIssueRequest{
		Assignees: usernames,
	})
	c.trackWriteRate(resp)
	if err != nil {
		return nil, err
	}
	return issue, nil
}

// RequestPullRequestReviewers requests reviews from the given users and
// returns the updated pull request.
func (c *Client) RequestPullRequestReviewers(
	ctx context.Context, owner, repo string, number int, usernames []string,
) (*gh.PullRequest, error) {
	pr, resp, err := c.writeGH().PullRequests.RequestReviewers(ctx, owner, repo, number, gh.ReviewersRequest{
		Reviewers: usernames,
	})
	c.trackWriteRate(resp)
	if err != nil {
		return nil, err
	}
	return pr, nil
}

// RemovePullRequestReviewers removes pending review requests for the
// given users.
func (c *Client) RemovePullRequestReviewers(
	ctx context.Context, owner, repo string, number int, usernames []string,
) error {
	resp, err := c.writeGH().PullRequests.RemoveReviewers(ctx, owner, repo, number, gh.ReviewersRequest{
		Reviewers: usernames,
	})
	c.trackWriteRate(resp)
	return err
}

func (c *Client) CreateIssue(
	ctx context.Context, owner, repo, title, body string,
) (*gh.Issue, error) {
	req := gh.CreateIssueRequest{
		Title: title,
	}
	if body != "" {
		req.Body = &body
	}
	issue, resp, err := c.writeGH().Issues.Create(ctx, owner, repo, req)
	c.trackWriteRate(resp)
	if err != nil {
		return nil, err
	}
	return issue, nil
}
