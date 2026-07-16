package gitlab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"go.kenn.io/middleman/internal/platform"
)

type archiveCursor struct {
	Mode     string `json:"mode"`
	Host     string `json:"host"`
	RepoPath string `json:"repo_path"`
	Number   int    `json:"number,omitempty"`
	Page     int64  `json:"page"`
	Since    string `json:"since,omitempty"`
}

func (c *Client) ListHistoricalIssues(
	ctx context.Context,
	ref platform.RepoRef,
	cursor string,
) (platform.ArchivePage[platform.Issue], error) {
	return c.listArchiveIssues(ctx, ref, time.Time{}, cursor, "historical_issues", "created_at")
}

func (c *Client) ListHistoricalMergeRequests(
	ctx context.Context,
	ref platform.RepoRef,
	cursor string,
) (platform.ArchivePage[platform.MergeRequest], error) {
	return c.listArchiveMergeRequests(
		ctx, ref, time.Time{}, cursor, "historical_merge_requests", "created_at",
	)
}

func (c *Client) ListUpdatedIssues(
	ctx context.Context,
	ref platform.RepoRef,
	since time.Time,
	cursor string,
) (platform.ArchivePage[platform.Issue], error) {
	return c.listArchiveIssues(ctx, ref, since.UTC(), cursor, "updated_issues", "updated_at")
}

func (c *Client) ListUpdatedMergeRequests(
	ctx context.Context,
	ref platform.RepoRef,
	since time.Time,
	cursor string,
) (platform.ArchivePage[platform.MergeRequest], error) {
	return c.listArchiveMergeRequests(
		ctx, ref, since.UTC(), cursor, "updated_merge_requests", "updated_at",
	)
}

func (c *Client) listArchiveIssues(
	ctx context.Context,
	ref platform.RepoRef,
	since time.Time,
	encodedCursor string,
	mode string,
	orderBy string,
) (platform.ArchivePage[platform.Issue], error) {
	pid, err := projectLookupArg(ref)
	if err != nil {
		return platform.ArchivePage[platform.Issue]{}, err
	}
	cursor, err := c.decodeArchiveCursor(ref, 0, encodedCursor, mode, since)
	if err != nil {
		return platform.ArchivePage[platform.Issue]{}, err
	}
	state, sortOrder := "all", "asc"
	if !since.IsZero() {
		// Updated rows can only move toward the already-consumed prefix in a
		// descending traversal. Rows that move ahead before they are consumed
		// are covered by the next scan's inclusive watermark.
		sortOrder = "desc"
	}
	opts := &gitlab.ListProjectIssuesOptions{
		State: &state, OrderBy: &orderBy, Sort: &sortOrder,
		ListOptions: gitlab.ListOptions{Page: cursor.Page, PerPage: defaultPageSize},
	}
	if !since.IsZero() {
		overlap := inclusiveGitLabWatermark(since)
		opts.UpdatedAfter = &overlap
	}
	issues, resp, err := c.api.Issues.ListProjectIssues(pid, opts, gitlab.WithContext(ctx))
	if err != nil {
		return platform.ArchivePage[platform.Issue]{}, c.mapGitLabError("archive_list_issues", err)
	}
	normalizedRef := c.normalizeRef(ref, ref.PlatformID)
	items := make([]platform.Issue, 0, len(issues))
	for _, issue := range issues {
		items = append(items, NormalizeIssue(normalizedRef, issue))
	}
	return gitLabArchivePage(items, cursor, nextGitLabPage(resp), false)
}

func (c *Client) listArchiveMergeRequests(
	ctx context.Context,
	ref platform.RepoRef,
	since time.Time,
	encodedCursor string,
	mode string,
	orderBy string,
) (platform.ArchivePage[platform.MergeRequest], error) {
	pid, err := projectLookupArg(ref)
	if err != nil {
		return platform.ArchivePage[platform.MergeRequest]{}, err
	}
	cursor, err := c.decodeArchiveCursor(ref, 0, encodedCursor, mode, since)
	if err != nil {
		return platform.ArchivePage[platform.MergeRequest]{}, err
	}
	state, sortOrder := "all", "asc"
	if !since.IsZero() {
		// Keep mutable rows from shifting an unseen row into an offset page we
		// already consumed. The inclusive watermark overlaps the next scan.
		sortOrder = "desc"
	}
	opts := &gitlab.ListProjectMergeRequestsOptions{
		State: &state, OrderBy: &orderBy, Sort: &sortOrder,
		ListOptions: gitlab.ListOptions{Page: cursor.Page, PerPage: defaultPageSize},
	}
	if !since.IsZero() {
		overlap := inclusiveGitLabWatermark(since)
		opts.UpdatedAfter = &overlap
	}
	mrs, resp, err := c.api.MergeRequests.ListProjectMergeRequests(pid, opts, gitlab.WithContext(ctx))
	if err != nil {
		return platform.ArchivePage[platform.MergeRequest]{}, c.mapGitLabError("archive_list_merge_requests", err)
	}
	normalizedRef := c.normalizeRef(ref, ref.PlatformID)
	items := make([]platform.MergeRequest, 0, len(mrs))
	for _, mr := range mrs {
		items = append(items, NormalizeMergeRequest(normalizedRef, mr, nil))
	}
	return gitLabArchivePage(items, cursor, nextGitLabPage(resp), false)
}

func inclusiveGitLabWatermark(since time.Time) time.Time {
	return since.UTC().Add(-time.Nanosecond)
}

func nextGitLabPage(resp *gitlab.Response) int64 {
	if resp == nil {
		return 0
	}
	return resp.NextPage
}

func (c *Client) GetArchiveIssue(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ArchiveItemResult[platform.Issue], error) {
	pid, err := projectLookupArg(ref)
	if err != nil {
		return platform.ArchiveItemResult[platform.Issue]{}, err
	}
	issue, _, err := c.api.Issues.GetIssue(pid, int64(number), nil, gitlab.WithContext(ctx))
	if err != nil {
		return classifyGitLabArchiveLookup(c, ctx, pid, "archive_get_issue", err)
	}
	return platform.ArchiveItemResult[platform.Issue]{
		Outcome: platform.ArchiveLookupPresent,
		Item:    NormalizeIssue(c.normalizeRef(ref, ref.PlatformID), issue),
	}, nil
}

func (c *Client) GetArchiveMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.ArchiveItemResult[platform.MergeRequest], error) {
	pid, err := projectLookupArg(ref)
	if err != nil {
		return platform.ArchiveItemResult[platform.MergeRequest]{}, err
	}
	mr, _, err := c.api.MergeRequests.GetMergeRequest(pid, int64(number), nil, gitlab.WithContext(ctx))
	if err != nil {
		outcome, classifyErr := classifyGitLabArchiveOutcome(c, ctx, pid, "archive_get_merge_request", err)
		return platform.ArchiveItemResult[platform.MergeRequest]{Outcome: outcome}, classifyErr
	}
	return platform.ArchiveItemResult[platform.MergeRequest]{
		Outcome: platform.ArchiveLookupPresent,
		Item:    NormalizeDetailedMergeRequest(c.normalizeRef(ref, ref.PlatformID), mr),
	}, nil
}

func classifyGitLabArchiveLookup(
	c *Client,
	ctx context.Context,
	pid any,
	capability string,
	err error,
) (platform.ArchiveItemResult[platform.Issue], error) {
	outcome, classifyErr := classifyGitLabArchiveOutcome(c, ctx, pid, capability, err)
	return platform.ArchiveItemResult[platform.Issue]{Outcome: outcome}, classifyErr
}

func classifyGitLabArchiveOutcome(
	c *Client,
	ctx context.Context,
	pid any,
	capability string,
	err error,
) (platform.ArchiveLookupOutcome, error) {
	if gitLabArchiveAccessError(err) {
		_, _, repoErr := c.api.Projects.GetProject(pid, nil, gitlab.WithContext(ctx))
		if repoErr == nil {
			return platform.ArchiveLookupInaccessible, nil
		}
		if gitLabArchiveAccessError(repoErr) || isGitLabNotFound(repoErr) {
			return "", platform.PermissionDenied(platform.KindGitLab, c.host, repoErr)
		}
		return "", c.mapGitLabError("archive_probe_repository", repoErr)
	}
	if !isGitLabNotFound(err) {
		return "", c.mapGitLabError(capability, err)
	}
	_, _, repoErr := c.api.Projects.GetProject(pid, nil, gitlab.WithContext(ctx))
	if repoErr == nil {
		// GitLab may hide confidential issues and merge requests behind an item
		// 404 even while the source project remains readable. Without a stable
		// deletion signal, retain the cached item as individually inaccessible.
		return platform.ArchiveLookupInaccessible, nil
	}
	if gitLabArchiveAccessError(repoErr) || isGitLabNotFound(repoErr) {
		return "", platform.PermissionDenied(platform.KindGitLab, c.host, repoErr)
	}
	return "", c.mapGitLabError("archive_probe_repository", repoErr)
}

func gitLabArchiveAccessError(err error) bool {
	var response *gitlab.ErrorResponse
	return errors.As(err, &response) &&
		(response.HasStatusCode(http.StatusUnauthorized) || response.HasStatusCode(http.StatusForbidden))
}

func (c *Client) ListArchiveIssueComments(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	encodedCursor string,
) (platform.ArchivePage[platform.IssueEvent], error) {
	pid, cursor, err := c.archiveDetailArgs(ref, number, encodedCursor, "issue_comments")
	if err != nil {
		return platform.ArchivePage[platform.IssueEvent]{}, err
	}
	discussions, resp, err := c.api.Discussions.ListIssueDiscussions(
		pid, int64(number),
		&gitlab.ListIssueDiscussionsOptions{ListOptions: gitlab.ListOptions{Page: cursor.Page, PerPage: defaultPageSize}},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.ArchivePage[platform.IssueEvent]{}, c.mapGitLabError("archive_issue_comments", err)
	}
	items := NormalizeIssueDiscussions(
		c.normalizeRef(ref, ref.PlatformID), number, gitLabIssueURL(ref, number), discussions,
	)
	return gitLabArchivePage(items, cursor, nextGitLabPage(resp), true)
}

func (c *Client) ListArchiveMergeRequestComments(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	encodedCursor string,
) (platform.ArchivePage[platform.MergeRequestEvent], error) {
	pid, cursor, err := c.archiveDetailArgs(ref, number, encodedCursor, "merge_request_comments")
	if err != nil {
		return platform.ArchivePage[platform.MergeRequestEvent]{}, err
	}
	discussions, resp, err := c.api.Discussions.ListMergeRequestDiscussions(
		pid, int64(number),
		&gitlab.ListMergeRequestDiscussionsOptions{ListOptions: gitlab.ListOptions{Page: cursor.Page, PerPage: defaultPageSize}},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.ArchivePage[platform.MergeRequestEvent]{}, c.mapGitLabError("archive_merge_request_comments", err)
	}
	ordinary := make([]*gitlab.Discussion, 0, len(discussions))
	for _, discussion := range discussions {
		if !gitLabInlineDiscussion(discussion) {
			ordinary = append(ordinary, gitLabArchiveOrdinaryDiscussion(discussion))
		}
	}
	items := NormalizeMergeRequestDiscussions(
		c.normalizeRef(ref, ref.PlatformID), number, gitLabMergeRequestURL(ref, number), ordinary,
	)
	return gitLabArchivePage(items, cursor, nextGitLabPage(resp), true)
}

func gitLabArchiveOrdinaryDiscussion(discussion *gitlab.Discussion) *gitlab.Discussion {
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

func (c *Client) ListArchiveSubmittedReviews(
	context.Context,
	platform.RepoRef,
	int,
	string,
) (platform.ArchivePage[platform.MergeRequestEvent], error) {
	return platform.ArchivePage[platform.MergeRequestEvent]{}, platform.UnsupportedCapability(
		platform.KindGitLab, c.host, string(platform.ArchiveCapabilitySubmittedReviews),
	)
}

func (c *Client) ListArchiveReviewThreads(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	encodedCursor string,
) (platform.ArchivePage[platform.MergeRequestReviewThread], error) {
	pid, cursor, err := c.archiveDetailArgs(ref, number, encodedCursor, "review_threads")
	if err != nil {
		return platform.ArchivePage[platform.MergeRequestReviewThread]{}, err
	}
	discussions, resp, err := c.api.Discussions.ListMergeRequestDiscussions(
		pid, int64(number),
		&gitlab.ListMergeRequestDiscussionsOptions{ListOptions: gitlab.ListOptions{Page: cursor.Page, PerPage: defaultPageSize}},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.ArchivePage[platform.MergeRequestReviewThread]{}, c.mapGitLabError("archive_review_threads", err)
	}
	normalizedRef := c.normalizeRef(ref, ref.PlatformID)
	items := make([]platform.MergeRequestReviewThread, 0)
	for _, discussion := range discussions {
		for _, item := range gitLabArchiveReviewThreads(gitLabMergeRequestURL(ref, number), discussion) {
			item.Repo = normalizedRef
			item.MergeRequestNumber = number
			items = append(items, item)
		}
	}
	return gitLabArchivePage(items, cursor, nextGitLabPage(resp), true)
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

func gitLabArchiveReviewThreads(
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

func (c *Client) archiveDetailArgs(
	ref platform.RepoRef,
	number int,
	encodedCursor string,
	mode string,
) (any, archiveCursor, error) {
	pid, err := projectLookupArg(ref)
	if err != nil {
		return nil, archiveCursor{}, err
	}
	cursor, err := c.decodeArchiveCursor(ref, number, encodedCursor, mode, time.Time{})
	return pid, cursor, err
}

func gitLabArchivePage[T any](
	items []T,
	cursor archiveCursor,
	nextPage int64,
	filtered bool,
) (platform.ArchivePage[T], error) {
	if nextPage == 0 {
		return platform.ArchivePage[T]{Items: items, Exhausted: true}, nil
	}
	cursor.Page = nextPage
	next, err := encodeGitLabArchiveCursor(cursor)
	if err != nil {
		return platform.ArchivePage[T]{}, err
	}
	return platform.ArchivePage[T]{
		Items: items, NextCursor: next, ProgressOnly: filtered && len(items) == 0,
	}, nil
}

func (c *Client) decodeArchiveCursor(
	ref platform.RepoRef,
	number int,
	encoded string,
	mode string,
	since time.Time,
) (archiveCursor, error) {
	repoPath, err := rawProjectPath(ref)
	if err != nil {
		return archiveCursor{}, err
	}
	repoPath = strings.Trim(repoPath, "/")
	expectedSince := ""
	if !since.IsZero() {
		expectedSince = since.UTC().Format(time.RFC3339Nano)
	}
	if encoded == "" {
		return archiveCursor{
			Mode: mode, Host: ref.Host, RepoPath: repoPath, Number: number, Page: 1, Since: expectedSince,
		}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return archiveCursor{}, c.archiveCursorError(err)
	}
	var cursor archiveCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return archiveCursor{}, c.archiveCursorError(err)
	}
	if cursor.Mode != mode || cursor.Host != ref.Host || cursor.RepoPath != repoPath || cursor.Number != number ||
		cursor.Page <= 0 || cursor.Since != expectedSince {
		return archiveCursor{}, c.archiveCursorError(errors.New("cursor does not match archive enumeration"))
	}
	return cursor, nil
}

func (c *Client) archiveCursorError(err error) error {
	return platform.ProviderContract(platform.KindGitLab, c.host, "archive_cursor", err)
}

func encodeGitLabArchiveCursor(cursor archiveCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode GitLab archive cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

var _ platform.ArchiveReader = (*Client)(nil)
