package gitealike

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/platform"
)

type archiveCursor struct {
	Mode   string `json:"mode"`
	Repo   string `json:"repo"`
	Number int    `json:"number,omitempty"`
	Page   int    `json:"page"`
	Since  string `json:"since,omitempty"`
	Before string `json:"before,omitempty"`
}

func (p *Provider) ListHistoricalIssues(ctx context.Context, ref platform.RepoRef, cursor string) (platform.Page[platform.Issue], error) {
	return p.listArchiveIssues(ctx, ref, time.Time{}, cursor, "historical_issues")
}

func (p *Provider) ListHistoricalMergeRequests(ctx context.Context, ref platform.RepoRef, cursor string) (platform.Page[platform.MergeRequest], error) {
	return p.listArchiveMergeRequests(ctx, ref, time.Time{}, cursor, "historical_merge_requests")
}

func (p *Provider) ListUpdatedIssues(ctx context.Context, ref platform.RepoRef, since time.Time, cursor string) (platform.Page[platform.Issue], error) {
	return p.listArchiveIssues(ctx, ref, since.UTC(), cursor, "updated_issues")
}

func (p *Provider) ListUpdatedMergeRequests(ctx context.Context, ref platform.RepoRef, since time.Time, cursor string) (platform.Page[platform.MergeRequest], error) {
	return p.listArchiveMergeRequests(ctx, ref, since.UTC(), cursor, "updated_merge_requests")
}

func (p *Provider) listArchiveIssues(ctx context.Context, ref platform.RepoRef, since time.Time, encoded, mode string) (platform.Page[platform.Issue], error) {
	t, err := p.archiveTransport()
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	cursor, err := p.decodeArchiveCursor(ref, 0, mode, since, encoded)
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	items, page, err := t.ListArchiveIssues(ctx, ref, ArchiveListOptions{
		PageOptions: PageOptions{Page: cursor.Page, PageSize: defaultPageSize},
		Since:       inclusiveArchiveWatermark(since), Before: archiveBefore(cursor),
	})
	if err != nil {
		return platform.Page[platform.Issue]{}, p.mapError(err)
	}
	// The Gitea-compatible issue endpoint has no sort parameter and returns
	// newest pages first. The first request discovers the final page; each
	// subsequent request walks backward and reverses that page.
	if cursor.Page == 1 && page.Last > 1 {
		cursor.Page = page.Last
		return progressArchivePage[platform.Issue](p, cursor)
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
	return nextArchivePage(p, out, cursor, len(out) == 0)
}

func (p *Provider) listArchiveMergeRequests(ctx context.Context, ref platform.RepoRef, since time.Time, encoded, mode string) (platform.Page[platform.MergeRequest], error) {
	t, err := p.archiveTransport()
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	cursor, err := p.decodeArchiveCursor(ref, 0, mode, since, encoded)
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
	overlap := inclusiveArchiveWatermark(since)
	for _, item := range items {
		watermarkValue := item.Created
		if !since.IsZero() {
			watermarkValue = item.Updated
		}
		if watermarkValue.After(parseArchiveTime(cursor.Before)) {
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
	return nextArchivePage(p, out, cursor, len(out) == 0)
}

func (p *Provider) GetArchiveIssue(ctx context.Context, ref platform.RepoRef, number int) (platform.ItemLookup[platform.Issue], error) {
	item, err := p.transport.GetIssue(ctx, ref, number)
	if err != nil {
		outcome, classifyErr := p.classifyArchiveLookup(ctx, ref, err)
		return platform.ItemLookup[platform.Issue]{Outcome: outcome}, classifyErr
	}
	return platform.ItemLookup[platform.Issue]{Outcome: platform.LookupPresent, Item: NormalizeIssue(ref, item)}, nil
}

func (p *Provider) GetArchiveMergeRequest(ctx context.Context, ref platform.RepoRef, number int) (platform.ItemLookup[platform.MergeRequest], error) {
	item, err := p.transport.GetPullRequest(ctx, ref, number)
	if err != nil {
		outcome, classifyErr := p.classifyArchiveLookup(ctx, ref, err)
		return platform.ItemLookup[platform.MergeRequest]{Outcome: outcome}, classifyErr
	}
	return platform.ItemLookup[platform.MergeRequest]{Outcome: platform.LookupPresent, Item: NormalizePullRequest(ref, item)}, nil
}

func (p *Provider) classifyArchiveLookup(ctx context.Context, ref platform.RepoRef, err error) (platform.LookupOutcome, error) {
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

func (p *Provider) ListArchiveIssueComments(ctx context.Context, ref platform.RepoRef, number int, encoded string) (platform.Page[platform.IssueEvent], error) {
	cursor, err := p.decodeArchiveCursor(ref, number, "issue_comments", time.Time{}, encoded)
	if err != nil {
		return platform.Page[platform.IssueEvent]{}, err
	}
	comments, page, err := p.transport.ListIssueComments(ctx, ref, number, PageOptions{Page: cursor.Page, PageSize: defaultPageSize})
	if err != nil {
		return platform.Page[platform.IssueEvent]{}, p.mapError(err)
	}
	return detailArchivePage(p, NormalizeIssueComments(p.kind, ref, number, comments), cursor, page)
}

func (p *Provider) ListArchiveMergeRequestComments(ctx context.Context, ref platform.RepoRef, number int, encoded string) (platform.Page[platform.MergeRequestEvent], error) {
	cursor, err := p.decodeArchiveCursor(ref, number, "merge_request_comments", time.Time{}, encoded)
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, err
	}
	comments, page, err := p.transport.ListPullRequestComments(ctx, ref, number, PageOptions{Page: cursor.Page, PageSize: defaultPageSize})
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, p.mapError(err)
	}
	return detailArchivePage(p, NormalizeMergeRequestEvents(p.kind, ref, number, comments, nil, nil), cursor, page)
}

func (p *Provider) ListArchiveSubmittedReviews(ctx context.Context, ref platform.RepoRef, number int, encoded string) (platform.Page[platform.MergeRequestEvent], error) {
	cursor, err := p.decodeArchiveCursor(ref, number, "submitted_reviews", time.Time{}, encoded)
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, err
	}
	reviews, page, err := p.transport.ListPullRequestReviews(ctx, ref, number, PageOptions{Page: cursor.Page, PageSize: defaultPageSize})
	if err != nil {
		return platform.Page[platform.MergeRequestEvent]{}, p.mapError(err)
	}
	submitted := make([]ReviewDTO, 0, len(reviews))
	for _, review := range reviews {
		if review.Submitted.IsZero() || !isSubmittedArchiveReviewState(review.State) {
			continue
		}
		submitted = append(submitted, review)
	}
	return detailArchivePage(p, NormalizeMergeRequestEvents(p.kind, ref, number, nil, submitted, nil), cursor, page)
}

func isSubmittedArchiveReviewState(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "APPROVED", "COMMENT", "REQUEST_CHANGES":
		return true
	default:
		return false
	}
}

func (p *Provider) ListArchiveReviewThreads(context.Context, platform.RepoRef, int, string) (platform.Page[platform.MergeRequestReviewThread], error) {
	return platform.Page[platform.MergeRequestReviewThread]{}, platform.UnsupportedCapability(p.kind, p.host, string(platform.ArchiveCapabilityInlineReviewComments))
}

func detailArchivePage[T any](p *Provider, items []T, cursor archiveCursor, page Page) (platform.Page[T], error) {
	if page.Next == 0 {
		return platform.Page[T]{Items: items, Exhausted: true}, nil
	}
	cursor.Page = page.Next
	return nextArchivePage(p, items, cursor, len(items) == 0)
}

func nextArchivePage[T any](p *Provider, items []T, cursor archiveCursor, progressOnly bool) (platform.Page[T], error) {
	next, err := encodeArchiveCursor(cursor)
	if err != nil {
		return platform.Page[T]{}, err
	}
	return platform.Page[T]{Items: items, NextCursor: next, ProgressOnly: progressOnly}, nil
}

func progressArchivePage[T any](p *Provider, cursor archiveCursor) (platform.Page[T], error) {
	return nextArchivePage(p, []T{}, cursor, true)
}

func (p *Provider) archiveTransport() (ArchiveTransport, error) {
	t, ok := p.transport.(ArchiveTransport)
	if !ok {
		return nil, platform.UnsupportedCapability(p.kind, p.host, string(platform.ArchiveCapabilityHistoricalIssues))
	}
	return t, nil
}

func (p *Provider) decodeArchiveCursor(ref platform.RepoRef, number int, mode string, since time.Time, encoded string) (archiveCursor, error) {
	expected := archiveCursor{Mode: mode, Repo: archiveRepoKey(p.kind, p.host, ref), Number: number, Page: 1}
	if !since.IsZero() {
		expected.Since = since.UTC().Format(time.RFC3339Nano)
	}
	if encoded == "" {
		expected.Before = time.Now().UTC().Format(time.RFC3339Nano)
		return expected, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return archiveCursor{}, p.archiveCursorError(err)
	}
	var cursor archiveCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return archiveCursor{}, p.archiveCursorError(err)
	}
	if cursor.Mode != expected.Mode || cursor.Repo != expected.Repo || cursor.Number != expected.Number || cursor.Since != expected.Since || cursor.Page < 1 || cursor.Before == "" {
		return archiveCursor{}, p.archiveCursorError(errors.New("cursor does not match archive enumeration"))
	}
	return cursor, nil
}

func (p *Provider) archiveCursorError(err error) error {
	return platform.ProviderContract(p.kind, p.host, "archive_cursor", err)
}

func encodeArchiveCursor(cursor archiveCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode archive cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func archiveRepoKey(kind platform.Kind, host string, ref platform.RepoRef) string {
	return strings.Join([]string{string(kind), host, string(ref.Platform), ref.Host, ref.Owner, ref.Name}, "\x00")
}

func inclusiveArchiveWatermark(since time.Time) time.Time {
	if since.IsZero() {
		return time.Time{}
	}
	return since.UTC().Add(-time.Second)
}

func archiveBefore(cursor archiveCursor) time.Time { return parseArchiveTime(cursor.Before) }

func parseArchiveTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

var _ platform.ArchiveReader = (*Provider)(nil)
