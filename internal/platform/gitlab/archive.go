package gitlab

import (
	"context"
	"time"

	"go.kenn.io/middleman/internal/platform"
)

// ListHistoricalIssues and the other ArchiveReader methods on this client are
// thin delegates over the canonical page/lookup surface in pages.go. The
// ArchiveReader interface still exists for internal/archive consumers; these
// wrappers are removed when that interface is deleted.
func (c *Client) ListHistoricalIssues(
	ctx context.Context,
	ref platform.RepoRef,
	cursor string,
) (platform.Page[platform.Issue], error) {
	return c.ListIssuesPage(ctx, ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderCreated, Cursor: cursor,
	})
}

func (c *Client) ListHistoricalMergeRequests(
	ctx context.Context,
	ref platform.RepoRef,
	cursor string,
) (platform.Page[platform.MergeRequest], error) {
	return c.ListMergeRequestsPage(ctx, ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderCreated, Cursor: cursor,
	})
}

func (c *Client) ListUpdatedIssues(
	ctx context.Context,
	ref platform.RepoRef,
	since time.Time,
	cursor string,
) (platform.Page[platform.Issue], error) {
	return c.ListIssuesPage(ctx, ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderUpdated,
		UpdatedSince: &since, Cursor: cursor,
	})
}

func (c *Client) ListUpdatedMergeRequests(
	ctx context.Context,
	ref platform.RepoRef,
	since time.Time,
	cursor string,
) (platform.Page[platform.MergeRequest], error) {
	return c.ListMergeRequestsPage(ctx, ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderUpdated,
		UpdatedSince: &since, Cursor: cursor,
	})
}

func (c *Client) GetArchiveIssue(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ItemLookup[platform.Issue], error) {
	return c.LookupIssue(ctx, ref, number)
}

func (c *Client) GetArchiveMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ItemLookup[platform.MergeRequest], error) {
	return c.LookupMergeRequest(ctx, ref, number)
}

func (c *Client) ListArchiveIssueComments(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	cursor string,
) (platform.Page[platform.IssueEvent], error) {
	return c.ListIssueCommentsPage(ctx, ref, number, cursor)
}

func (c *Client) ListArchiveMergeRequestComments(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	cursor string,
) (platform.Page[platform.MergeRequestEvent], error) {
	return c.ListMergeRequestCommentsPage(ctx, ref, number, cursor)
}

func (c *Client) ListArchiveSubmittedReviews(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	cursor string,
) (platform.Page[platform.MergeRequestEvent], error) {
	return c.ListSubmittedReviewsPage(ctx, ref, number, cursor)
}

func (c *Client) ListArchiveReviewThreads(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	cursor string,
) (platform.Page[platform.MergeRequestReviewThread], error) {
	return c.ListReviewThreadsPage(ctx, ref, number, cursor)
}

var _ platform.ArchiveReader = (*Client)(nil)
