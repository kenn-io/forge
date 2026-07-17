package gitealike

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/platform"
)

func TestArchiveIssueInventoryWalksOldestPagesAndExcludesPullRequests(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	transport := &archiveFakeTransport{
		fakeTransport: &fakeTransport{},
		issuePages: map[int][]IssueDTO{
			1: {{ID: 3, Index: 3, Created: base.Add(2 * time.Hour), Updated: base.Add(2 * time.Hour)}},
			3: {
				{ID: 2, Index: 2, Created: base.Add(time.Hour), Updated: base.Add(time.Hour), IsPullRequest: true},
				{ID: 1, Index: 1, Created: base, Updated: base},
			},
		},
		issueLast: 3,
	}
	provider := NewProvider(platform.KindForgejo, "forge.example", transport)
	ref := platform.RepoRef{Platform: platform.KindForgejo, Host: "forge.example", Owner: "owner", Name: "repo"}

	discovery, err := provider.ListHistoricalIssues(context.Background(), ref, "")
	require.NoError(err)
	assert.True(discovery.ProgressOnly)
	assert.NotEmpty(discovery.NextCursor)

	oldest, err := provider.ListHistoricalIssues(context.Background(), ref, discovery.NextCursor)
	require.NoError(err)
	assert.Equal([]int{1}, []int{oldest.Items[0].Number})
	assert.Equal([]int{1, 3}, transport.issueRequests)
	assert.NotEmpty(oldest.NextCursor)

	_, err = provider.ListHistoricalIssues(context.Background(), platform.RepoRef{
		Platform: platform.KindForgejo, Host: "other.example", Owner: "owner", Name: "repo",
	}, oldest.NextCursor)
	assert.Error(err)
}

func TestArchiveUpdatedInventoryUsesInclusiveWatermarkAndProviderSort(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	since := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	transport := &archiveFakeTransport{
		fakeTransport: &fakeTransport{},
		issuePages: map[int][]IssueDTO{1: {
			{ID: 1, Index: 1, Created: since.Add(-time.Hour), Updated: since},
			{ID: 3, Index: 3, Created: since.Add(-time.Hour), Updated: since.Add(-500 * time.Millisecond)},
		}},
		pullPages: map[int][]PullRequestDTO{1: {{ID: 2, Index: 2, Created: since.Add(-time.Hour), Updated: since}}},
	}
	provider := NewProvider(platform.KindGitea, "git.example", transport)
	ref := platform.RepoRef{Platform: platform.KindGitea, Host: "git.example", Owner: "owner", Name: "repo"}

	issues, err := provider.ListUpdatedIssues(context.Background(), ref, since, "")
	require.NoError(err)
	pulls, err := provider.ListUpdatedMergeRequests(context.Background(), ref, since, "")
	require.NoError(err)

	assert.Len(issues.Items, 2)
	assert.Len(pulls.Items, 1)
	assert.Equal(since.Add(-time.Second), transport.issueOptions[0].Since)
	assert.Equal("recentupdate", transport.pullOptions[0].Sort)
	assert.Equal(2, transport.requests)
}

func TestArchiveUpdatedMergeRequestsStopAfterOverlappedWatermark(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	since := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	transport := &archiveFakeTransport{
		fakeTransport: &fakeTransport{},
		pullPages: map[int][]PullRequestDTO{
			1: {{ID: 3, Index: 3, Updated: since.Add(time.Minute)}},
			2: {
				{ID: 2, Index: 2, Updated: since.Add(-500 * time.Millisecond)},
				{ID: 1, Index: 1, Updated: since.Add(-2 * time.Second)},
			},
		},
		pullPage: map[int]Page{1: {Next: 2}, 2: {Next: 3}},
	}
	provider := NewProvider(platform.KindForgejo, "forge.example", transport)
	ref := platform.RepoRef{Platform: platform.KindForgejo, Host: "forge.example", Owner: "owner", Name: "repo"}

	first, err := provider.ListUpdatedMergeRequests(t.Context(), ref, since, "")
	require.NoError(err)
	second, err := provider.ListUpdatedMergeRequests(t.Context(), ref, since, first.NextCursor)
	require.NoError(err)

	assert.Equal([]int{3}, []int{first.Items[0].Number})
	require.Len(second.Items, 1)
	assert.Equal(2, second.Items[0].Number)
	assert.True(second.Exhausted)
	assert.Equal([]int{1, 2}, []int{transport.pullOptions[0].Page, transport.pullOptions[1].Page})
}

func TestArchiveDetailsAndLookupOutcomes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	transport := &archiveFakeTransport{
		fakeTransport: &fakeTransport{repo: RepositoryDTO{Name: "repo"}},
		comments:      []CommentDTO{{ID: 11, Created: now, User: UserDTO{UserName: "alice"}}},
		reviews: []ReviewDTO{
			{ID: 12, Submitted: now, User: UserDTO{UserName: "bob"}, State: "APPROVED"},
			{ID: 13, Submitted: now, User: UserDTO{UserName: "carol"}, State: "REQUEST_REVIEW"},
			{ID: 14, User: UserDTO{UserName: "dan"}, State: "PENDING"},
		},
		reviewPage: Page{Next: 2},
		issueErr:   &HTTPError{StatusCode: http.StatusNotFound, Err: errors.New("gone")},
	}
	provider := NewProvider(platform.KindForgejo, "forge.example", transport)
	ref := platform.RepoRef{Platform: platform.KindForgejo, Host: "forge.example", Owner: "owner", Name: "repo"}

	comments, err := provider.ListArchiveMergeRequestComments(context.Background(), ref, 7, "")
	require.NoError(err)
	reviews, err := provider.ListArchiveSubmittedReviews(context.Background(), ref, 7, "")
	require.NoError(err)
	removed, err := provider.GetArchiveIssue(context.Background(), ref, 8)
	require.NoError(err)
	_, inlineErr := provider.ListArchiveReviewThreads(context.Background(), ref, 7, "")

	assert.Equal("issue_comment", comments.Items[0].EventType)
	assert.Equal("review", reviews.Items[0].EventType)
	assert.Len(reviews.Items, 1)
	assert.False(reviews.ProgressOnly)
	assert.NotEmpty(reviews.NextCursor)
	assert.Equal(platform.LookupRemoved, removed.Outcome)
	require.ErrorIs(inlineErr, platform.ErrUnsupportedCapability)
	assert.Equal(platform.ArchiveCapabilities{
		HistoricalIssues: true, HistoricalMergeRequests: true,
		OrdinaryComments: true, SubmittedReviews: true,
	}, provider.Capabilities().Archive)
}

func TestLiveCommentsDrainCanonicalArchivePages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	transport := &archiveFakeTransport{
		fakeTransport: &fakeTransport{},
		issueCommentPages: map[int][]CommentDTO{
			1: {{ID: 11, Body: "first issue comment", Created: now}},
			2: {{ID: 12, Body: "second issue comment", Created: now.Add(time.Minute)}},
		},
		pullCommentPages: map[int][]CommentDTO{
			1: {{ID: 21, Body: "first pull comment", Created: now}},
			2: {{ID: 22, Body: "second pull comment", Created: now.Add(time.Minute)}},
		},
		commentPage: map[int]Page{1: {Next: 2}},
	}
	provider := NewProvider(platform.KindForgejo, "forge.example", transport)
	ref := platform.RepoRef{
		Platform: platform.KindForgejo, Host: "forge.example", Owner: "owner", Name: "repo",
	}

	issueComments, err := provider.ListIssueComments(t.Context(), ref, 7)
	require.NoError(err)
	mergeRequestComments, err := provider.ListMergeRequestComments(t.Context(), ref, 8)
	require.NoError(err)

	assert.Equal([]string{"11", "12"}, []string{
		issueComments[0].PlatformExternalID, issueComments[1].PlatformExternalID,
	})
	assert.Equal([]string{"21", "22"}, []string{
		mergeRequestComments[0].PlatformExternalID, mergeRequestComments[1].PlatformExternalID,
	})
	assert.Equal([]int{1, 2}, transport.issueCommentRequests)
	assert.Equal([]int{1, 2}, transport.pullCommentRequests)
}

func TestArchiveSubmittedReviewsReturnProgressPageWhenDraftsAreFiltered(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	transport := &archiveFakeTransport{
		fakeTransport: &fakeTransport{},
		reviews: []ReviewDTO{
			{ID: 1, Submitted: time.Now().UTC(), State: "REQUEST_REVIEW"},
			{ID: 2, State: "PENDING"},
		},
		reviewPage: Page{Next: 2},
	}
	provider := NewProvider(platform.KindGitea, "git.example", transport)
	ref := platform.RepoRef{Platform: platform.KindGitea, Host: "git.example", Owner: "owner", Name: "repo"}

	page, err := provider.ListArchiveSubmittedReviews(t.Context(), ref, 7, "")
	require.NoError(err)
	assert.Empty(page.Items)
	assert.True(page.ProgressOnly)
	assert.NotEmpty(page.NextCursor)
	assert.False(page.Exhausted)
}

func TestArchiveLookupReturnsRepositoryAccessFailure(t *testing.T) {
	transport := &archiveFakeTransport{
		fakeTransport: &fakeTransport{repoErr: &HTTPError{
			StatusCode: http.StatusNotFound, Err: errors.New("repository hidden"),
		}},
		issueErr: &HTTPError{StatusCode: http.StatusNotFound, Err: errors.New("item hidden")},
	}
	provider := NewProvider(platform.KindForgejo, "forge.example", transport)
	ref := platform.RepoRef{Platform: platform.KindForgejo, Host: "forge.example", Owner: "owner", Name: "repo"}

	_, err := provider.GetArchiveIssue(t.Context(), ref, 7)
	require.ErrorIs(t, err, platform.ErrPermissionDenied)
}

func TestArchiveLookupPreservesRepositoryProbeFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		itemStatus   int
		repoErr      error
		want         error
		notPermanent bool
	}{
		{name: "forbidden then server error", itemStatus: http.StatusForbidden, repoErr: &HTTPError{StatusCode: http.StatusInternalServerError, Err: errors.New("server unavailable")}, notPermanent: true},
		{name: "missing then server error", itemStatus: http.StatusNotFound, repoErr: &HTTPError{StatusCode: http.StatusInternalServerError, Err: errors.New("server unavailable")}, notPermanent: true},
		{name: "forbidden then rate limit", itemStatus: http.StatusForbidden, repoErr: &HTTPError{StatusCode: http.StatusTooManyRequests, Err: errors.New("slow down")}, want: platform.ErrRateLimited},
		{name: "missing then rate limit", itemStatus: http.StatusNotFound, repoErr: &HTTPError{StatusCode: http.StatusTooManyRequests, Err: errors.New("slow down")}, want: platform.ErrRateLimited},
		{name: "permission denied", itemStatus: http.StatusForbidden, repoErr: &HTTPError{StatusCode: http.StatusForbidden, Err: errors.New("hidden")}, want: platform.ErrPermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			transport := &archiveFakeTransport{
				fakeTransport: &fakeTransport{repoErr: tc.repoErr},
				issueErr:      &HTTPError{StatusCode: tc.itemStatus, Err: errors.New("item hidden")},
			}
			provider := NewProvider(platform.KindForgejo, "forge.example", transport)
			ref := platform.RepoRef{Platform: platform.KindForgejo, Host: "forge.example", Owner: "owner", Name: "repo"}

			_, err := provider.GetArchiveIssue(t.Context(), ref, 7)
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
			if tc.notPermanent {
				require.NotErrorIs(t, err, platform.ErrPermissionDenied)
			}
		})
	}
}

type archiveFakeTransport struct {
	*fakeTransport
	issuePages           map[int][]IssueDTO
	pullPages            map[int][]PullRequestDTO
	issueLast            int
	issueOptions         []ArchiveListOptions
	pullOptions          []ArchiveListOptions
	issueRequests        []int
	requests             int
	comments             []CommentDTO
	issueCommentPages    map[int][]CommentDTO
	pullCommentPages     map[int][]CommentDTO
	commentPage          map[int]Page
	issueCommentRequests []int
	pullCommentRequests  []int
	reviews              []ReviewDTO
	reviewPage           Page
	pullPage             map[int]Page
	issueErr             error
}

func (t *archiveFakeTransport) ListArchiveIssues(_ context.Context, _ platform.RepoRef, opts ArchiveListOptions) ([]IssueDTO, Page, error) {
	t.requests++
	t.issueRequests = append(t.issueRequests, opts.Page)
	t.issueOptions = append(t.issueOptions, opts)
	return t.issuePages[opts.Page], Page{Last: t.issueLast}, nil
}

func (t *archiveFakeTransport) ListArchivePullRequests(_ context.Context, _ platform.RepoRef, opts ArchiveListOptions) ([]PullRequestDTO, Page, error) {
	t.requests++
	t.pullOptions = append(t.pullOptions, opts)
	return t.pullPages[opts.Page], t.pullPage[opts.Page], nil
}

func (t *archiveFakeTransport) ListPullRequestComments(_ context.Context, _ platform.RepoRef, _ int, opts PageOptions) ([]CommentDTO, Page, error) {
	t.requests++
	t.pullCommentRequests = append(t.pullCommentRequests, opts.Page)
	if t.pullCommentPages != nil {
		return t.pullCommentPages[opts.Page], t.commentPage[opts.Page], nil
	}
	return t.comments, Page{}, nil
}

func (t *archiveFakeTransport) ListIssueComments(_ context.Context, _ platform.RepoRef, _ int, opts PageOptions) ([]CommentDTO, Page, error) {
	t.requests++
	t.issueCommentRequests = append(t.issueCommentRequests, opts.Page)
	return t.issueCommentPages[opts.Page], t.commentPage[opts.Page], nil
}

func (t *archiveFakeTransport) ListPullRequestReviews(context.Context, platform.RepoRef, int, PageOptions) ([]ReviewDTO, Page, error) {
	t.requests++
	return t.reviews, t.reviewPage, nil
}

func (t *archiveFakeTransport) GetIssue(context.Context, platform.RepoRef, int) (IssueDTO, error) {
	return IssueDTO{}, t.issueErr
}
