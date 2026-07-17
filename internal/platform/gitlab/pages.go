package gitlab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"go.kenn.io/middleman/internal/platform"
)

// gitLabPageCursor binds an opaque resumable cursor to the enumeration that
// produced it: the query mode, the repository identity, the item number for
// detail datasets, and the maintenance watermark. Reusing a cursor with a
// different shape is a provider-contract violation. Exactly one continuation
// form is populated per traversal kind: Page for offset traversals, Link for
// keyset traversals, and Page plus an optional Window for the windowed
// created-order merge-request traversal.
type gitLabPageCursor struct {
	Mode     string `json:"mode"`
	Host     string `json:"host"`
	RepoPath string `json:"repo_path"`
	Number   int    `json:"number,omitempty"`
	Page     int64  `json:"page,omitempty"`
	Since    string `json:"since,omitempty"`
	// Link is the provider-issued keyset next-page link; only its query
	// parameters are ever applied, against this client's own base URL.
	Link string `json:"link,omitempty"`
	// Window is the created_after lower bound of the current windowed
	// created-order traversal window (RFC3339Nano UTC).
	Window string `json:"window,omitempty"`
}

// gitLabCursorShape names the continuation form a traversal mode expects, so a
// decoded cursor cannot smuggle another mode's continuation state.
type gitLabCursorShape int

const (
	gitLabCursorOffset gitLabCursorShape = iota
	gitLabCursorKeyset
	gitLabCursorWindowedOffset
)

// ListIssuesPage is the single owner of GitLab issue inventory requests and
// their normalization. It dispatches on the query: StateOpen drains the open
// list into one exhausted page, StateAll ordered-by-created pages ascending
// with a resumable cursor, and StateAll ordered-by-updated pages descending
// behind an inclusive watermark served through GitLab's exclusive
// updated_after filter.
func (c *Client) ListIssuesPage(
	ctx context.Context,
	ref platform.RepoRef,
	query platform.ItemPageQuery,
) (platform.Page[platform.Issue], error) {
	if err := platform.ValidateItemPageQuery(query); err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	if query.State == platform.ItemStateOpen {
		return c.listOpenIssuesPage(ctx, ref)
	}
	if query.Order == platform.ItemOrderUpdated {
		since := time.Time{}
		if query.UpdatedSince != nil {
			since = query.UpdatedSince.UTC()
		}
		return c.listInventoryIssuesPage(ctx, ref, since, query.Cursor, "updated_issues", "updated_at")
	}
	return c.listInventoryIssuesPage(ctx, ref, time.Time{}, query.Cursor, "historical_issues", "created_at")
}

// ListMergeRequestsPage is the merge-request counterpart to ListIssuesPage and
// keeps the open-scan merge-status recheck flag inside the canonical open
// branch.
func (c *Client) ListMergeRequestsPage(
	ctx context.Context,
	ref platform.RepoRef,
	query platform.ItemPageQuery,
) (platform.Page[platform.MergeRequest], error) {
	if err := platform.ValidateItemPageQuery(query); err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	if query.State == platform.ItemStateOpen {
		return c.listOpenMergeRequestsPage(ctx, ref)
	}
	if query.Order == platform.ItemOrderUpdated {
		since := time.Time{}
		if query.UpdatedSince != nil {
			since = query.UpdatedSince.UTC()
		}
		return c.listInventoryMergeRequestsPage(
			ctx, ref, since, query.Cursor, "updated_merge_requests", "updated_at",
		)
	}
	return c.listInventoryMergeRequestsPage(
		ctx, ref, time.Time{}, query.Cursor, "historical_merge_requests", "created_at",
	)
}

// listOpenIssuesPage returns every open issue as a single exhausted page. Open
// scans are single-shot current-index reads, so the internal page drain is not
// resumable through a caller cursor.
func (c *Client) listOpenIssuesPage(
	ctx context.Context,
	ref platform.RepoRef,
) (platform.Page[platform.Issue], error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	state := "opened"
	items, err := collectGitLabPages(ctx, func(ctx context.Context, page int64) ([]platform.Issue, int64, error) {
		issues, resp, err := c.api.Issues.ListProjectIssues(pid, &gitlab.ListProjectIssuesOptions{
			State:       &state,
			ListOptions: gitlab.ListOptions{Page: page, PerPage: defaultPageSize},
		}, gitlab.WithContext(ctx))
		if err != nil {
			return nil, 0, c.mapGitLabError("list_issues", err)
		}
		out := make([]platform.Issue, 0, len(issues))
		for _, issue := range issues {
			out = append(out, NormalizeIssue(normalizedRef, issue))
		}
		return out, nextGitLabPage(resp), nil
	})
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	return platform.Page[platform.Issue]{Items: items, Exhausted: true}, nil
}

// listOpenMergeRequestsPage returns every open merge request as a single
// exhausted page, including the fork head clone URL enrichment live sync
// depends on.
func (c *Client) listOpenMergeRequestsPage(
	ctx context.Context,
	ref platform.RepoRef,
) (platform.Page[platform.MergeRequest], error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	state := "opened"
	recheck := true
	items, err := collectGitLabPages(ctx, func(ctx context.Context, page int64) ([]platform.MergeRequest, int64, error) {
		mrs, resp, err := c.api.MergeRequests.ListProjectMergeRequests(pid, &gitlab.ListProjectMergeRequestsOptions{
			State:                  &state,
			WithMergeStatusRecheck: &recheck,
			ListOptions:            gitlab.ListOptions{Page: page, PerPage: defaultPageSize},
		}, gitlab.WithContext(ctx))
		if err != nil {
			return nil, 0, c.mapGitLabError("list_merge_requests", err)
		}
		out := make([]platform.MergeRequest, 0, len(mrs))
		for _, mr := range mrs {
			normalized := NormalizeMergeRequest(normalizedRef, mr, nil)
			normalized.HeadRepoCloneURL, err = c.optionalHeadRepoCloneURL(ctx, normalizedRef, mr.ProjectID, mr.SourceProjectID)
			if err != nil {
				return nil, 0, err
			}
			out = append(out, normalized)
		}
		return out, nextGitLabPage(resp), nil
	})
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	return platform.Page[platform.MergeRequest]{Items: items, Exhausted: true}, nil
}

// listInventoryIssuesPage owns the historical and maintenance issue request
// shapes. Both traversals use GitLab's keyset pagination (supported for
// project issues with order_by created_at/updated_at since GitLab 18.3), so
// the server's cursor carries its own id tie-break and equal timestamps have
// a total order across page boundaries. Ordering deviation from the neutral
// contract: created-order ties break by provider record id (the keyset
// tie-break column), not by item number; both are monotone with insertion, so
// restart stability holds. The maintenance traversal runs ascending — under a
// keyset cursor an item whose updated_at moves mid-scan only moves forward
// past the cursor, so it is re-served rather than skipped. On servers without
// issue keyset support the pagination parameter is ignored and the returned
// offset next-link degrades this to offset traversal.
func (c *Client) listInventoryIssuesPage(
	ctx context.Context,
	ref platform.RepoRef,
	since time.Time,
	encodedCursor string,
	mode string,
	orderBy string,
) (platform.Page[platform.Issue], error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	cursor, err := c.decodePageCursor(normalizedRef, 0, encodedCursor, mode, since, gitLabCursorKeyset)
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	state := "all"
	opts := &gitlab.ListProjectIssuesOptions{
		State: &state,
		ListOptions: gitlab.ListOptions{
			Pagination: "keyset", OrderBy: orderBy, Sort: "asc", PerPage: defaultPageSize,
		},
	}
	if !since.IsZero() {
		overlap := inclusiveGitLabWatermark(since)
		opts.UpdatedAfter = &overlap
	}
	requestOptions := []gitlab.RequestOptionFunc{gitlab.WithContext(ctx)}
	if cursor.Link != "" {
		requestOptions = append(requestOptions, gitlab.WithKeysetPaginationParameters(cursor.Link))
	}
	issues, resp, err := c.api.Issues.ListProjectIssues(pid, opts, requestOptions...)
	if err != nil {
		return platform.Page[platform.Issue]{}, c.mapGitLabError(
			string(platform.ArchiveCapabilityHistoricalIssues), err,
		)
	}
	items := make([]platform.Issue, 0, len(issues))
	for _, issue := range issues {
		items = append(items, NormalizeIssue(normalizedRef, issue))
	}
	return gitLabKeysetPage(items, cursor, resp)
}

// listInventoryMergeRequestsPage owns the historical and maintenance
// merge-request request shapes. GitLab does not support keyset pagination for
// project merge requests, so both traversals deviate from cursor-stable
// ordering and guarantee no-skip via overlap instead:
//
//   - The historical created-order traversal pages offset-ascending inside a
//     created_after window. Each time a page's newest created_at advances the
//     window, the cursor re-anchors one second behind it and resets to page
//     one, so equal-created_at ties and offset shifts at a page boundary are
//     re-delivered by the overlap rather than skipped (consumers dedupe by
//     identity). Ties denser than one window page keep paging within the
//     window. Ordering ties within a timestamp are server-unspecified.
//   - The maintenance traversal pages offset-descending behind the inclusive
//     updated_after watermark: mid-scan updates move rows into the consumed
//     prefix, and anything shifted past the scan is covered by the next
//     scan's watermark overlap.
func (c *Client) listInventoryMergeRequestsPage(
	ctx context.Context,
	ref platform.RepoRef,
	since time.Time,
	encodedCursor string,
	mode string,
	orderBy string,
) (platform.Page[platform.MergeRequest], error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	shape := gitLabCursorWindowedOffset
	state, sortOrder := "all", "asc"
	if !since.IsZero() {
		shape = gitLabCursorOffset
		sortOrder = "desc"
	}
	cursor, err := c.decodePageCursor(normalizedRef, 0, encodedCursor, mode, since, shape)
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	opts := &gitlab.ListProjectMergeRequestsOptions{
		State: &state, OrderBy: &orderBy, Sort: &sortOrder,
		ListOptions: gitlab.ListOptions{Page: cursor.Page, PerPage: defaultPageSize},
	}
	if !since.IsZero() {
		overlap := inclusiveGitLabWatermark(since)
		opts.UpdatedAfter = &overlap
	}
	if cursor.Window != "" {
		windowStart, parseErr := time.Parse(time.RFC3339Nano, cursor.Window)
		if parseErr != nil {
			return platform.Page[platform.MergeRequest]{}, c.pageCursorError(parseErr)
		}
		opts.CreatedAfter = &windowStart
	}
	mrs, resp, err := c.api.MergeRequests.ListProjectMergeRequests(pid, opts, gitlab.WithContext(ctx))
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, c.mapGitLabError(
			string(platform.ArchiveCapabilityHistoricalMergeRequests), err,
		)
	}
	items := make([]platform.MergeRequest, 0, len(mrs))
	for _, mr := range mrs {
		items = append(items, NormalizeMergeRequest(normalizedRef, mr, nil))
	}
	if shape == gitLabCursorWindowedOffset {
		return gitLabWindowedCursorPage(items, cursor, nextGitLabPage(resp))
	}
	return gitLabCursorPage(items, cursor, nextGitLabPage(resp), false)
}

// gitLabWindowedCursorPage advances the windowed created-order traversal:
// when the consumed page moved the newest created_at forward, the next window
// re-anchors one second behind it (overlap re-delivery instead of boundary
// skips) at page one; otherwise ties denser than the window advance keep
// offset-paging within the current window.
func gitLabWindowedCursorPage(
	items []platform.MergeRequest,
	cursor gitLabPageCursor,
	nextPage int64,
) (platform.Page[platform.MergeRequest], error) {
	if nextPage == 0 {
		return platform.Page[platform.MergeRequest]{Items: items, Exhausted: true}, nil
	}
	advanced := false
	if len(items) > 0 {
		newWindow := items[len(items)-1].CreatedAt.UTC().Add(-time.Second)
		windowStart, err := currentWindowStart(cursor.Window)
		if err != nil {
			return platform.Page[platform.MergeRequest]{}, err
		}
		if cursor.Window == "" || newWindow.After(windowStart) {
			cursor.Window = newWindow.Format(time.RFC3339Nano)
			cursor.Page = 1
			advanced = true
		}
	}
	if !advanced {
		cursor.Page = nextPage
	}
	next, err := encodeGitLabPageCursor(cursor)
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	return platform.Page[platform.MergeRequest]{Items: items, NextCursor: next}, nil
}

func currentWindowStart(window string) (time.Time, error) {
	if window == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, window)
}

func inclusiveGitLabWatermark(since time.Time) time.Time {
	return since.UTC().Add(-time.Nanosecond)
}

// gitLabKeysetPage assembles a canonical page from one keyset response:
// exhaustion when the provider issued no next link, otherwise a cursor
// carrying the provider's keyset continuation link.
func gitLabKeysetPage[T any](
	items []T,
	cursor gitLabPageCursor,
	resp *gitlab.Response,
) (platform.Page[T], error) {
	nextLink := ""
	if resp != nil {
		nextLink = resp.NextLink
	}
	if nextLink == "" {
		return platform.Page[T]{Items: items, Exhausted: true}, nil
	}
	cursor.Link = nextLink
	next, err := encodeGitLabPageCursor(cursor)
	if err != nil {
		return platform.Page[T]{}, err
	}
	return platform.Page[T]{Items: items, NextCursor: next}, nil
}

func nextGitLabPage(resp *gitlab.Response) int64 {
	if resp == nil {
		return 0
	}
	return resp.NextPage
}

// collectGitLabPages drains a numeric-page provider list through the bounded
// neutral collector, so every whole-dataset read shares one cycle-guarded,
// page-capped drain instead of a hand-rolled loop.
func collectGitLabPages[T any](
	ctx context.Context,
	fetch func(context.Context, int64) ([]T, int64, error),
) ([]T, error) {
	return platform.CollectPages(ctx, "1", func(ctx context.Context, cursor string) (platform.Page[T], error) {
		pageNumber, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return platform.Page[T]{}, fmt.Errorf("parse GitLab page cursor: %w", err)
		}
		items, nextPage, err := fetch(ctx, pageNumber)
		if err != nil {
			return platform.Page[T]{}, err
		}
		if nextPage == 0 {
			return platform.Page[T]{Items: items, Exhausted: true}, nil
		}
		return platform.Page[T]{Items: items, NextCursor: strconv.FormatInt(nextPage, 10)}, nil
	})
}

// LookupIssue fetches a single issue and returns a typed outcome. GitLab may
// hide confidential issues behind an item 404 even while the source project
// remains readable, so an ambiguous 404 classifies as inaccessible rather than
// removed; both live callers (which require present) and archive callers
// (which record every outcome) share this one classification.
func (c *Client) LookupIssue(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ItemLookup[platform.Issue], error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.ItemLookup[platform.Issue]{}, err
	}
	issue, _, err := c.api.Issues.GetIssue(pid, int64(number), nil, gitlab.WithContext(ctx))
	if err != nil {
		return c.classifyIssueLookup(ctx, pid, err)
	}
	return platform.ItemLookup[platform.Issue]{
		Outcome: platform.LookupPresent,
		Item:    NormalizeIssue(normalizedRef, issue),
	}, nil
}

// LookupMergeRequest is the merge-request counterpart to LookupIssue. It is
// the core fetch plus classification only: source-project clone-URL
// enrichment stays on the live GetMergeRequest wrapper, so archive lookups
// never spend an unadmitted enrichment request and an enrichment failure can
// never fail an archive lookup.
func (c *Client) LookupMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ItemLookup[platform.MergeRequest], error) {
	lookup, _, _, err := c.lookupMergeRequestDetail(ctx, ref, number)
	return lookup, err
}

// lookupMergeRequestDetail performs the canonical merge-request lookup and
// additionally exposes the raw provider record and hydrated repository ref so
// the live wrapper can enrich the present item without a second fetch.
func (c *Client) lookupMergeRequestDetail(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ItemLookup[platform.MergeRequest], *gitlab.MergeRequest, platform.RepoRef, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.ItemLookup[platform.MergeRequest]{}, nil, platform.RepoRef{}, err
	}
	mr, _, err := c.api.MergeRequests.GetMergeRequest(pid, int64(number), nil, gitlab.WithContext(ctx))
	if err != nil {
		outcome, classifyErr := c.classifyLookupOutcome(
			ctx, pid, string(platform.ArchiveCapabilityHistoricalMergeRequests), err,
		)
		return platform.ItemLookup[platform.MergeRequest]{Outcome: outcome}, nil, normalizedRef, classifyErr
	}
	lookup := platform.ItemLookup[platform.MergeRequest]{
		Outcome: platform.LookupPresent,
		Item:    NormalizeDetailedMergeRequest(normalizedRef, mr),
	}
	return lookup, mr, normalizedRef, nil
}

func (c *Client) classifyIssueLookup(
	ctx context.Context,
	pid any,
	err error,
) (platform.ItemLookup[platform.Issue], error) {
	outcome, classifyErr := c.classifyLookupOutcome(
		ctx, pid, string(platform.ArchiveCapabilityHistoricalIssues), err,
	)
	return platform.ItemLookup[platform.Issue]{Outcome: outcome}, classifyErr
}

// classifyLookupOutcome maps a failed single-item fetch onto the canonical
// lookup outcomes, spending at most one repository probe to distinguish
// item-level access loss from repository-level access loss.
func (c *Client) classifyLookupOutcome(
	ctx context.Context,
	pid any,
	capability string,
	err error,
) (platform.LookupOutcome, error) {
	if gitLabAccessError(err) {
		_, _, repoErr := c.api.Projects.GetProject(pid, nil, gitlab.WithContext(ctx))
		if repoErr == nil {
			return platform.LookupInaccessible, nil
		}
		if gitLabAccessError(repoErr) || isGitLabNotFound(repoErr) {
			return "", platform.PermissionDenied(platform.KindGitLab, c.host, repoErr)
		}
		return "", c.mapGitLabError("probe_repository", repoErr)
	}
	if !isGitLabNotFound(err) {
		return "", c.mapGitLabError(capability, err)
	}
	_, _, repoErr := c.api.Projects.GetProject(pid, nil, gitlab.WithContext(ctx))
	if repoErr == nil {
		// GitLab may hide confidential issues and merge requests behind an item
		// 404 even while the source project remains readable. Without a stable
		// deletion signal, retain the cached item as individually inaccessible.
		return platform.LookupInaccessible, nil
	}
	if gitLabAccessError(repoErr) || isGitLabNotFound(repoErr) {
		return "", platform.PermissionDenied(platform.KindGitLab, c.host, repoErr)
	}
	return "", c.mapGitLabError("probe_repository", repoErr)
}

func gitLabAccessError(err error) bool {
	var response *gitlab.ErrorResponse
	return errors.As(err, &response) &&
		(response.HasStatusCode(http.StatusUnauthorized) || response.HasStatusCode(http.StatusForbidden))
}

// lookupNotPresentError renders the typed error a live caller receives when a
// single-item lookup resolves to a non-present outcome. Live callers require
// present; archive callers inspect the outcome instead. Removed is not_found,
// inaccessible is permission_denied, and moved is not_found carrying the
// destination repository so callers can retarget the reference.
func (c *Client) lookupNotPresentError(
	ref platform.RepoRef,
	number int,
	outcome platform.LookupOutcome,
	destination *platform.RepoRef,
) error {
	code := platform.ErrCodeNotFound
	if outcome == platform.LookupInaccessible {
		code = platform.ErrCodePermissionDenied
	}
	return &platform.Error{
		Code:         code,
		Provider:     platform.KindGitLab,
		PlatformHost: c.host,
		Destination:  destination,
		Err:          fmt.Errorf("%s#%d is not present (%s)", ref.DisplayName(), number, outcome),
	}
}

// ListIssueCommentsPage returns one page of normalized issue comment events.
func (c *Client) ListIssueCommentsPage(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	encodedCursor string,
) (platform.Page[platform.IssueEvent], error) {
	pid, normalizedRef, cursor, err := c.detailPageArgs(ctx, ref, number, encodedCursor, "issue_comments")
	if err != nil {
		return platform.Page[platform.IssueEvent]{}, err
	}
	discussions, nextPage, err := c.listIssueDiscussionsPage(ctx, pid, number, cursor.Page)
	if err != nil {
		return platform.Page[platform.IssueEvent]{}, c.mapGitLabError(
			string(platform.ArchiveCapabilityOrdinaryComments), err,
		)
	}
	items := NormalizeIssueDiscussions(
		normalizedRef, number, gitLabIssueURL(normalizedRef, number), discussions,
	)
	return gitLabCursorPage(items, cursor, nextPage, true)
}

// ListMergeRequestCommentsPage returns one page of normalized ordinary
// merge-request comment events: inline discussions belong to the review-thread
// dataset and system notes are not comments, so both are filtered here. The
// discussions request itself is shared with ListReviewThreadsPage.
func (c *Client) ListMergeRequestCommentsPage(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	encodedCursor string,
) (platform.Page[platform.MergeRequestEvent], error) {
	pid, normalizedRef, cursor, err := c.detailPageArgs(ctx, ref, number, encodedCursor, "merge_request_comments")
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, err
	}
	discussions, nextPage, err := c.listMergeRequestDiscussionsPage(ctx, pid, number, cursor.Page)
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, c.mapGitLabError(
			string(platform.ArchiveCapabilityOrdinaryComments), err,
		)
	}
	ordinary := make([]*gitlab.Discussion, 0, len(discussions))
	for _, discussion := range discussions {
		if !gitLabInlineDiscussion(discussion) {
			ordinary = append(ordinary, gitLabOrdinaryDiscussion(discussion))
		}
	}
	items := NormalizeMergeRequestDiscussions(
		normalizedRef, number, gitLabMergeRequestURL(normalizedRef, number), ordinary,
	)
	return gitLabCursorPage(items, cursor, nextPage, true)
}

// ListSubmittedReviewsPage is a typed unsupported capability: GitLab has no
// submitted-reviews dataset.
func (c *Client) ListSubmittedReviewsPage(
	context.Context,
	platform.RepoRef,
	int,
	string,
) (platform.Page[platform.MergeRequestEvent], error) {
	return platform.Page[platform.MergeRequestEvent]{}, platform.UnsupportedCapability(
		platform.KindGitLab, c.host, string(platform.ArchiveCapabilitySubmittedReviews),
	)
}

// ListReviewThreadsPage returns one page of normalized inline review-thread
// comments extracted from the same discussions endpoint the comments page
// consumes.
func (c *Client) ListReviewThreadsPage(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	encodedCursor string,
) (platform.Page[platform.MergeRequestReviewThread], error) {
	pid, normalizedRef, cursor, err := c.detailPageArgs(ctx, ref, number, encodedCursor, "review_threads")
	if err != nil {
		return platform.Page[platform.MergeRequestReviewThread]{}, err
	}
	discussions, nextPage, err := c.listMergeRequestDiscussionsPage(ctx, pid, number, cursor.Page)
	if err != nil {
		return platform.Page[platform.MergeRequestReviewThread]{}, c.mapGitLabError(
			string(platform.ArchiveCapabilityInlineReviewComments), err,
		)
	}
	items := make([]platform.MergeRequestReviewThread, 0)
	for _, discussion := range discussions {
		for _, item := range gitLabDiscussionReviewThreads(gitLabMergeRequestURL(normalizedRef, number), discussion) {
			item.Repo = normalizedRef
			item.MergeRequestNumber = number
			items = append(items, item)
		}
	}
	return gitLabCursorPage(items, cursor, nextPage, true)
}

// listIssueDiscussionsPage is the single owner of the issue discussions
// endpoint request shape. Callers map errors onto their dataset capability.
func (c *Client) listIssueDiscussionsPage(
	ctx context.Context,
	pid any,
	number int,
	page int64,
) ([]*gitlab.Discussion, int64, error) {
	discussions, resp, err := c.api.Discussions.ListIssueDiscussions(
		pid, int64(number),
		&gitlab.ListIssueDiscussionsOptions{ListOptions: gitlab.ListOptions{Page: page, PerPage: defaultPageSize}},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return nil, 0, err
	}
	return discussions, nextGitLabPage(resp), nil
}

// listMergeRequestDiscussionsPage is the single owner of the merge-request
// discussions endpoint request shape, feeding the ordinary-comment filter, the
// review-thread extraction, and the live event surfaces. Callers map errors
// onto their dataset capability.
func (c *Client) listMergeRequestDiscussionsPage(
	ctx context.Context,
	pid any,
	number int,
	page int64,
) ([]*gitlab.Discussion, int64, error) {
	discussions, resp, err := c.api.Discussions.ListMergeRequestDiscussions(
		pid, int64(number),
		&gitlab.ListMergeRequestDiscussionsOptions{ListOptions: gitlab.ListOptions{Page: page, PerPage: defaultPageSize}},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return nil, 0, err
	}
	return discussions, nextGitLabPage(resp), nil
}

// gitLabOrdinaryDiscussion strips system notes from a non-inline discussion so
// the ordinary-comments dataset carries only human comments.
func gitLabOrdinaryDiscussion(discussion *gitlab.Discussion) *gitlab.Discussion {
	if discussion == nil {
		return nil
	}
	copy := *discussion
	copy.Notes = make([]*gitlab.Note, 0, len(discussion.Notes))
	for _, note := range discussion.Notes {
		if note != nil && !note.System {
			copy.Notes = append(copy.Notes, note)
		}
	}
	return &copy
}

func gitLabInlineDiscussion(discussion *gitlab.Discussion) bool {
	if discussion == nil {
		return false
	}
	for _, note := range discussion.Notes {
		if note != nil && !note.System && note.Position != nil {
			return true
		}
	}
	return false
}

// gitLabDiscussionReviewThreads extracts the inline review-thread comments of
// one discussion, anchoring position-less replies to the thread's first
// positioned note.
func gitLabDiscussionReviewThreads(
	parentURL string,
	discussion *gitlab.Discussion,
) []platform.MergeRequestReviewThread {
	if !gitLabInlineDiscussion(discussion) {
		return nil
	}
	var anchor *gitlab.Note
	for _, note := range discussion.Notes {
		if note != nil && !note.System && note.Position != nil {
			anchor = note
			break
		}
	}
	if anchor == nil {
		return nil
	}
	items := make([]platform.MergeRequestReviewThread, 0, len(discussion.Notes))
	for _, note := range discussion.Notes {
		if note == nil || note.System {
			continue
		}
		copy := *note
		if copy.Position == nil {
			copy.Position = anchor.Position
		}
		item := gitlabReviewThread(parentURL, discussion.ID, &copy)
		item.Resolved = anchor.Resolved
		if anchor.Resolved && anchor.UpdatedAt != nil {
			resolvedAt := anchor.UpdatedAt.UTC()
			item.ResolvedAt = &resolvedAt
		} else {
			item.ResolvedAt = nil
		}
		items = append(items, item)
	}
	return items
}

func (c *Client) detailPageArgs(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	encodedCursor string,
	mode string,
) (any, platform.RepoRef, gitLabPageCursor, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return nil, platform.RepoRef{}, gitLabPageCursor{}, err
	}
	cursor, err := c.decodePageCursor(normalizedRef, number, encodedCursor, mode, time.Time{}, gitLabCursorOffset)
	return pid, normalizedRef, cursor, err
}

// gitLabCursorPage assembles a canonical page from one provider response:
// exhaustion when the provider reports no further page, otherwise the encoded
// resume cursor, with filtered empty pages marked as explicit progress.
func gitLabCursorPage[T any](
	items []T,
	cursor gitLabPageCursor,
	nextPage int64,
	filtered bool,
) (platform.Page[T], error) {
	if nextPage == 0 {
		return platform.Page[T]{Items: items, Exhausted: true}, nil
	}
	cursor.Page = nextPage
	next, err := encodeGitLabPageCursor(cursor)
	if err != nil {
		return platform.Page[T]{}, err
	}
	return platform.Page[T]{
		Items: items, NextCursor: next, ProgressOnly: filtered && len(items) == 0,
	}, nil
}

func (c *Client) decodePageCursor(
	ref platform.RepoRef,
	number int,
	encoded string,
	mode string,
	since time.Time,
	shape gitLabCursorShape,
) (gitLabPageCursor, error) {
	repoPath, err := rawProjectPath(ref)
	if err != nil {
		return gitLabPageCursor{}, err
	}
	repoPath = strings.Trim(repoPath, "/")
	expectedSince := ""
	if !since.IsZero() {
		expectedSince = since.UTC().Format(time.RFC3339Nano)
	}
	if encoded == "" {
		cursor := gitLabPageCursor{
			Mode: mode, Host: ref.Host, RepoPath: repoPath, Number: number, Since: expectedSince,
		}
		if shape != gitLabCursorKeyset {
			cursor.Page = 1
		}
		return cursor, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return gitLabPageCursor{}, c.pageCursorError(err)
	}
	var cursor gitLabPageCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return gitLabPageCursor{}, c.pageCursorError(err)
	}
	if cursor.Mode != mode || cursor.Host != ref.Host || cursor.RepoPath != repoPath || cursor.Number != number ||
		cursor.Since != expectedSince {
		return gitLabPageCursor{}, c.pageCursorError(errors.New("cursor does not match page enumeration"))
	}
	if err := validateGitLabCursorShape(cursor, shape); err != nil {
		return gitLabPageCursor{}, c.pageCursorError(err)
	}
	return cursor, nil
}

// validateGitLabCursorShape rejects a resumption cursor whose continuation
// state does not belong to the traversal kind decoding it, so an offset page
// number can never be replayed as a keyset link or vice versa.
func validateGitLabCursorShape(cursor gitLabPageCursor, shape gitLabCursorShape) error {
	switch shape {
	case gitLabCursorKeyset:
		if cursor.Link == "" || cursor.Page != 0 || cursor.Window != "" {
			return errors.New("cursor does not carry keyset continuation state")
		}
		if _, err := url.Parse(cursor.Link); err != nil {
			return fmt.Errorf("invalid keyset continuation link: %w", err)
		}
	case gitLabCursorWindowedOffset:
		if cursor.Page <= 0 || cursor.Link != "" {
			return errors.New("cursor does not carry windowed offset continuation state")
		}
		if cursor.Window != "" {
			if _, err := time.Parse(time.RFC3339Nano, cursor.Window); err != nil {
				return fmt.Errorf("invalid traversal window: %w", err)
			}
		}
	default:
		if cursor.Page <= 0 || cursor.Link != "" || cursor.Window != "" {
			return errors.New("cursor does not carry offset continuation state")
		}
	}
	return nil
}

func (c *Client) pageCursorError(err error) error {
	return platform.ProviderContract(platform.KindGitLab, c.host, "archive_cursor", err)
}

func encodeGitLabPageCursor(cursor gitLabPageCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode GitLab page cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

var (
	_ platform.IssuePageReader        = (*Client)(nil)
	_ platform.MergeRequestPageReader = (*Client)(nil)
)
