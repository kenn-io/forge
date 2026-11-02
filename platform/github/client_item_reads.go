package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/platform"
)

func (c *Client) GetIssue(
	ctx context.Context, owner, repo string, number int,
) (*gh.Issue, error) {
	issue, resp, err := c.gh.Issues.Get(ctx, owner, repo, number)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"getting issue %s/%s#%d: %w", owner, repo, number, err,
		)
	}
	return issue, nil
}

func (c *Client) GetIssueIfChanged(
	ctx context.Context,
	owner, repo string,
	number int,
	etag string,
) (*gh.Issue, string, bool, error) {
	u := fmt.Sprintf("repos/%v/%v/issues/%v", owner, repo, number)
	req, err := c.gh.NewRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	issue := new(gh.Issue)
	resp, err := c.gh.Do(req, issue)
	c.trackRate(resp)
	if err != nil {
		if IsNotModified(err) {
			return nil, etag, true, nil
		}
		return nil, "", false, fmt.Errorf(
			"getting issue %s/%s#%d: %w", owner, repo, number, err,
		)
	}
	if resp != nil && resp.Response != nil {
		etag = resp.Header.Get("ETag")
	}
	return issue, etag, false, nil
}

func (c *Client) GetPullRequest(ctx context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
	pr, resp, err := c.gh.PullRequests.Get(ctx, owner, repo, number)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf("getting pull request %s/%s#%d: %w", owner, repo, number, err)
	}
	return pr, nil
}

func (c *Client) GetPullRequestIfChanged(
	ctx context.Context,
	owner, repo string,
	number int,
	etag string,
) (*gh.PullRequest, string, bool, error) {
	u := fmt.Sprintf("repos/%v/%v/pulls/%v", owner, repo, number)
	req, err := c.gh.NewRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	pr := new(gh.PullRequest)
	resp, err := c.gh.Do(req, pr)
	c.trackRate(resp)
	if err != nil {
		if IsNotModified(err) {
			return nil, etag, true, nil
		}
		return nil, "", false, fmt.Errorf("getting pull request %s/%s#%d: %w", owner, repo, number, err)
	}
	if resp != nil && resp.Response != nil {
		etag = resp.Header.Get("ETag")
	}
	return pr, etag, false, nil
}

func (c *Client) GetUser(ctx context.Context, login string) (*gh.User, error) {
	user, resp, err := c.gh.Users.Get(ctx, login)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf("getting user %s: %w", login, err)
	}
	return user, nil
}

func (c *Client) ListIssueComments(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.IssueComment, error) {
	return c.listIssueComments(WithUnconditionalRead(ctx), owner, repo, number)
}

func (c *Client) ListIssueCommentsIfChanged(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.IssueComment, error) {
	return c.listIssueComments(ctx, owner, repo, number)
}

func (c *Client) listIssueComments(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.IssueComment, error) {
	return platform.CollectPages(ctx, "", func(
		ctx context.Context, cursor string,
	) (platform.Page[*gh.IssueComment], error) {
		page, err := strconv.Atoi(cursor)
		if err != nil || page < 1 {
			page = 1
		}
		items, more, err := c.ListIssueCommentsPage(ctx, owner, repo, number, page)
		if err != nil {
			return platform.Page[*gh.IssueComment]{}, err
		}
		if !more {
			return platform.Page[*gh.IssueComment]{Items: items, Exhausted: true}, nil
		}
		return platform.Page[*gh.IssueComment]{
			Items: items, NextCursor: strconv.Itoa(page + 1),
		}, nil
	})
}

func (c *Client) ListReviews(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.PullRequestReview, error) {
	return platform.CollectPages(ctx, "", func(
		ctx context.Context, cursor string,
	) (platform.Page[*gh.PullRequestReview], error) {
		page, err := strconv.Atoi(cursor)
		if err != nil || page < 1 {
			page = 1
		}
		items, more, err := c.ListReviewsPage(ctx, owner, repo, number, page)
		if err != nil {
			return platform.Page[*gh.PullRequestReview]{}, err
		}
		if !more {
			return platform.Page[*gh.PullRequestReview]{Items: items, Exhausted: true}, nil
		}
		return platform.Page[*gh.PullRequestReview]{
			Items: items, NextCursor: strconv.Itoa(page + 1),
		}, nil
	})
}

func (c *Client) ListPullRequestReviewThreads(
	ctx context.Context,
	owner string,
	repo string,
	number int,
) ([]PullRequestReviewThread, error) {
	var threads []PullRequestReviewThread
	cursor := ""
	for {
		page, next, exhausted, err := c.ListInventoryReviewThreadsPage(
			ctx, c.platformHost, owner, repo, number, cursor,
		)
		if err != nil {
			return nil, err
		}
		for _, fragment := range page {
			if len(threads) > 0 && threads[len(threads)-1].NodeID == fragment.NodeID {
				threads[len(threads)-1].Comments = append(
					threads[len(threads)-1].Comments,
					fragment.Comments...,
				)
				continue
			}
			threads = append(threads, fragment)
		}
		if exhausted {
			return threads, nil
		}
		if next == "" || next == cursor {
			return nil, errors.New("review thread page did not advance its cursor")
		}
		cursor = next
	}
}

func githubReviewThreadCommentFromGraphQL(
	comment graphQLReviewThreadComment,
) PullRequestReviewThreadComment {
	next := PullRequestReviewThreadComment{
		NodeID:            comment.NodeID,
		DatabaseID:        FirstPositiveInt64(int64(comment.FullDatabaseID), int64(comment.DatabaseID)),
		SubjectType:       comment.SubjectType,
		Body:              comment.Body,
		AuthorAssociation: comment.AuthorAssociation,
		Path:              comment.Path,
		Line:              comment.Line,
		OriginalLine:      comment.OriginalLine,
		DiffHunk:          comment.DiffHunk,
		URL:               comment.URL,
		IsMinimized:       comment.IsMinimized,
		MinimizedReason:   comment.MinimizedReason,
		CreatedAt:         comment.CreatedAt,
		UpdatedAt:         comment.UpdatedAt,
	}
	if comment.Author != nil {
		next.AuthorLogin = comment.Author.Login
	}
	if comment.Commit != nil {
		next.CommitID = comment.Commit.OID
	}
	if comment.OriginalCommit != nil {
		next.OriginalCommitID = comment.OriginalCommit.OID
	}
	if comment.PullRequestReview != nil {
		next.ReviewDatabaseID = comment.PullRequestReview.DatabaseID
	}
	return next
}

func FirstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func (c *Client) ListCommits(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.RepositoryCommit, error) {
	all, err := collectPages(ctx, func(opts *gh.ListOptions) ([]*gh.RepositoryCommit, *gh.Response, error) {
		page, resp, err := c.gh.PullRequests.ListCommits(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("listing commits for %s/%s#%d: %w", owner, repo, number, err)
		}
		return page, resp, nil
	}, c.trackRate)
	if err != nil {
		return nil, err
	}
	return all, nil
}
