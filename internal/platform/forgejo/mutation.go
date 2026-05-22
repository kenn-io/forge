package forgejo

import (
	"context"

	forgejosdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/platform/gitealike"
)

func (c *Client) CreateMergeRequestComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (platform.MergeRequestEvent, error) {
	return c.provider.CreateMergeRequestComment(ctx, ref, number, body)
}

func (c *Client) EditMergeRequestComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commentID int64,
	body string,
) (platform.MergeRequestEvent, error) {
	return c.provider.EditMergeRequestComment(ctx, ref, number, commentID, body)
}

func (c *Client) CreateIssueComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (platform.IssueEvent, error) {
	return c.provider.CreateIssueComment(ctx, ref, number, body)
}

func (c *Client) EditIssueComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commentID int64,
	body string,
) (platform.IssueEvent, error) {
	return c.provider.EditIssueComment(ctx, ref, number, commentID, body)
}

func (c *Client) CreateIssue(
	ctx context.Context,
	ref platform.RepoRef,
	title string,
	body string,
) (platform.Issue, error) {
	return c.provider.CreateIssue(ctx, ref, title, body)
}

func (c *Client) SetMergeRequestState(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	state string,
) (platform.MergeRequest, error) {
	return c.provider.SetMergeRequestState(ctx, ref, number, state)
}

func (c *Client) SetIssueState(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	state string,
) (platform.Issue, error) {
	return c.provider.SetIssueState(ctx, ref, number, state)
}

func (c *Client) MergeMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commitTitle string,
	commitMessage string,
	method string,
) (platform.MergeResult, error) {
	return c.provider.MergeMergeRequest(ctx, ref, number, commitTitle, commitMessage, method)
}

func (c *Client) ApproveMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (platform.MergeRequestEvent, error) {
	return c.provider.ApproveMergeRequest(ctx, ref, number, body)
}

func (c *Client) EditMergeRequestContent(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	title *string,
	body *string,
) (platform.MergeRequest, error) {
	return c.provider.EditMergeRequestContent(ctx, ref, number, title, body)
}

func (c *Client) EditIssueContent(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	title *string,
	body *string,
) (platform.Issue, error) {
	return c.provider.EditIssueContent(ctx, ref, number, title, body)
}

func (t *transport) CreateIssueComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (gitealike.CommentDTO, error) {
	var comment *forgejosdk.Comment
	var resp *forgejosdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		comment, resp, err = t.api.CreateIssueComment(ref.Owner, ref.Name, int64(number), forgejosdk.CreateIssueCommentOption{
			Body: body,
		})
		return err
	})
	if err != nil {
		return gitealike.CommentDTO{}, forgejoHTTPError(resp, err)
	}
	return convertComment(comment), nil
}

func (t *transport) EditIssueComment(
	ctx context.Context,
	ref platform.RepoRef,
	commentID int64,
	body string,
) (gitealike.CommentDTO, error) {
	var comment *forgejosdk.Comment
	var resp *forgejosdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		comment, resp, err = t.api.EditIssueComment(ref.Owner, ref.Name, commentID, forgejosdk.EditIssueCommentOption{
			Body: body,
		})
		return err
	})
	if err != nil {
		return gitealike.CommentDTO{}, forgejoHTTPError(resp, err)
	}
	return convertComment(comment), nil
}

func (t *transport) CreateIssue(
	ctx context.Context,
	ref platform.RepoRef,
	title string,
	body string,
) (gitealike.IssueDTO, error) {
	var issue *forgejosdk.Issue
	var resp *forgejosdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		issue, resp, err = t.api.CreateIssue(ref.Owner, ref.Name, forgejosdk.CreateIssueOption{
			Title: title,
			Body:  body,
		})
		return err
	})
	if err != nil {
		return gitealike.IssueDTO{}, forgejoHTTPError(resp, err)
	}
	return convertIssue(issue), nil
}

func (t *transport) EditIssue(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	opts gitealike.IssueMutationOptions,
) (gitealike.IssueDTO, error) {
	var issue *forgejosdk.Issue
	var resp *forgejosdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		issue, resp, err = t.api.EditIssue(ref.Owner, ref.Name, int64(number), forgejosdk.EditIssueOption{
			Title: stringValue(opts.Title),
			Body:  opts.Body,
			State: forgejoStatePtr(opts.State),
		})
		return err
	})
	if err != nil {
		return gitealike.IssueDTO{}, forgejoHTTPError(resp, err)
	}
	return convertIssue(issue), nil
}

func (t *transport) EditPullRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	opts gitealike.PullRequestMutationOptions,
) (gitealike.PullRequestDTO, error) {
	var pr *forgejosdk.PullRequest
	var resp *forgejosdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		pr, resp, err = t.api.EditPullRequest(ref.Owner, ref.Name, int64(number), forgejosdk.EditPullRequestOption{
			Title: stringValue(opts.Title),
			Body:  opts.Body,
			State: forgejoStatePtr(opts.State),
		})
		return err
	})
	if err != nil {
		return gitealike.PullRequestDTO{}, forgejoHTTPError(resp, err)
	}
	return convertPullRequest(pr, t.mergeableForPullRequest(pr)), nil
}

func (t *transport) MergePullRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	opts gitealike.MergeOptions,
) (gitealike.MergeResultDTO, error) {
	var merged bool
	var resp *forgejosdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		merged, resp, err = t.api.MergePullRequest(ref.Owner, ref.Name, int64(number), forgejosdk.MergePullRequestOption{
			Style:   forgejoMergeStyle(opts.Method),
			Title:   opts.CommitTitle,
			Message: opts.CommitMessage,
		})
		return err
	})
	if err != nil {
		return gitealike.MergeResultDTO{}, forgejoHTTPError(resp, err)
	}
	return gitealike.MergeResultDTO{Merged: merged}, nil
}

func (t *transport) CreatePullReview(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (gitealike.ReviewDTO, error) {
	var review *forgejosdk.PullReview
	var resp *forgejosdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		review, resp, err = t.api.CreatePullReview(ref.Owner, ref.Name, int64(number), forgejosdk.CreatePullReviewOptions{
			State: forgejosdk.ReviewStateApproved,
			Body:  body,
		})
		return err
	})
	if err != nil {
		return gitealike.ReviewDTO{}, forgejoHTTPError(resp, err)
	}
	return convertReview(review), nil
}

func forgejoStatePtr(state *string) *forgejosdk.StateType {
	if state == nil {
		return nil
	}
	value := forgejosdk.StateType(*state)
	return &value
}

func forgejoMergeStyle(method string) forgejosdk.MergeStyle {
	switch method {
	case "squash":
		return forgejosdk.MergeStyleSquash
	case "rebase":
		return forgejosdk.MergeStyleRebase
	case "rebase-merge":
		return forgejosdk.MergeStyleRebaseMerge
	case "fast-forward-only":
		return forgejosdk.MergeStyleFastForwardOnly
	default:
		return forgejosdk.MergeStyleMerge
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
