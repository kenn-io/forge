package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/platform"
	platformgithub "go.kenn.io/middleman/internal/platform/github"
)

// ListIssuesPage is the single owner of GitHub issue inventory requests and
// their normalization. It dispatches on the query: StateOpen drains the REST
// open-list endpoint, StateAll with ItemOrderUpdated runs the GraphQL
// maintenance scan bounded by the watermark, and StateAll with ItemOrderCreated
// runs the GraphQL historical scan. Request construction and normalization for
// all three shapes live here so live collectors and archive workers share one
// implementation.
func (p *gitHubClientProvider) ListIssuesPage(
	ctx context.Context,
	ref platform.RepoRef,
	query platform.ItemPageQuery,
) (platform.Page[platform.Issue], error) {
	if query.State == platform.ItemStateOpen {
		return p.listOpenIssuesPage(ctx, ref)
	}
	if query.Order == platform.ItemOrderUpdated {
		since := time.Time{}
		if query.UpdatedSince != nil {
			since = query.UpdatedSince.UTC()
		}
		return p.listInventoryIssuesPage(ctx, ref, query.Cursor, "updated_issues", "updated", since)
	}
	return p.listInventoryIssuesPage(ctx, ref, query.Cursor, "historical_issues", "created", time.Time{})
}

// ListMergeRequestsPage is the single owner of GitHub merge-request inventory
// requests and normalization, dispatching on the query the same way
// ListIssuesPage does. GitHub serves both historical and maintenance
// merge-request scans from the REST pull-request list with different sort
// fences, which stay inside this method.
func (p *gitHubClientProvider) ListMergeRequestsPage(
	ctx context.Context,
	ref platform.RepoRef,
	query platform.ItemPageQuery,
) (platform.Page[platform.MergeRequest], error) {
	if query.State == platform.ItemStateOpen {
		return p.listOpenMergeRequestsPage(ctx, ref)
	}
	if query.Order == platform.ItemOrderUpdated {
		since := time.Time{}
		if query.UpdatedSince != nil {
			since = query.UpdatedSince.UTC()
		}
		return p.listInventoryMergeRequestsPage(
			ctx, ref, query.Cursor, "updated_merge_requests", "updated", since,
		)
	}
	return p.listInventoryMergeRequestsPage(
		ctx, ref, query.Cursor, "historical_merge_requests", "created", time.Time{},
	)
}

// LookupIssue fetches a single issue and returns a typed outcome: present,
// moved (repository transfer detected from the returned repository URL),
// removed, or inaccessible. Provider probing and error classification happen
// once here so both live callers (which require present) and archive callers
// (which record every outcome) share one lookup.
func (p *gitHubClientProvider) LookupIssue(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ItemLookup[platform.Issue], error) {
	issue, err := p.client.GetIssue(ctx, ref.Owner, ref.Name, number)
	if err != nil {
		return p.classifyIssueLookup(ctx, ref, err)
	}
	if destination := githubArchiveDestination(ref, issue.GetRepositoryURL()); destination != nil {
		return platform.ItemLookup[platform.Issue]{
			Outcome: platform.LookupMoved, Destination: destination,
		}, nil
	}
	item, err := platformgithub.NormalizeIssue(ref, issue)
	if err != nil {
		return platform.ItemLookup[platform.Issue]{}, err
	}
	return platform.ItemLookup[platform.Issue]{Outcome: platform.LookupPresent, Item: item}, nil
}

// LookupMergeRequest is the merge-request counterpart to LookupIssue.
func (p *gitHubClientProvider) LookupMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ItemLookup[platform.MergeRequest], error) {
	pr, err := p.client.GetPullRequest(ctx, ref.Owner, ref.Name, number)
	if err != nil {
		return p.classifyMergeRequestLookup(ctx, ref, err)
	}
	if destination := githubArchiveDestination(ref, pr.GetBase().GetRepo().GetURL()); destination != nil {
		return platform.ItemLookup[platform.MergeRequest]{
			Outcome: platform.LookupMoved, Destination: destination,
		}, nil
	}
	item, err := platformgithub.NormalizePullRequest(ref, pr)
	if err != nil {
		return platform.ItemLookup[platform.MergeRequest]{}, err
	}
	return platform.ItemLookup[platform.MergeRequest]{Outcome: platform.LookupPresent, Item: item}, nil
}

func (p *gitHubClientProvider) classifyIssueLookup(
	ctx context.Context,
	ref platform.RepoRef,
	err error,
) (platform.ItemLookup[platform.Issue], error) {
	mapped := p.archiveTransportError(platform.ArchiveCapabilityHistoricalIssues, err)
	if errors.Is(mapped, platform.ErrRateLimited) {
		return platform.ItemLookup[platform.Issue]{}, mapped
	}
	status := githubStatusCode(err)
	if status != http.StatusNotFound {
		if status == http.StatusForbidden || status == http.StatusUnauthorized {
			if _, repoErr := p.client.GetRepository(ctx, ref.Owner, ref.Name); repoErr != nil {
				return platform.ItemLookup[platform.Issue]{}, p.archiveRepositoryProbeError(repoErr)
			}
			return platform.ItemLookup[platform.Issue]{Outcome: platform.LookupInaccessible}, nil
		}
		return platform.ItemLookup[platform.Issue]{}, mapped
	}
	if _, repoErr := p.client.GetRepository(ctx, ref.Owner, ref.Name); repoErr != nil {
		return platform.ItemLookup[platform.Issue]{}, p.archiveRepositoryProbeError(repoErr)
	}
	return platform.ItemLookup[platform.Issue]{Outcome: platform.LookupRemoved}, nil
}

func (p *gitHubClientProvider) classifyMergeRequestLookup(
	ctx context.Context,
	ref platform.RepoRef,
	err error,
) (platform.ItemLookup[platform.MergeRequest], error) {
	mapped := p.archiveTransportError(platform.ArchiveCapabilityHistoricalMergeRequests, err)
	if errors.Is(mapped, platform.ErrRateLimited) {
		return platform.ItemLookup[platform.MergeRequest]{}, mapped
	}
	status := githubStatusCode(err)
	if status != http.StatusNotFound {
		if status == http.StatusForbidden || status == http.StatusUnauthorized {
			if _, repoErr := p.client.GetRepository(ctx, ref.Owner, ref.Name); repoErr != nil {
				return platform.ItemLookup[platform.MergeRequest]{}, p.archiveRepositoryProbeError(repoErr)
			}
			return platform.ItemLookup[platform.MergeRequest]{Outcome: platform.LookupInaccessible}, nil
		}
		return platform.ItemLookup[platform.MergeRequest]{}, mapped
	}
	if _, repoErr := p.client.GetRepository(ctx, ref.Owner, ref.Name); repoErr != nil {
		return platform.ItemLookup[platform.MergeRequest]{}, p.archiveRepositoryProbeError(repoErr)
	}
	return platform.ItemLookup[platform.MergeRequest]{Outcome: platform.LookupRemoved}, nil
}

// ListIssueCommentsPage returns one page of normalized issue comment events.
func (p *gitHubClientProvider) ListIssueCommentsPage(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	cursor string,
) (platform.Page[platform.IssueEvent], error) {
	client, state, err := p.archiveDetailPageClient(ref, number, cursor, "issue_comments", time.Time{})
	if err != nil {
		return platform.Page[platform.IssueEvent]{}, err
	}
	comments, more, err := client.ListIssueCommentsPage(ctx, ref.Owner, ref.Name, number, state.Page)
	if err != nil {
		return platform.Page[platform.IssueEvent]{}, p.archiveTransportError(platform.ArchiveCapabilityOrdinaryComments, err)
	}
	items := make([]platform.IssueEvent, 0, len(comments))
	for _, comment := range comments {
		items = append(items, platformgithub.NormalizeIssueCommentEvent(ref, number, comment))
	}
	return archivePageWithNext(items, state, more)
}

// ListMergeRequestCommentsPage returns one page of normalized merge-request
// comment events. Issue comments and merge-request comments share the same
// underlying REST reader; only the normalization differs.
func (p *gitHubClientProvider) ListMergeRequestCommentsPage(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	cursor string,
) (platform.Page[platform.MergeRequestEvent], error) {
	client, state, err := p.archiveDetailPageClient(ref, number, cursor, "merge_request_comments", time.Time{})
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, err
	}
	comments, more, err := client.ListIssueCommentsPage(ctx, ref.Owner, ref.Name, number, state.Page)
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, p.archiveTransportError(platform.ArchiveCapabilityOrdinaryComments, err)
	}
	items := make([]platform.MergeRequestEvent, 0, len(comments))
	for _, comment := range comments {
		items = append(items, platformgithub.NormalizeCommentEvent(ref, number, comment))
	}
	return archivePageWithNext(items, state, more)
}

// ListSubmittedReviewsPage returns one page of normalized submitted-review
// events, skipping pending drafts and reviews without a submission time.
func (p *gitHubClientProvider) ListSubmittedReviewsPage(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	cursor string,
) (platform.Page[platform.MergeRequestEvent], error) {
	client, state, err := p.archiveDetailPageClient(ref, number, cursor, "submitted_reviews", time.Time{})
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, err
	}
	reviews, more, err := client.ListReviewsPage(ctx, ref.Owner, ref.Name, number, state.Page)
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, p.archiveTransportError(platform.ArchiveCapabilitySubmittedReviews, err)
	}
	items := make([]platform.MergeRequestEvent, 0, len(reviews))
	for _, review := range reviews {
		if review == nil || review.SubmittedAt == nil || strings.EqualFold(review.GetState(), "PENDING") {
			continue
		}
		items = append(items, platformgithub.NormalizeReviewEvent(ref, number, review))
	}
	return archivePageWithNext(items, state, more)
}

// ListReviewThreadsPage returns one page of normalized inline review-thread
// comments. It uses a single GraphQL batch size (100 threads per page) shared
// by live and archive callers; nested per-thread comment continuation stays
// inside the opaque cursor.
func (p *gitHubClientProvider) ListReviewThreadsPage(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	cursor string,
) (platform.Page[platform.MergeRequestReviewThread], error) {
	client, err := p.archivePageClient()
	if err != nil {
		return platform.Page[platform.MergeRequestReviewThread]{}, err
	}
	if _, err := decodeGitHubArchiveReviewCursor(cursor, ref.Host, ref.Owner, ref.Name, number); err != nil {
		return platform.Page[platform.MergeRequestReviewThread]{}, platform.ProviderContract(
			platform.KindGitHub, p.host, "archive_cursor", err,
		)
	}
	threads, next, exhausted, err := client.ListArchiveReviewThreadsPage(
		ctx, ref.Host, ref.Owner, ref.Name, number, cursor,
	)
	if err != nil {
		return platform.Page[platform.MergeRequestReviewThread]{}, p.archiveTransportError(platform.ArchiveCapabilityInlineReviewComments, err)
	}
	items := make([]platform.MergeRequestReviewThread, 0)
	for _, thread := range threads {
		for _, comment := range thread.Comments {
			normalized := githubReviewThreadComment(thread, comment)
			if normalized.ProviderThreadID == "" || normalized.ProviderCommentID == "" {
				continue
			}
			normalized.Repo = ref
			normalized.MergeRequestNumber = number
			items = append(items, normalized)
		}
	}
	return platform.Page[platform.MergeRequestReviewThread]{
		Items: items, NextCursor: next, Exhausted: exhausted,
	}, nil
}

// listOpenIssuesPage returns every open issue as a single exhausted page. The
// REST open-list endpoint drains all pages internally, so open enumeration is
// not resumable through a cursor.
func (p *gitHubClientProvider) listOpenIssuesPage(
	ctx context.Context,
	ref platform.RepoRef,
) (platform.Page[platform.Issue], error) {
	issues, err := p.ListOpenGitHubIssues(ctx, ref)
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	out := make([]platform.Issue, 0, len(issues))
	for _, issue := range issues {
		normalized, err := platformgithub.NormalizeIssue(ref, issue)
		if err != nil {
			return platform.Page[platform.Issue]{}, err
		}
		out = append(out, normalized)
	}
	return platform.Page[platform.Issue]{Items: out, Exhausted: true}, nil
}

// listOpenMergeRequestsPage returns every open merge request as a single
// exhausted page.
func (p *gitHubClientProvider) listOpenMergeRequestsPage(
	ctx context.Context,
	ref platform.RepoRef,
) (platform.Page[platform.MergeRequest], error) {
	prs, err := p.client.ListOpenPullRequests(ctx, ref.Owner, ref.Name)
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	out := make([]platform.MergeRequest, 0, len(prs))
	for _, pr := range prs {
		mr, err := platformgithub.NormalizePullRequest(ref, pr)
		if err != nil {
			return platform.Page[platform.MergeRequest]{}, err
		}
		out = append(out, mr)
	}
	return platform.Page[platform.MergeRequest]{Items: out, Exhausted: true}, nil
}

// listInventoryIssuesPage owns the GraphQL historical and maintenance issue
// request shapes. The mode string binds the opaque cursor to this enumeration;
// sortBy selects the GraphQL order field; since bounds the maintenance scan.
func (p *gitHubClientProvider) listInventoryIssuesPage(
	ctx context.Context,
	ref platform.RepoRef,
	cursor string,
	mode string,
	sortBy string,
	since time.Time,
) (platform.Page[platform.Issue], error) {
	client, err := p.archivePageClient()
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	state, err := decodeGitHubArchiveCursor(cursor, ref, 0, mode, since)
	if err != nil {
		return platform.Page[platform.Issue]{}, platform.ProviderContract(
			platform.KindGitHub, p.host, "archive_cursor", err,
		)
	}
	querySince, err := githubArchiveIssueSince(mode, state.Since)
	if err != nil {
		return platform.Page[platform.Issue]{}, platform.ProviderContract(
			platform.KindGitHub, p.host, "archive_cursor", err,
		)
	}
	items, next, exhausted, err := client.ListArchiveIssuesPage(
		ctx, ref.Owner, ref.Name, sortBy, state.After, querySince,
	)
	if err != nil {
		return platform.Page[platform.Issue]{}, p.archiveTransportError(platform.ArchiveCapabilityHistoricalIssues, err)
	}
	out := make([]platform.Issue, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		normalized, err := platformgithub.NormalizeIssue(ref, item)
		if err != nil {
			return platform.Page[platform.Issue]{}, err
		}
		out = append(out, normalized)
	}
	if exhausted {
		return platform.Page[platform.Issue]{Items: out, Exhausted: true}, nil
	}
	state.After = next
	encoded, err := encodeGitHubArchiveCursor(state)
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	return platform.Page[platform.Issue]{Items: out, NextCursor: encoded}, nil
}

// githubArchiveIssueSince overlaps the maintenance issue scan by one second so
// the inclusive watermark contract is honored against GitHub's exclusive
// GraphQL since filter.
func githubArchiveIssueSince(mode, since string) (string, error) {
	if mode != "updated_issues" || since == "" {
		return since, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, since)
	if err != nil {
		return "", fmt.Errorf("parse issue maintenance watermark: %w", err)
	}
	return parsed.Add(-time.Second).Format(time.RFC3339Nano), nil
}

// listInventoryMergeRequestsPage owns the REST historical and maintenance
// merge-request request shapes. The historical scan traverses ascending by
// creation time; the maintenance scan traverses descending by update time and
// stops once it crosses the overlapped watermark.
func (p *gitHubClientProvider) listInventoryMergeRequestsPage(
	ctx context.Context,
	ref platform.RepoRef,
	cursor string,
	mode string,
	sortBy string,
	since time.Time,
) (platform.Page[platform.MergeRequest], error) {
	client, err := p.archivePageClient()
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	state, err := decodeGitHubArchiveCursor(cursor, ref, 0, mode, since)
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, platform.ProviderContract(
			platform.KindGitHub, p.host, "archive_cursor", err,
		)
	}
	items, hasMore, err := client.ListArchivePullRequestsPage(
		ctx, ref.Owner, ref.Name, sortBy, state.Page,
	)
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, p.archiveTransportError(platform.ArchiveCapabilityHistoricalMergeRequests, err)
	}
	out := make([]platform.MergeRequest, 0, len(items))
	crossedWatermark := false
	overlapStart := since.Add(-time.Second)
	for _, item := range items {
		normalized, err := platformgithub.NormalizePullRequest(ref, item)
		if err != nil {
			return platform.Page[platform.MergeRequest]{}, err
		}
		if mode == "updated_merge_requests" && normalized.UpdatedAt.Before(overlapStart) {
			crossedWatermark = true
			continue
		}
		out = append(out, normalized)
	}
	if crossedWatermark {
		return platform.Page[platform.MergeRequest]{Items: out, Exhausted: true}, nil
	}
	return archivePageWithNext(out, state, hasMore)
}

var (
	_ platform.IssuePageReader        = (*gitHubClientProvider)(nil)
	_ platform.MergeRequestPageReader = (*gitHubClientProvider)(nil)
)
