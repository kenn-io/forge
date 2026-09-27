package bitbucket

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	bitbucket "github.com/ktrysmt/go-bitbucket"
	"go.kenn.io/forge/platform"
)

func (c *Client) writePullComment(ctx context.Context, ref platform.RepoRef, number int, id int64, body string, method string, parent *int) (platform.MergeRequestEvent, error) {
	owner, name, err := repoParts(ref)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	api, err := c.sdk(ctx)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	opts := &bitbucket.PullRequestCommentOptions{Owner: owner, RepoSlug: name, PullRequestID: strconv.Itoa(number), CommentId: strconv.FormatInt(id, 10), Content: body, Parent: parent}
	var value any
	switch method {
	case http.MethodPost:
		value, err = api.Repositories.PullRequests.AddComment(opts)
	case http.MethodPut:
		value, err = api.Repositories.PullRequests.UpdateComment(opts)
	case http.MethodDelete:
		_, err = api.Repositories.PullRequests.DeleteComment(opts)
		return platform.MergeRequestEvent{}, classify(err)
	}
	row, err := decode[comment](value, err)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	return row.pullEvent(ref, number), nil
}

func (c *Client) CreateMergeRequestComment(ctx context.Context, ref platform.RepoRef, number int, body string) (platform.MergeRequestEvent, error) {
	return c.writePullComment(ctx, ref, number, 0, body, http.MethodPost, nil)
}

func (c *Client) EditMergeRequestComment(ctx context.Context, ref platform.RepoRef, number int, id int64, body string) (platform.MergeRequestEvent, error) {
	return c.writePullComment(ctx, ref, number, id, body, http.MethodPut, nil)
}

func (c *Client) DeleteMergeRequestComment(ctx context.Context, ref platform.RepoRef, number int, id int64) error {
	_, err := c.writePullComment(ctx, ref, number, id, "", http.MethodDelete, nil)
	return err
}

func (c *Client) ReplyToThread(ctx context.Context, ref platform.RepoRef, number int, threadID, body string) (platform.MergeRequestEvent, error) {
	id, err := strconv.Atoi(threadID)
	if err != nil || id <= 0 {
		return platform.MergeRequestEvent{}, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "thread_id"}
	}
	return c.writePullComment(ctx, ref, number, 0, body, http.MethodPost, &id)
}

func (c *Client) writeIssueComment(ctx context.Context, ref platform.RepoRef, number int, id int64, body, method string) (platform.IssueEvent, error) {
	issueOpts, err := issueOptions(ref, number)
	if err != nil {
		return platform.IssueEvent{}, err
	}
	api, err := c.sdk(ctx)
	if err != nil {
		return platform.IssueEvent{}, err
	}
	opts := &bitbucket.IssueCommentsOptions{IssuesOptions: *issueOpts, CommentID: strconv.FormatInt(id, 10), CommentContent: body}
	var value any
	switch method {
	case http.MethodPost:
		value, err = api.Repositories.Issues.CreateComment(opts)
	case http.MethodPut:
		value, err = api.Repositories.Issues.UpdateComment(opts)
	case http.MethodDelete:
		_, err = api.Repositories.Issues.DeleteComment(opts)
		return platform.IssueEvent{}, classify(err)
	}
	row, err := decode[comment](value, err)
	if err != nil {
		return platform.IssueEvent{}, err
	}
	return row.issueEvent(ref, number), nil
}

func (c *Client) CreateIssueComment(ctx context.Context, ref platform.RepoRef, number int, body string) (platform.IssueEvent, error) {
	return c.writeIssueComment(ctx, ref, number, 0, body, http.MethodPost)
}

func (c *Client) EditIssueComment(ctx context.Context, ref platform.RepoRef, number int, id int64, body string) (platform.IssueEvent, error) {
	return c.writeIssueComment(ctx, ref, number, id, body, http.MethodPut)
}

func (c *Client) DeleteIssueComment(ctx context.Context, ref platform.RepoRef, number int, id int64) error {
	_, err := c.writeIssueComment(ctx, ref, number, id, "", http.MethodDelete)
	return err
}

func (c *Client) CreateIssue(ctx context.Context, ref platform.RepoRef, title, body string) (platform.Issue, error) {
	opts, err := issueOptions(ref, 0)
	if err != nil {
		return platform.Issue{}, err
	}
	opts.Title, opts.Content = title, body
	api, err := c.sdk(ctx)
	if err != nil {
		return platform.Issue{}, err
	}
	row, err := decode[issue](api.Repositories.Issues.Create(opts))
	if err != nil {
		return platform.Issue{}, err
	}
	return row.normalize(ref)
}

func (c *Client) MergeMergeRequest(ctx context.Context, ref platform.RepoRef, number int, title, body, method, expectedSHA string) (platform.MergeResult, error) {
	opts, err := pullOptions(ref, number)
	if err != nil {
		return platform.MergeResult{}, err
	}
	switch method {
	case "", "merge":
		opts.MergeStrategy = "merge_commit"
	case "squash":
		opts.MergeStrategy = "squash"
	case "rebase":
		opts.MergeStrategy = "rebase_fast_forward"
	default:
		return platform.MergeResult{}, platform.UnsupportedCapability(c.Platform(), c.Host(), "merge_method_"+method)
	}
	if err := c.checkHead(ctx, ref, number, expectedSHA); err != nil {
		return platform.MergeResult{}, err
	}
	opts.Message = strings.TrimSpace(title + "\n\n" + body)
	api, err := c.sdk(ctx)
	if err != nil {
		return platform.MergeResult{}, err
	}
	row, err := decode[pull](api.Repositories.PullRequests.Merge(opts))
	if err != nil {
		return platform.MergeResult{}, err
	}
	// Bitbucket can return 202 with a merge task. Do not report success or
	// resubmit an uncertain write; the next sync will observe its outcome.
	if row.State != "MERGED" {
		return platform.MergeResult{}, &platform.Error{Code: platform.ErrCodeConflict, Provider: c.Platform(), Hint: "Bitbucket has not confirmed the merge. Refresh before retrying."}
	}
	return platform.MergeResult{Merged: true, SHA: row.MergeCommit.Hash}, nil
}

func (c *Client) checkHead(ctx context.Context, ref platform.RepoRef, number int, expected string) error {
	if expected == "" {
		return nil
	}
	row, err := c.GetMergeRequest(ctx, ref, number)
	if err != nil {
		return err
	}
	if row.HeadSHA != expected {
		return &platform.Error{Code: platform.ErrCodeStaleState, Provider: c.Platform()}
	}
	return nil
}

func (c *Client) ApproveMergeRequest(ctx context.Context, ref platform.RepoRef, number int, body, expectedSHA string) (platform.MergeRequestEvent, error) {
	opts, err := pullOptions(ref, number)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	if err := c.checkHead(ctx, ref, number, expectedSHA); err != nil {
		return platform.MergeRequestEvent{}, err
	}
	api, err := c.sdk(ctx)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	if body != "" {
		if _, err := c.CreateMergeRequestComment(ctx, ref, number, body); err != nil {
			return platform.MergeRequestEvent{}, err
		}
	}
	approval, err := decode[participant](api.Repositories.PullRequests.Approve(opts))
	if err != nil {
		return platform.MergeRequestEvent{}, classify(err)
	}
	if expectedSHA != "" {
		if err := c.checkHead(ctx, ref, number, expectedSHA); err != nil {
			_, revokeErr := api.Repositories.PullRequests.UnApprove(opts)
			details := map[string]string{"revocation": "succeeded"}
			if revokeErr != nil {
				details["revocation"] = "failed"
			}
			return platform.MergeRequestEvent{}, &platform.Error{Code: platform.ErrCodeStaleState, Provider: c.Platform(), Details: details, Err: err}
		}
	}
	id := "approval:" + approval.User.UUID + ":" + approval.ParticipatedOn.Format(time.RFC3339Nano)
	return platform.MergeRequestEvent{Repo: ref, MergeRequestNumber: number, PlatformExternalID: id, DedupeKey: id, EventType: "review", Author: approval.User.UUID, Summary: "approved", CreatedAt: approval.ParticipatedOn.UTC()}, nil
}

func (c *Client) RequestChanges(ctx context.Context, ref platform.RepoRef, number int, body, expectedSHA string) error {
	opts, err := pullOptions(ref, number)
	if err != nil {
		return err
	}
	api, err := c.sdk(ctx)
	if err != nil {
		return err
	}
	if body != "" {
		if _, err := c.CreateMergeRequestComment(ctx, ref, number, body); err != nil {
			return err
		}
	}
	_, err = api.Repositories.PullRequests.RequestChanges(opts)
	return classify(err)
}
