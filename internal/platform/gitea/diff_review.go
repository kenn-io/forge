package gitea

import (
	"context"
	"strconv"
	"time"

	giteasdk "code.gitea.io/sdk/gitea"
	"go.kenn.io/middleman/internal/platform"
)

func (c *Client) ListMergeRequestReviewThreads(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.MergeRequestReviewThread, error) {
	if !c.readReviewThreads {
		return nil, platform.UnsupportedCapability(
			platform.KindGitea, c.host, "read_review_threads",
		)
	}
	return c.transport.listMergeRequestReviewThreads(ctx, ref, number)
}

func (t *transport) listMergeRequestReviewThreads(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.MergeRequestReviewThread, error) {
	reviews, err := t.listAllPullReviews(ctx, ref, number)
	if err != nil {
		return nil, err
	}
	threads := make([]platform.MergeRequestReviewThread, 0)
	for _, review := range reviews {
		comments, err := t.listPullReviewComments(ctx, ref, number, review.ID)
		if err != nil {
			return nil, err
		}
		for _, comment := range comments {
			threads = append(threads, giteaReviewThread(review, comment))
		}
	}
	return threads, nil
}

func (t *transport) listAllPullReviews(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]*giteasdk.PullReview, error) {
	var out []*giteasdk.PullReview
	page := 1
	for {
		var reviews []*giteasdk.PullReview
		var resp *giteasdk.Response
		err := t.withRequestContext(ctx, func() error {
			var err error
			reviews, resp, err = t.api.ListPullReviews(
				ref.Owner, ref.Name, int64(number),
				giteasdk.ListPullReviewsOptions{
					ListOptions: giteasdk.ListOptions{Page: page, PageSize: 100},
				},
			)
			return err
		})
		if err != nil {
			return nil, giteaHTTPError(resp, err)
		}
		out = append(out, reviews...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		page = resp.NextPage
	}
}

func (t *transport) listPullReviewComments(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	reviewID int64,
) ([]*giteasdk.PullReviewComment, error) {
	var comments []*giteasdk.PullReviewComment
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		comments, resp, err = t.api.ListPullReviewComments(
			ref.Owner, ref.Name, int64(number), reviewID,
		)
		return err
	})
	if err != nil {
		return nil, giteaHTTPError(resp, err)
	}
	return comments, nil
}

func giteaReviewThread(
	review *giteasdk.PullReview,
	comment *giteasdk.PullReviewComment,
) platform.MergeRequestReviewThread {
	if review == nil {
		review = &giteasdk.PullReview{}
	}
	if comment == nil {
		comment = &giteasdk.PullReviewComment{}
	}
	line := int(comment.LineNum)
	side := "right"
	lineType := "add"
	var oldLine *int
	var newLine *int
	if comment.OldLineNum > 0 {
		line = int(comment.OldLineNum)
		side = "left"
		lineType = "delete"
		oldLine = &line
	} else {
		newLine = &line
	}
	var resolvedAt *time.Time
	resolved := comment.Resolver != nil
	if resolved {
		updated := comment.Updated.UTC()
		resolvedAt = &updated
	}
	return platform.MergeRequestReviewThread{
		ProviderThreadID:  strconv.FormatInt(comment.ID, 10),
		ProviderReviewID:  strconv.FormatInt(review.ID, 10),
		ProviderCommentID: strconv.FormatInt(comment.ID, 10),
		Body:              comment.Body,
		AuthorLogin:       convertUser(comment.Reviewer).UserName,
		DirectURL:         comment.HTMLURL,
		Range: platform.DiffReviewLineRange{
			Path:        comment.Path,
			Side:        side,
			Line:        line,
			OldLine:     oldLine,
			NewLine:     newLine,
			LineType:    lineType,
			DiffHeadSHA: comment.CommitID,
			CommitSHA:   comment.CommitID,
		},
		Resolved:   resolved,
		CreatedAt:  comment.Created.UTC(),
		UpdatedAt:  comment.Updated.UTC(),
		ResolvedAt: resolvedAt,
	}
}
