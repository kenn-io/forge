package gitealike

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/platform"
)

func pagesTestRef() platform.RepoRef {
	return platform.RepoRef{
		Platform: platform.KindForgejo, Host: "forge.example", Owner: "owner", Name: "repo",
	}
}

func newInventoryFakeTransport(base time.Time) *archiveFakeTransport {
	return &archiveFakeTransport{
		fakeTransport: &fakeTransport{},
		issuePages: map[int][]IssueDTO{
			1: {{ID: 3, Index: 3, Created: base.Add(2 * time.Hour), Updated: base.Add(2 * time.Hour)}},
			3: {
				{ID: 2, Index: 2, Created: base.Add(time.Hour), Updated: base.Add(time.Hour), IsPullRequest: true},
				{ID: 1, Index: 1, Created: base, Updated: base},
			},
		},
		issueLast: 3,
		pullPages: map[int][]PullRequestDTO{
			1: {{ID: 11, Index: 1, Created: base, Updated: base.Add(time.Hour)}},
			2: {{ID: 12, Index: 2, Created: base.Add(time.Minute), Updated: base}},
		},
		pullPage: map[int]Page{1: {Next: 2}},
	}
}

func drainPages[T any](t *testing.T, fetch func(context.Context, string) (platform.Page[T], error)) []T {
	t.Helper()
	items, err := platform.CollectPages(t.Context(), "", fetch)
	require.NoError(t, err)
	return items
}

func TestListPagesRejectInvalidQueries(t *testing.T) {
	since := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	queries := map[string]platform.ItemPageQuery{
		"missing state":                {Order: platform.ItemOrderCreated},
		"unknown order":                {State: platform.ItemStateAll, Order: "newest"},
		"open with cursor":             {State: platform.ItemStateOpen, Order: platform.ItemOrderCreated, Cursor: "abc"},
		"open with watermark":          {State: platform.ItemStateOpen, Order: platform.ItemOrderUpdated, UpdatedSince: &since},
		"watermark with created order": {State: platform.ItemStateAll, Order: platform.ItemOrderCreated, UpdatedSince: &since},
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			transport := &archiveFakeTransport{fakeTransport: &fakeTransport{}}
			provider := NewProvider(platform.KindForgejo, "forge.example", transport)

			_, issueErr := provider.ListIssuesPage(t.Context(), pagesTestRef(), query)
			_, mrErr := provider.ListMergeRequestsPage(t.Context(), pagesTestRef(), query)

			require.ErrorIs(t, issueErr, platform.ErrInvalidArgument)
			require.ErrorIs(t, mrErr, platform.ErrInvalidArgument)
			assert.Zero(t, transport.requests)
		})
	}
}

func TestItemScopedReadsRejectNonpositiveNumbers(t *testing.T) {
	calls := []struct {
		name string
		call func(context.Context, *Provider, platform.RepoRef, int) error
	}{
		{"lookup issue", func(ctx context.Context, p *Provider, ref platform.RepoRef, n int) error {
			_, err := p.LookupIssue(ctx, ref, n)
			return err
		}},
		{"lookup merge request", func(ctx context.Context, p *Provider, ref platform.RepoRef, n int) error {
			_, err := p.LookupMergeRequest(ctx, ref, n)
			return err
		}},
		{"issue comments page", func(ctx context.Context, p *Provider, ref platform.RepoRef, n int) error {
			_, err := p.ListIssueCommentsPage(ctx, ref, n, "")
			return err
		}},
		{"merge request comments page", func(ctx context.Context, p *Provider, ref platform.RepoRef, n int) error {
			_, err := p.ListMergeRequestCommentsPage(ctx, ref, n, "")
			return err
		}},
		{"submitted reviews page", func(ctx context.Context, p *Provider, ref platform.RepoRef, n int) error {
			_, err := p.ListSubmittedReviewsPage(ctx, ref, n, "")
			return err
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			transport := &archiveFakeTransport{fakeTransport: &fakeTransport{}}
			provider := NewProvider(platform.KindGitea, "git.example", transport)
			ref := platform.RepoRef{
				Platform: platform.KindGitea, Host: "git.example", Owner: "owner", Name: "repo",
			}

			for _, number := range []int{0, -4} {
				err := tc.call(t.Context(), provider, ref, number)
				require.ErrorIs(t, err, platform.ErrInvalidArgument)
			}
			assert.Zero(t, transport.requests)
		})
	}
}

func TestOpenScansMatchLegacyLists(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	base := time.Date(2026, 5, 1, 2, 3, 4, 0, time.UTC)
	transport := &archiveFakeTransport{fakeTransport: &fakeTransport{
		pulls: [][]PullRequestDTO{
			{{ID: 4, Index: 1, Title: "one", State: "open", Created: base, Updated: base}},
			{{ID: 5, Index: 2, Title: "two", State: "open", Created: base, Updated: base}},
		},
		issues: [][]IssueDTO{{
			{ID: 6, Index: 3, Title: "issue", State: "open", Created: base, Updated: base},
			{ID: 7, Index: 4, Title: "pull dupe", State: "open", Created: base, Updated: base, IsPullRequest: true},
		}},
	}}
	provider := NewProvider(platform.KindForgejo, "forge.example", transport)
	ref := pagesTestRef()

	issuePage, err := provider.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateOpen, Order: platform.ItemOrderCreated,
	})
	require.NoError(err)
	mrPage, err := provider.ListMergeRequestsPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateOpen, Order: platform.ItemOrderCreated,
	})
	require.NoError(err)
	openIssues, err := platform.ListOpenIssues(t.Context(), provider, ref)
	require.NoError(err)
	openMRs, err := platform.ListOpenMergeRequests(t.Context(), provider, ref)
	require.NoError(err)

	assert.True(issuePage.Exhausted)
	assert.True(mrPage.Exhausted)
	assert.Equal(openIssues, issuePage.Items)
	assert.Equal(openMRs, mrPage.Items)
	require.Len(issuePage.Items, 1)
	assert.Equal(3, issuePage.Items[0].Number)
	assert.Equal([]int{1, 2}, []int{mrPage.Items[0].Number, mrPage.Items[1].Number})
}

func TestIssueInventoryLegacyMatchesCanonical(t *testing.T) {
	assert := assert.New(t)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	since := base
	ref := pagesTestRef()

	historicalCanonical := NewProvider(platform.KindForgejo, "forge.example", newInventoryFakeTransport(base))
	historicalLegacy := NewProvider(platform.KindForgejo, "forge.example", newInventoryFakeTransport(base))
	canonical := drainPages(t, func(ctx context.Context, cursor string) (platform.Page[platform.Issue], error) {
		return historicalCanonical.ListIssuesPage(ctx, ref, platform.ItemPageQuery{
			State: platform.ItemStateAll, Order: platform.ItemOrderCreated, Cursor: cursor,
		})
	})
	legacy := drainPages(t, func(ctx context.Context, cursor string) (platform.Page[platform.Issue], error) {
		return historicalLegacy.ListHistoricalIssues(ctx, ref, cursor)
	})
	assert.Equal(legacy, canonical)
	assert.Equal([]int{1, 3}, []int{canonical[0].Number, canonical[1].Number},
		"historical traversal is created-ascending and excludes pull requests")

	updatedCanonicalTransport := newInventoryFakeTransport(base)
	updatedCanonicalProvider := NewProvider(platform.KindForgejo, "forge.example", updatedCanonicalTransport)
	updatedLegacyProvider := NewProvider(platform.KindForgejo, "forge.example", newInventoryFakeTransport(base))
	updatedCanonical := drainPages(t, func(ctx context.Context, cursor string) (platform.Page[platform.Issue], error) {
		return updatedCanonicalProvider.ListIssuesPage(ctx, ref, platform.ItemPageQuery{
			State: platform.ItemStateAll, Order: platform.ItemOrderUpdated,
			UpdatedSince: &since, Cursor: cursor,
		})
	})
	updatedLegacy := drainPages(t, func(ctx context.Context, cursor string) (platform.Page[platform.Issue], error) {
		return updatedLegacyProvider.ListUpdatedIssues(ctx, ref, since, cursor)
	})
	assert.Equal(updatedLegacy, updatedCanonical)
	assert.Equal(since.Add(-time.Second), updatedCanonicalTransport.issueOptions[0].Since,
		"maintenance scans pass the inclusive watermark overlap to the provider")
}

func TestMergeRequestInventoryLegacyMatchesCanonical(t *testing.T) {
	assert := assert.New(t)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	since := base
	ref := pagesTestRef()

	historicalCanonical := NewProvider(platform.KindForgejo, "forge.example", newInventoryFakeTransport(base))
	historicalLegacy := NewProvider(platform.KindForgejo, "forge.example", newInventoryFakeTransport(base))
	canonical := drainPages(t, func(ctx context.Context, cursor string) (platform.Page[platform.MergeRequest], error) {
		return historicalCanonical.ListMergeRequestsPage(ctx, ref, platform.ItemPageQuery{
			State: platform.ItemStateAll, Order: platform.ItemOrderCreated, Cursor: cursor,
		})
	})
	legacy := drainPages(t, func(ctx context.Context, cursor string) (platform.Page[platform.MergeRequest], error) {
		return historicalLegacy.ListHistoricalMergeRequests(ctx, ref, cursor)
	})
	assert.Equal(legacy, canonical)
	assert.Equal([]int{1, 2}, []int{canonical[0].Number, canonical[1].Number})

	updatedCanonicalTransport := newInventoryFakeTransport(base)
	updatedCanonicalProvider := NewProvider(platform.KindForgejo, "forge.example", updatedCanonicalTransport)
	updatedLegacyProvider := NewProvider(platform.KindForgejo, "forge.example", newInventoryFakeTransport(base))
	updatedCanonical := drainPages(t, func(ctx context.Context, cursor string) (platform.Page[platform.MergeRequest], error) {
		return updatedCanonicalProvider.ListMergeRequestsPage(ctx, ref, platform.ItemPageQuery{
			State: platform.ItemStateAll, Order: platform.ItemOrderUpdated,
			UpdatedSince: &since, Cursor: cursor,
		})
	})
	updatedLegacy := drainPages(t, func(ctx context.Context, cursor string) (platform.Page[platform.MergeRequest], error) {
		return updatedLegacyProvider.ListUpdatedMergeRequests(ctx, ref, since, cursor)
	})
	assert.Equal(updatedLegacy, updatedCanonical)
	assert.Equal("recentupdate", updatedCanonicalTransport.pullOptions[0].Sort)
}

func TestDetailPagesLegacyMatchesCanonical(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	transport := &archiveFakeTransport{
		fakeTransport:     &fakeTransport{},
		issueCommentPages: map[int][]CommentDTO{1: {{ID: 11, Body: "issue note", Created: now}}},
		pullCommentPages:  map[int][]CommentDTO{1: {{ID: 21, Body: "pull note", Created: now}}},
		commentPage:       map[int]Page{},
		reviews: []ReviewDTO{
			{ID: 31, Submitted: now, User: UserDTO{UserName: "bob"}, State: "APPROVED"},
			{ID: 32, Submitted: now, User: UserDTO{UserName: "carol"}, State: "REQUEST_REVIEW"},
			{ID: 33, User: UserDTO{UserName: "dan"}, State: "PENDING"},
		},
	}
	provider := NewProvider(platform.KindForgejo, "forge.example", transport)
	ref := pagesTestRef()

	issueCanonical, err := provider.ListIssueCommentsPage(t.Context(), ref, 7, "")
	require.NoError(err)
	issueLegacy, err := provider.ListArchiveIssueComments(t.Context(), ref, 7, "")
	require.NoError(err)
	mrCanonical, err := provider.ListMergeRequestCommentsPage(t.Context(), ref, 8, "")
	require.NoError(err)
	mrLegacy, err := provider.ListArchiveMergeRequestComments(t.Context(), ref, 8, "")
	require.NoError(err)
	reviewCanonical, err := provider.ListSubmittedReviewsPage(t.Context(), ref, 8, "")
	require.NoError(err)
	reviewLegacy, err := provider.ListArchiveSubmittedReviews(t.Context(), ref, 8, "")
	require.NoError(err)

	assert.Equal(issueLegacy, issueCanonical)
	assert.Equal(mrLegacy, mrCanonical)
	assert.Equal(reviewLegacy, reviewCanonical)
	assert.True(issueCanonical.Exhausted)
	require.Len(issueCanonical.Items, 1)
	assert.Equal("issue_comment", issueCanonical.Items[0].EventType)
	require.Len(mrCanonical.Items, 1)
	assert.Equal("issue_comment", mrCanonical.Items[0].EventType)
	require.Len(reviewCanonical.Items, 1)
	assert.Equal("31", reviewCanonical.Items[0].PlatformExternalID,
		"only submitted review states survive the canonical filter")
}

func TestReviewThreadsPageIsTypedUnsupported(t *testing.T) {
	transport := &archiveFakeTransport{fakeTransport: &fakeTransport{}}
	provider := NewProvider(platform.KindGitea, "git.example", transport)
	ref := platform.RepoRef{Platform: platform.KindGitea, Host: "git.example", Owner: "owner", Name: "repo"}

	_, canonicalErr := provider.ListReviewThreadsPage(t.Context(), ref, 7, "")
	_, legacyErr := provider.ListArchiveReviewThreads(t.Context(), ref, 7, "")

	require.ErrorIs(t, canonicalErr, platform.ErrUnsupportedCapability)
	require.ErrorIs(t, legacyErr, platform.ErrUnsupportedCapability)
}

func TestLiveIssueEventsCollectCanonicalCommentPages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	transport := &archiveFakeTransport{
		fakeTransport: &fakeTransport{},
		issueCommentPages: map[int][]CommentDTO{
			1: {{ID: 11, Body: "first issue comment", Created: now}},
			2: {{ID: 12, Body: "second issue comment", Created: now.Add(time.Minute)}},
		},
		commentPage: map[int]Page{1: {Next: 2}},
	}
	provider := NewProvider(platform.KindForgejo, "forge.example", transport)

	events, err := provider.ListIssueEvents(t.Context(), pagesTestRef(), 7)
	require.NoError(err)

	require.Len(events, 2)
	assert.Equal([]string{"11", "12"}, []string{
		events[0].PlatformExternalID, events[1].PlatformExternalID,
	})
	assert.Equal([]int{1, 2}, transport.issueCommentRequests,
		"live issue events must drain every canonical comment page")
}

func TestLiveMergeRequestEventsAggregateCanonicalPagesAndCommits(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	transport := &archiveFakeTransport{
		fakeTransport: &fakeTransport{},
		pullCommentPages: map[int][]CommentDTO{
			1: {{ID: 21, Body: "first pull comment", Created: now}},
			2: {{ID: 22, Body: "second pull comment", Created: now.Add(time.Minute)}},
		},
		commentPage: map[int]Page{1: {Next: 2}},
		reviewPages: map[int][]ReviewDTO{
			1: {
				{ID: 31, Submitted: now, User: UserDTO{UserName: "bob"}, State: "APPROVED"},
				{ID: 32, Submitted: now, User: UserDTO{UserName: "carol"}, State: "REQUEST_REVIEW"},
			},
			2: {{ID: 33, Submitted: now.Add(time.Minute), User: UserDTO{UserName: "dan"}, State: "COMMENT"}},
		},
		reviewPageMap: map[int]Page{1: {Next: 2}},
		commits:       []CommitDTO{{SHA: "abc123", AuthorName: "erin", Message: "commit", Created: now}},
	}
	provider := NewProvider(platform.KindForgejo, "forge.example", transport)

	events, err := provider.ListMergeRequestEvents(t.Context(), pagesTestRef(), 8)
	require.NoError(err)

	ids := make([]string, 0, len(events))
	types := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.PlatformExternalID)
		types = append(types, event.EventType)
	}
	assert.Equal([]string{"21", "22", "31", "33", "abc123"}, ids,
		"multi-page comments and submitted reviews aggregate ahead of commits; non-submitted reviews are filtered")
	assert.Equal([]string{"issue_comment", "issue_comment", "review", "review", "commit"}, types)
	assert.Equal([]int{1, 2}, transport.pullCommentRequests)
	assert.Equal([]int{1, 2}, transport.reviewRequests)
}

func TestCollectTransportPagesRejectsRepeatedPages(t *testing.T) {
	_, err := collectTransportPages(t.Context(), func(_ context.Context, opts PageOptions) ([]int, Page, error) {
		return []int{opts.Page}, Page{Next: opts.Page}, nil
	})
	require.ErrorIs(t, err, platform.ErrProviderContract)
}

func TestCollectTransportPagesBoundsPageBudget(t *testing.T) {
	_, err := collectTransportPages(t.Context(), func(_ context.Context, opts PageOptions) ([]int, Page, error) {
		return []int{opts.Page}, Page{Next: opts.Page + 1}, nil
	})
	require.ErrorIs(t, err, platform.ErrPageLimit)
}
