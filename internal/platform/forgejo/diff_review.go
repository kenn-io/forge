package forgejo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	forgejosdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/platform/gitealike"
)

func (c *Client) PublishDiffReviewDraft(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	input platform.PublishDiffReviewDraftInput,
) (*platform.PublishedDiffReview, error) {
	return c.transport.PublishDiffReviewDraft(ctx, c.host, ref, number, input)
}

func (c *Client) ListMergeRequestReviewThreads(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.MergeRequestReviewThread, error) {
	return c.transport.ListMergeRequestReviewThreads(ctx, ref, number)
}

func (t *transport) PublishDiffReviewDraft(
	ctx context.Context,
	host string,
	ref platform.RepoRef,
	number int,
	input platform.PublishDiffReviewDraftInput,
) (*platform.PublishedDiffReview, error) {
	comments := make([]forgejosdk.CreatePullReviewComment, 0, len(input.Comments))
	commitID := input.HeadSHA
	for _, comment := range input.Comments {
		if commitID == "" {
			commitID = comment.Range.CommitSHA
			if commitID == "" {
				commitID = comment.Range.DiffHeadSHA
			}
		}
		comments = append(comments, forgejoReviewComments(comment)...)
	}
	if input.Action == platform.ReviewActionApprove && commitID != "" {
		current, err := t.GetPullRequest(ctx, ref, number)
		if err != nil {
			return nil, err
		}
		if current.Head.SHA != "" && current.Head.SHA != commitID {
			return nil, forgejoStaleApprovalError(host, nil, nil)
		}
	}

	var review *forgejosdk.PullReview
	var resp *forgejosdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		review, resp, err = t.api.CreatePullReview(ref.Owner, ref.Name, int64(number), forgejosdk.CreatePullReviewOptions{
			State:    forgejoReviewState(input.Action),
			Body:     input.Body,
			CommitID: commitID,
			Comments: comments,
		})
		return err
	})
	if err != nil {
		return nil, forgejoHTTPError(resp, err)
	}
	if review == nil {
		return nil, fmt.Errorf("forgejo create pull review returned nil review")
	}
	published := &platform.PublishedDiffReview{
		ProviderReviewID: strconv.FormatInt(review.ID, 10),
		SubmittedAt:      review.Submitted.UTC(),
	}
	if input.Action == platform.ReviewActionApprove && commitID != "" {
		if err := t.deleteReviewIfHeadMoved(ctx, host, ref, number, commitID, review.ID); err != nil {
			if forgejoPartialReviewPublish(input, err) {
				return published, &platform.DiffReviewPublishPartialError{
					Err:                 err,
					PublishedCommentIDs: forgejoPublishedDraftCommentIDs(input.Comments),
				}
			}
			return nil, err
		}
	}
	return published, nil
}

func (t *transport) deleteReviewIfHeadMoved(
	ctx context.Context,
	host string,
	ref platform.RepoRef,
	number int,
	expectedHeadSHA string,
	reviewID int64,
) error {
	current, err := t.GetPullRequest(ctx, ref, number)
	verifyErr := forgejoPostSubmitHeadVerificationError(current, err)
	if verifyErr == nil && current.Head.SHA == expectedHeadSHA {
		return nil
	}
	reviewIDValue := strconv.FormatInt(reviewID, 10)
	cause := "moved_head"
	if verifyErr != nil {
		cause = "head_unverifiable"
	}
	failedRevocationMessage := fmt.Sprintf("approval %d may stand on a moved head", reviewID)
	successfulRevocationMessage := fmt.Sprintf(
		"head moved while the approval submitted; approval %d was deleted",
		reviewID,
	)
	if verifyErr != nil {
		failedRevocationMessage = fmt.Sprintf(
			"approval %d may stand because post-submit head could not be verified: %v",
			reviewID, verifyErr,
		)
		successfulRevocationMessage = fmt.Sprintf(
			"approval %d was deleted because post-submit head could not be verified: %v",
			reviewID, verifyErr,
		)
	}
	if deleteErr := t.DeletePullReview(ctx, ref, number, reviewID); deleteErr != nil {
		return forgejoStaleApprovalError(host, fmt.Errorf(
			"%s: deletion failed: %w",
			failedRevocationMessage, deleteErr,
		), map[string]string{"revocation": "failed", "review_id": reviewIDValue, "cause": cause})
	}
	return forgejoStaleApprovalError(host, errors.New(successfulRevocationMessage),
		map[string]string{"revocation": "succeeded", "review_id": reviewIDValue, "cause": cause})
}

func forgejoStaleApprovalError(host string, err error, details map[string]string) error {
	hint := ""
	if err != nil {
		hint = err.Error()
	}
	return &platform.Error{
		Code:         platform.ErrCodeStaleState,
		Provider:     platform.KindForgejo,
		PlatformHost: host,
		Capability:   "approve_merge_request",
		Hint:         hint,
		Details:      details,
		Err:          err,
	}
}

func forgejoPostSubmitHeadVerificationError(current gitealike.PullRequestDTO, err error) error {
	if err != nil {
		return fmt.Errorf("read failed: %w", err)
	}
	if current.Head.SHA == "" {
		return errors.New("provider returned no head sha")
	}
	return nil
}

func forgejoPartialReviewPublish(input platform.PublishDiffReviewDraftInput, err error) bool {
	if strings.TrimSpace(input.Body) == "" && len(input.Comments) == 0 {
		return false
	}
	var platformErr *platform.Error
	if !errors.As(err, &platformErr) {
		return false
	}
	return platformErr.Details["revocation"] == "failed"
}

func forgejoPublishedDraftCommentIDs(comments []platform.LocalDiffReviewDraftComment) []int64 {
	ids := make([]int64, 0, len(comments))
	for _, comment := range comments {
		if comment.ID > 0 {
			ids = append(ids, comment.ID)
		}
	}
	return ids
}

func (t *transport) ListMergeRequestReviewThreads(
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
			threads = append(threads, forgejoReviewThread(review, comment))
		}
	}
	return threads, nil
}

func (t *transport) listAllPullReviews(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]*forgejosdk.PullReview, error) {
	t.spendSyncBudget(ctx)
	var out []*forgejosdk.PullReview
	page := 1
	for {
		var reviews []*forgejosdk.PullReview
		var resp *forgejosdk.Response
		err := t.withRequestContext(ctx, func() error {
			var err error
			reviews, resp, err = t.api.ListPullReviews(ref.Owner, ref.Name, int64(number), forgejosdk.ListPullReviewsOptions{
				ListOptions: forgejosdk.ListOptions{Page: page, PageSize: 100},
			})
			return err
		})
		if err != nil {
			return nil, forgejoHTTPError(resp, err)
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
) ([]*forgejosdk.PullReviewComment, error) {
	t.spendSyncBudget(ctx)
	var comments []*forgejosdk.PullReviewComment
	var resp *forgejosdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		comments, resp, err = t.api.ListPullReviewComments(ref.Owner, ref.Name, int64(number), reviewID)
		return err
	})
	if err != nil {
		return nil, forgejoHTTPError(resp, err)
	}
	return comments, nil
}

func forgejoReviewState(action platform.ReviewAction) forgejosdk.ReviewStateType {
	switch action {
	case platform.ReviewActionApprove:
		return forgejosdk.ReviewStateApproved
	case platform.ReviewActionRequestChanges:
		return forgejosdk.ReviewStateRequestChanges
	default:
		return forgejosdk.ReviewStateComment
	}
}

func forgejoReviewComments(comment platform.LocalDiffReviewDraftComment) []forgejosdk.CreatePullReviewComment {
	next := forgejosdk.CreatePullReviewComment{
		Path: comment.Range.Path,
		Body: comment.Body,
	}
	if comment.Range.Side == "left" {
		next.OldLineNum = int64(comment.Range.Line)
	} else {
		next.NewLineNum = int64(comment.Range.Line)
	}
	return []forgejosdk.CreatePullReviewComment{next}
}

func forgejoReviewThread(
	review *forgejosdk.PullReview,
	comment *forgejosdk.PullReviewComment,
) platform.MergeRequestReviewThread {
	if review == nil {
		review = &forgejosdk.PullReview{}
	}
	if comment == nil {
		comment = &forgejosdk.PullReviewComment{}
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
	resolvedAt := (*time.Time)(nil)
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
