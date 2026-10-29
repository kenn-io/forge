package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/platform"
)

func (c *Client) CreateIssueComment(
	ctx context.Context, owner, repo string, number int, body string,
) (*gh.IssueComment, error) {
	comment, resp, err := c.writeGH().Issues.CreateComment(ctx, owner, repo, number, gh.IssueCommentRequest{
		Body: body,
	})
	c.trackWriteRate(resp)
	if err != nil {
		return nil, fmt.Errorf("creating comment on %s/%s#%d: %w", owner, repo, number, err)
	}
	return comment, nil
}

func (c *Client) EditIssueComment(
	ctx context.Context, owner, repo string, commentID int64, body string,
) (*gh.IssueComment, error) {
	comment, resp, err := c.writeGH().Issues.UpdateComment(
		ctx, owner, repo, commentID, gh.IssueCommentRequest{Body: body},
	)
	c.trackWriteRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"editing comment %d on %s/%s: %w", commentID, owner, repo, err,
		)
	}
	return comment, nil
}

func (c *Client) DeleteIssueComment(
	ctx context.Context, owner, repo string, commentID int64,
) error {
	resp, err := c.writeGH().Issues.DeleteComment(ctx, owner, repo, commentID)
	c.trackWriteRate(resp)
	if err != nil {
		if responseErr, ok := errors.AsType[*gh.ErrorResponse](err); ok && responseErr.Response != nil && responseErr.Response.StatusCode == http.StatusNotFound {
			return &platform.Error{
				Code:         platform.ErrCodeNotFound,
				Provider:     platform.KindGitHub,
				PlatformHost: c.platformHost,
				Err:          err,
			}
		}
		return fmt.Errorf("deleting comment %d on %s/%s: %w", commentID, owner, repo, err)
	}
	return nil
}

func (c *Client) CreatePullRequestReviewCommentReply(
	ctx context.Context, owner, repo string, number int, body string, commentID int64,
) (*gh.PullRequestComment, error) {
	comment, resp, err := c.writeGH().PullRequests.CreateCommentInReplyTo(
		ctx, owner, repo, number, body, commentID,
	)
	c.trackWriteRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"replying to review comment %d on %s/%s#%d: %w",
			commentID, owner, repo, number, err,
		)
	}
	return comment, nil
}
