package gitealike

import (
	"context"
	"time"

	"go.kenn.io/middleman/internal/platform"
)

// ListHistoricalIssues and the other ArchiveReader methods on this provider are
// thin delegates over the canonical page/lookup surface in pages.go. The
// ArchiveReader interface still exists for internal/archive consumers; these
// wrappers are removed when that interface is deleted.
func (p *Provider) ListHistoricalIssues(
	ctx context.Context,
	ref platform.RepoRef,
	cursor string,
) (platform.Page[platform.Issue], error) {
	return p.ListIssuesPage(ctx, ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderCreated, Cursor: cursor,
	})
}

func (p *Provider) ListHistoricalMergeRequests(
	ctx context.Context,
	ref platform.RepoRef,
	cursor string,
) (platform.Page[platform.MergeRequest], error) {
	return p.ListMergeRequestsPage(ctx, ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderCreated, Cursor: cursor,
	})
}

func (p *Provider) ListUpdatedIssues(
	ctx context.Context,
	ref platform.RepoRef,
	since time.Time,
	cursor string,
) (platform.Page[platform.Issue], error) {
	return p.ListIssuesPage(ctx, ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderUpdated,
		UpdatedSince: &since, Cursor: cursor,
	})
}

func (p *Provider) ListUpdatedMergeRequests(
	ctx context.Context,
	ref platform.RepoRef,
	since time.Time,
	cursor string,
) (platform.Page[platform.MergeRequest], error) {
	return p.ListMergeRequestsPage(ctx, ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderUpdated,
		UpdatedSince: &since, Cursor: cursor,
	})
}

func (p *Provider) GetArchiveIssue(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ItemLookup[platform.Issue], error) {
	return p.LookupIssue(ctx, ref, number)
}

func (p *Provider) GetArchiveMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ItemLookup[platform.MergeRequest], error) {
	return p.LookupMergeRequest(ctx, ref, number)
}

func (p *Provider) ListArchiveIssueComments(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	cursor string,
) (platform.Page[platform.IssueEvent], error) {
	return p.ListIssueCommentsPage(ctx, ref, number, cursor)
}

func (p *Provider) ListArchiveMergeRequestComments(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	cursor string,
) (platform.Page[platform.MergeRequestEvent], error) {
	return p.ListMergeRequestCommentsPage(ctx, ref, number, cursor)
}

func (p *Provider) ListArchiveSubmittedReviews(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	cursor string,
) (platform.Page[platform.MergeRequestEvent], error) {
	return p.ListSubmittedReviewsPage(ctx, ref, number, cursor)
}

func (p *Provider) ListArchiveReviewThreads(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	cursor string,
) (platform.Page[platform.MergeRequestReviewThread], error) {
	return p.ListReviewThreadsPage(ctx, ref, number, cursor)
}

var _ platform.ArchiveReader = (*Provider)(nil)
