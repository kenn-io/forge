package gitealike

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/platform"
)

// pageCursor binds an opaque resumable cursor to the enumeration that produced
// it: the query mode, the repository identity, the item number for detail
// datasets, and the maintenance watermark. Reusing a cursor with a different
// shape is a provider-contract violation. Before pins the wall-clock upper
// bound of the traversal so restarts do not re-deliver items created or
// updated after the scan began.
type pageCursor struct {
	Mode   string `json:"mode"`
	Repo   string `json:"repo"`
	Number int    `json:"number,omitempty"`
	Page   int    `json:"page"`
	Since  string `json:"since,omitempty"`
	Before string `json:"before,omitempty"`
}

// ListIssuesPage is the single owner of Forgejo/Gitea issue inventory requests
// and their normalization. It dispatches on the query: StateOpen drains the
// open list into one exhausted page, StateAll ordered-by-created walks the
// unsorted issue endpoint backward from its last page so items surface
// created-ascending, and StateAll ordered-by-updated applies the inclusive
// watermark through the endpoint's since filter.
func (p *Provider) ListIssuesPage(
	ctx context.Context,
	ref platform.RepoRef,
	query platform.ItemPageQuery,
) (platform.Page[platform.Issue], error) {
	if err := platform.ValidateItemPageQuery(query); err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	if query.State == platform.ItemStateOpen {
		return p.listOpenIssuesPage(ctx, ref)
	}
	if query.Order == platform.ItemOrderUpdated {
		since := time.Time{}
		if query.UpdatedSince != nil {
			since = query.UpdatedSince.UTC()
		}
		return p.listInventoryIssuesPage(ctx, ref, since, query.Cursor, "updated_issues")
	}
	return p.listInventoryIssuesPage(ctx, ref, time.Time{}, query.Cursor, "historical_issues")
}

// ListMergeRequestsPage is the merge-request counterpart to ListIssuesPage.
// The pulls endpoint has stable sort modes, so created-order traversal pages
// forward under "oldest" and the maintenance traversal pages "recentupdate"
// descending until it crosses the overlapped watermark.
func (p *Provider) ListMergeRequestsPage(
	ctx context.Context,
	ref platform.RepoRef,
	query platform.ItemPageQuery,
) (platform.Page[platform.MergeRequest], error) {
	if err := platform.ValidateItemPageQuery(query); err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	if query.State == platform.ItemStateOpen {
		return p.listOpenMergeRequestsPage(ctx, ref)
	}
	if query.Order == platform.ItemOrderUpdated {
		since := time.Time{}
		if query.UpdatedSince != nil {
			since = query.UpdatedSince.UTC()
		}
		return p.listInventoryMergeRequestsPage(ctx, ref, since, query.Cursor, "updated_merge_requests")
	}
	return p.listInventoryMergeRequestsPage(ctx, ref, time.Time{}, query.Cursor, "historical_merge_requests")
}

// listOpenIssuesPage returns every open issue as a single exhausted page. Open
// scans are single-shot current-index reads, so the internal page drain is not
// resumable through a caller cursor. The issues endpoint interleaves pull
// requests, which are filtered here.
func (p *Provider) listOpenIssuesPage(
	ctx context.Context,
	ref platform.RepoRef,
) (platform.Page[platform.Issue], error) {
	items, err := collectTransportPages(ctx, func(ctx context.Context, opts PageOptions) ([]IssueDTO, Page, error) {
		return p.transport.ListOpenIssues(ctx, ref, opts)
	})
	if err != nil {
		return platform.Page[platform.Issue]{}, p.mapError(err)
	}
	out := make([]platform.Issue, 0, len(items))
	for _, item := range items {
		if item.IsPullRequest {
			continue
		}
		out = append(out, NormalizeIssue(ref, item))
	}
	return platform.Page[platform.Issue]{Items: out, Exhausted: true}, nil
}

// listOpenMergeRequestsPage returns every open merge request as a single
// exhausted page.
func (p *Provider) listOpenMergeRequestsPage(
	ctx context.Context,
	ref platform.RepoRef,
) (platform.Page[platform.MergeRequest], error) {
	items, err := collectTransportPages(ctx, func(ctx context.Context, opts PageOptions) ([]PullRequestDTO, Page, error) {
		return p.transport.ListOpenPullRequests(ctx, ref, opts)
	})
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, p.mapError(err)
	}
	out := make([]platform.MergeRequest, 0, len(items))
	for _, item := range items {
		out = append(out, NormalizePullRequest(ref, item))
	}
	return platform.Page[platform.MergeRequest]{Items: out, Exhausted: true}, nil
}

// listInventoryIssuesPage owns the historical and maintenance issue request
// shapes over the archive transport.
func (p *Provider) listInventoryIssuesPage(
	ctx context.Context,
	ref platform.RepoRef,
	since time.Time,
	encoded string,
	mode string,
) (platform.Page[platform.Issue], error) {
	t, err := p.archiveTransport()
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	cursor, err := p.decodePageCursor(ref, 0, mode, since, encoded)
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	items, page, err := t.ListArchiveIssues(ctx, ref, ArchiveListOptions{
		PageOptions: PageOptions{Page: cursor.Page, PageSize: defaultPageSize},
		Since:       inclusiveWatermark(since), Before: parseCursorTime(cursor.Before),
	})
	if err != nil {
		return platform.Page[platform.Issue]{}, p.mapError(err)
	}
	// The Gitea-compatible issue endpoint has no sort parameter and returns
	// newest pages first. The first request of a fresh traversal discovers the
	// final page; each subsequent request walks backward and reverses that
	// page. The discovery jump is gated on the fresh call: a resumed cursor at
	// page one is the final backward step and must consume, not re-discover,
	// or the walk would cycle 1 -> last forever.
	if encoded == "" && cursor.Page == 1 && page.Last > 1 {
		cursor.Page = page.Last
		return progressCursorPage[platform.Issue](cursor)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Created.Equal(items[j].Created) {
			return items[i].Index < items[j].Index
		}
		return items[i].Created.Before(items[j].Created)
	})
	out := make([]platform.Issue, 0, len(items))
	for _, item := range items {
		if item.IsPullRequest {
			continue
		}
		out = append(out, NormalizeIssue(ref, item))
	}
	if cursor.Page <= 1 {
		return platform.Page[platform.Issue]{Items: out, Exhausted: true}, nil
	}
	cursor.Page--
	return nextCursorPage(out, cursor, len(out) == 0)
}

// listInventoryMergeRequestsPage owns the historical and maintenance
// merge-request request shapes over the archive transport. A zero since runs
// the created-order "oldest" traversal; a nonzero since runs the
// "recentupdate" maintenance traversal, which stops once a page crosses the
// overlapped inclusive watermark.
func (p *Provider) listInventoryMergeRequestsPage(
	ctx context.Context,
	ref platform.RepoRef,
	since time.Time,
	encoded string,
	mode string,
) (platform.Page[platform.MergeRequest], error) {
	t, err := p.archiveTransport()
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	cursor, err := p.decodePageCursor(ref, 0, mode, since, encoded)
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	sortMode := "oldest"
	if !since.IsZero() {
		sortMode = "recentupdate"
	}
	items, page, err := t.ListArchivePullRequests(ctx, ref, ArchiveListOptions{
		PageOptions: PageOptions{Page: cursor.Page, PageSize: defaultPageSize}, Sort: sortMode,
	})
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, p.mapError(err)
	}
	out := make([]platform.MergeRequest, 0, len(items))
	crossedWatermark := false
	overlap := inclusiveWatermark(since)
	for _, item := range items {
		watermarkValue := item.Created
		if !since.IsZero() {
			watermarkValue = item.Updated
		}
		if watermarkValue.After(parseCursorTime(cursor.Before)) {
			continue
		}
		if !since.IsZero() && item.Updated.Before(overlap) {
			crossedWatermark = true
			continue
		}
		out = append(out, NormalizePullRequest(ref, item))
	}
	if page.Next == 0 || crossedWatermark {
		return platform.Page[platform.MergeRequest]{Items: out, Exhausted: true}, nil
	}
	cursor.Page = page.Next
	return nextCursorPage(out, cursor, len(out) == 0)
}

// LookupIssue fetches a single issue and returns a typed outcome. Provider
// probing and error classification happen once here so both live callers
// (which require present) and archive callers (which record every outcome)
// share one lookup.
func (p *Provider) LookupIssue(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ItemLookup[platform.Issue], error) {
	if err := p.validItemNumber(number); err != nil {
		return platform.ItemLookup[platform.Issue]{}, err
	}
	item, err := p.transport.GetIssue(ctx, ref, number)
	if err != nil {
		outcome, classifyErr := p.classifyLookupOutcome(ctx, ref, err)
		return platform.ItemLookup[platform.Issue]{Outcome: outcome}, classifyErr
	}
	return platform.ItemLookup[platform.Issue]{
		Outcome: platform.LookupPresent, Item: NormalizeIssue(ref, item),
	}, nil
}

// LookupMergeRequest is the merge-request counterpart to LookupIssue.
func (p *Provider) LookupMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ItemLookup[platform.MergeRequest], error) {
	if err := p.validItemNumber(number); err != nil {
		return platform.ItemLookup[platform.MergeRequest]{}, err
	}
	item, err := p.transport.GetPullRequest(ctx, ref, number)
	if err != nil {
		outcome, classifyErr := p.classifyLookupOutcome(ctx, ref, err)
		return platform.ItemLookup[platform.MergeRequest]{Outcome: outcome}, classifyErr
	}
	return platform.ItemLookup[platform.MergeRequest]{
		Outcome: platform.LookupPresent, Item: NormalizePullRequest(ref, item),
	}, nil
}

// classifyLookupOutcome maps a failed single-item fetch onto the canonical
// lookup outcomes, spending at most one repository probe to distinguish
// item-level access loss from repository-level access loss.
func (p *Provider) classifyLookupOutcome(
	ctx context.Context,
	ref platform.RepoRef,
	err error,
) (platform.LookupOutcome, error) {
	mapped := p.mapError(err)
	if errors.Is(mapped, platform.ErrPermissionDenied) {
		_, repoErr := p.transport.GetRepository(ctx, ref.Owner, ref.Name)
		if repoErr == nil {
			return platform.LookupInaccessible, nil
		}
		mappedRepoErr := p.mapError(repoErr)
		if errors.Is(mappedRepoErr, platform.ErrPermissionDenied) || errors.Is(mappedRepoErr, platform.ErrNotFound) {
			return "", platform.PermissionDenied(p.kind, p.host, repoErr)
		}
		return "", mappedRepoErr
	}
	if !errors.Is(mapped, platform.ErrNotFound) {
		return "", mapped
	}
	_, repoErr := p.transport.GetRepository(ctx, ref.Owner, ref.Name)
	if repoErr == nil {
		// Neither SDK exposes a transfer destination on item lookup. An item
		// 404 in an accessible source repository is therefore terminal removal.
		return platform.LookupRemoved, nil
	}
	mappedRepoErr := p.mapError(repoErr)
	if errors.Is(mappedRepoErr, platform.ErrPermissionDenied) || errors.Is(mappedRepoErr, platform.ErrNotFound) {
		return "", platform.PermissionDenied(p.kind, p.host, repoErr)
	}
	return "", mappedRepoErr
}

// ListIssueCommentsPage returns one page of normalized issue comment events.
func (p *Provider) ListIssueCommentsPage(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	encoded string,
) (platform.Page[platform.IssueEvent], error) {
	if err := p.validItemNumber(number); err != nil {
		return platform.Page[platform.IssueEvent]{}, err
	}
	cursor, err := p.decodePageCursor(ref, number, "issue_comments", time.Time{}, encoded)
	if err != nil {
		return platform.Page[platform.IssueEvent]{}, err
	}
	comments, page, err := p.transport.ListIssueComments(ctx, ref, number, PageOptions{Page: cursor.Page, PageSize: defaultPageSize})
	if err != nil {
		return platform.Page[platform.IssueEvent]{}, p.mapError(err)
	}
	return detailCursorPage(NormalizeIssueComments(p.kind, ref, number, comments), cursor, page)
}

// ListMergeRequestCommentsPage returns one page of normalized ordinary
// merge-request comment events.
func (p *Provider) ListMergeRequestCommentsPage(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	encoded string,
) (platform.Page[platform.MergeRequestEvent], error) {
	if err := p.validItemNumber(number); err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, err
	}
	cursor, err := p.decodePageCursor(ref, number, "merge_request_comments", time.Time{}, encoded)
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, err
	}
	comments, page, err := p.transport.ListPullRequestComments(ctx, ref, number, PageOptions{Page: cursor.Page, PageSize: defaultPageSize})
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, p.mapError(err)
	}
	return detailCursorPage(NormalizeMergeRequestEvents(p.kind, ref, number, comments, nil, nil), cursor, page)
}

// ListSubmittedReviewsPage returns one page of normalized submitted-review
// events. Forgejo and Gitea model review requests and pending drafts as review
// rows, so only genuinely submitted states survive the filter; a filtered-empty
// page is marked as explicit progress.
func (p *Provider) ListSubmittedReviewsPage(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	encoded string,
) (platform.Page[platform.MergeRequestEvent], error) {
	if err := p.validItemNumber(number); err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, err
	}
	cursor, err := p.decodePageCursor(ref, number, "submitted_reviews", time.Time{}, encoded)
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, err
	}
	reviews, page, err := p.transport.ListPullRequestReviews(ctx, ref, number, PageOptions{Page: cursor.Page, PageSize: defaultPageSize})
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, p.mapError(err)
	}
	submitted := make([]ReviewDTO, 0, len(reviews))
	for _, review := range reviews {
		if review.Submitted.IsZero() || !isSubmittedReviewState(review.State) {
			continue
		}
		submitted = append(submitted, review)
	}
	return detailCursorPage(NormalizeMergeRequestEvents(p.kind, ref, number, nil, submitted, nil), cursor, page)
}

func isSubmittedReviewState(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "APPROVED", "COMMENT", "REQUEST_CHANGES":
		return true
	default:
		return false
	}
}

// ListReviewThreadsPage is a typed unsupported capability: neither SDK exposes
// a usable inline review-thread dataset.
func (p *Provider) ListReviewThreadsPage(
	context.Context,
	platform.RepoRef,
	int,
	string,
) (platform.Page[platform.MergeRequestReviewThread], error) {
	return platform.Page[platform.MergeRequestReviewThread]{}, platform.UnsupportedCapability(
		p.kind, p.host, string(platform.ArchiveCapabilityInlineReviewComments),
	)
}

// validItemNumber rejects nonpositive item numbers before any provider call:
// the issues and pulls endpoints route them onto unrelated collection URLs
// instead of failing, so they must never reach the transport.
func (p *Provider) validItemNumber(number int) error {
	if number <= 0 {
		return &platform.Error{
			Code: platform.ErrCodeInvalidArgument, Provider: p.kind, PlatformHost: p.host,
			Field: "item_number", Err: fmt.Errorf("item number must be positive: %d", number),
		}
	}
	return nil
}

func detailCursorPage[T any](items []T, cursor pageCursor, page Page) (platform.Page[T], error) {
	if page.Next == 0 {
		return platform.Page[T]{Items: items, Exhausted: true}, nil
	}
	cursor.Page = page.Next
	return nextCursorPage(items, cursor, len(items) == 0)
}

func nextCursorPage[T any](items []T, cursor pageCursor, progressOnly bool) (platform.Page[T], error) {
	next, err := encodePageCursor(cursor)
	if err != nil {
		return platform.Page[T]{}, err
	}
	return platform.Page[T]{Items: items, NextCursor: next, ProgressOnly: progressOnly}, nil
}

func progressCursorPage[T any](cursor pageCursor) (platform.Page[T], error) {
	return nextCursorPage([]T{}, cursor, true)
}

func (p *Provider) archiveTransport() (ArchiveTransport, error) {
	t, ok := p.transport.(ArchiveTransport)
	if !ok {
		return nil, platform.UnsupportedCapability(p.kind, p.host, string(platform.ArchiveCapabilityHistoricalIssues))
	}
	return t, nil
}

func (p *Provider) decodePageCursor(ref platform.RepoRef, number int, mode string, since time.Time, encoded string) (pageCursor, error) {
	expected := pageCursor{Mode: mode, Repo: cursorRepoKey(p.kind, p.host, ref), Number: number, Page: 1}
	if !since.IsZero() {
		expected.Since = since.UTC().Format(time.RFC3339Nano)
	}
	if encoded == "" {
		expected.Before = time.Now().UTC().Format(time.RFC3339Nano)
		return expected, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return pageCursor{}, p.pageCursorError(err)
	}
	var cursor pageCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return pageCursor{}, p.pageCursorError(err)
	}
	if cursor.Mode != expected.Mode || cursor.Repo != expected.Repo || cursor.Number != expected.Number || cursor.Since != expected.Since || cursor.Page < 1 || cursor.Before == "" {
		return pageCursor{}, p.pageCursorError(errors.New("cursor does not match page enumeration"))
	}
	return cursor, nil
}

func (p *Provider) pageCursorError(err error) error {
	return platform.ProviderContract(p.kind, p.host, "archive_cursor", err)
}

func encodePageCursor(cursor pageCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode page cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func cursorRepoKey(kind platform.Kind, host string, ref platform.RepoRef) string {
	return strings.Join([]string{string(kind), host, string(ref.Platform), ref.Host, ref.Owner, ref.Name}, "\x00")
}

func inclusiveWatermark(since time.Time) time.Time {
	if since.IsZero() {
		return time.Time{}
	}
	return since.UTC().Add(-time.Second)
}

func parseCursorTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

// collectTransportPages drains a numeric-page transport list through the
// bounded neutral collector, so every whole-dataset read shares one
// cycle-guarded, page-capped drain instead of a hand-rolled loop. Transport
// errors surface unmapped; callers apply their own error mapping.
func collectTransportPages[T any](
	ctx context.Context,
	fetch func(context.Context, PageOptions) ([]T, Page, error),
) ([]T, error) {
	return platform.CollectPages(ctx, "1", func(ctx context.Context, cursor string) (platform.Page[T], error) {
		pageNumber, err := strconv.Atoi(cursor)
		if err != nil {
			return platform.Page[T]{}, fmt.Errorf("parse gitealike page cursor: %w", err)
		}
		items, next, err := fetch(ctx, PageOptions{Page: pageNumber, PageSize: defaultPageSize})
		if err != nil {
			return platform.Page[T]{}, err
		}
		nextPage := NextPage(next.Next)
		if nextPage == 0 {
			return platform.Page[T]{Items: items, Exhausted: true}, nil
		}
		return platform.Page[T]{Items: items, NextCursor: strconv.Itoa(nextPage)}, nil
	})
}

var (
	_ platform.IssuePageReader        = (*Provider)(nil)
	_ platform.MergeRequestPageReader = (*Provider)(nil)
)
