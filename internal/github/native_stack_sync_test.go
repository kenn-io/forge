package github

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v88/github"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
)

type nativeStackSyncTestClient struct {
	*mockClient
	mu        sync.Mutex
	pulls     []*gh.PullRequest
	hints     map[int]*NativeStackHint
	pages     map[int]NativeStackPage
	errors    map[int]error
	pageCalls []int
	// listErrors is consumed one entry per open-PR list call so a test can model
	// a 304 arriving on a later sync.
	listErrors []error
	// onPage runs before each stacks page is served so a test can model state
	// changing while the sync is in flight.
	onPage func()
}

func (c *nativeStackSyncTestClient) ListOpenPullRequestsWithNativeStackHints(
	context.Context, string, string,
) ([]*gh.PullRequest, map[int]*NativeStackHint, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.listErrors) > 0 {
		err := c.listErrors[0]
		c.listErrors = c.listErrors[1:]
		if err != nil {
			return nil, nil, err
		}
	}
	return c.pulls, c.hints, nil
}

// notModified is the error the ETag transport surfaces for a 304 open-PR list.
func notModified() error {
	return &gh.ErrorResponse{Response: &http.Response{
		StatusCode: http.StatusNotModified,
		Request: &http.Request{
			Method: http.MethodGet,
			URL:    &url.URL{Scheme: "https", Host: "api.github.com", Path: "/repos/owner/repo/pulls"},
		},
	}}
}

func (c *nativeStackSyncTestClient) ListNativeStacksPage(
	_ context.Context, _, _ string, page int,
) (NativeStackPage, error) {
	if c.onPage != nil {
		c.onPage()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pageCalls = append(c.pageCalls, page)
	if err := c.errors[page]; err != nil {
		return NativeStackPage{}, err
	}
	return c.pages[page], nil
}

func TestRefreshGitHubNativeStackCacheReusesConsistentCache(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	require.NoError(database.ReplaceGitHubNativeStack(t.Context(), db.GitHubNativeStack{
		RepoID: repoID, GitHubID: 900, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "cached", LastObservedAt: now,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 101, State: "open", HeadRef: "a", HeadSHA: "aaa"},
			{Position: 2, PullRequestNumber: 102, State: "open", HeadRef: "b", HeadSHA: "bbb"},
		},
	}))
	client := &nativeStackSyncTestClient{mockClient: &mockClient{}}
	syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, nil, time.Minute, nil, nil)

	result := syncer.refreshGitHubNativeStackCache(t.Context(), RepoRef{
		Owner: "acme", Name: "widgets", PlatformHost: "github.com",
	}, repoID, map[int]*NativeStackHint{
		101: {Number: 42, Size: 2, Position: 1, BaseRef: "main"},
		102: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
	}, false)

	assert.Equal([]int{42}, result.ConfirmedNumbers)
	assert.Empty(client.pageCalls)
	unchanged := syncer.refreshGitHubNativeStackCache(
		t.Context(), RepoRef{Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
		repoID, nil, true,
	)
	assert.Equal([]int{42}, unchanged.ConfirmedNumbers)
}

func TestRefreshGitHubNativeStackCacheStopsAfterTargetIsFoundOrPassed(t *testing.T) {
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	stack := func(number int) NativeStack {
		return NativeStack{
			ID: int64(1000 + number), Number: number, BaseRef: "main",
			Open: true, CreatedAt: now,
			Members: []NativeStackMember{
				{Position: 1, PullRequestNumber: 101, State: "open", HeadRef: "a", HeadSHA: "aaa"},
				{Position: 2, PullRequestNumber: 102, State: "open", HeadRef: "b", HeadSHA: "bbb"},
			},
		}
	}
	cases := []struct {
		name              string
		page              NativeStackPage
		hintSize          int
		wantConfirmed     []int
		wantCached        int
		wantInvalidations int32
	}{
		{
			name: "target found on first page",
			page: NativeStackPage{Stacks: []NativeStack{
				stack(50), stack(42), stack(40),
			}, NextPage: 2},
			wantConfirmed: []int{42}, wantCached: 1,
		},
		{
			name: "target passed on first page",
			page: NativeStackPage{Stacks: []NativeStack{
				stack(50), stack(41),
			}, NextPage: 2},
			wantConfirmed: []int{}, wantCached: 0,
		},
		{
			// A rejected row leaves the target unaccounted for, so the pass is
			// partial: nothing projects and the next sync must re-list.
			name:     "target resource disagrees with pull request size",
			page:     NativeStackPage{Stacks: []NativeStack{stack(42)}},
			hintSize: 3, wantConfirmed: nil, wantCached: 0, wantInvalidations: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := openTestDB(t)
			repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
			require.NoError(err)
			client := &nativeStackSyncTestClient{
				mockClient: &mockClient{}, pages: map[int]NativeStackPage{1: tc.page},
			}
			syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, nil, time.Minute, nil, nil)
			syncer.now = func() time.Time { return now }
			hintSize := tc.hintSize
			if hintSize == 0 {
				hintSize = 2
			}

			result := syncer.refreshGitHubNativeStackCache(t.Context(), RepoRef{
				Owner: "acme", Name: "widgets", PlatformHost: "github.com",
			}, repoID, map[int]*NativeStackHint{
				101: {Number: 42, Size: hintSize, Position: 1, BaseRef: "main"},
				102: {Number: 42, Size: hintSize, Position: 2, BaseRef: "main"},
			}, false)

			assert.Equal(tc.wantConfirmed, result.ConfirmedNumbers)
			assert.Equal([]int{1}, client.pageCalls)
			assert.Equal(tc.wantInvalidations, client.invalidateCalls.Load())
			cached, err := database.ListGitHubNativeStacks(t.Context(), repoID)
			require.NoError(err)
			assert.Len(cached, tc.wantCached)
		})
	}
}

func TestRefreshGitHubNativeStackCacheTreatsPreviewNotFoundAsFallback(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	client := &nativeStackSyncTestClient{
		mockClient: &mockClient{},
		errors:     map[int]error{1: &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotFound}}},
	}
	syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, nil, time.Minute, nil, nil)

	result := syncer.refreshGitHubNativeStackCache(t.Context(), RepoRef{
		Owner: "acme", Name: "widgets", PlatformHost: "github.com",
	}, repoID, map[int]*NativeStackHint{
		101: {Number: 42, Size: 2, Position: 1, BaseRef: "main"},
	}, false)

	assert.Empty(t, result.ConfirmedNumbers)
}

func TestRefreshGitHubNativeStackCacheDoesNotReconfirmSuspectCacheAfterNotModified(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	require.NoError(database.ReplaceGitHubNativeStack(t.Context(), db.GitHubNativeStack{
		RepoID: repoID, GitHubID: 900, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "cached", LastObservedAt: now,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 101, State: "open", HeadRef: "a", HeadSHA: "aaa"},
			{Position: 2, PullRequestNumber: 102, State: "open", HeadRef: "b", HeadSHA: "bbb"},
		},
	}))
	client := &nativeStackSyncTestClient{
		mockClient: &mockClient{},
		errors: map[int]error{1: &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusServiceUnavailable},
		}},
	}
	syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, nil, time.Minute, nil, nil)
	repo := RepoRef{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}

	failed := syncer.refreshGitHubNativeStackCache(t.Context(), repo, repoID, map[int]*NativeStackHint{
		101: {Number: 42, Size: 3, Position: 1, BaseRef: "main"},
	}, false)
	assert.Empty(t, failed.ConfirmedNumbers)
	unchanged := syncer.refreshGitHubNativeStackCache(t.Context(), repo, repoID, nil, true)
	assert.Empty(t, unchanged.ConfirmedNumbers)
}

func TestRefreshGitHubNativeStackCacheRefetchesUnobservableMembersOnSchedule(t *testing.T) {
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name           string
		lastObservedAt time.Time
		wantPageCalls  []int
	}{
		{
			name:           "recent observation reuses the cache",
			lastObservedAt: now.Add(-time.Hour),
			wantPageCalls:  nil,
		},
		{
			name:           "stale observation refetches the stack",
			lastObservedAt: now.Add(-nativeStackObservationTTL - time.Hour),
			wantPageCalls:  []int{1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := openTestDB(t)
			repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
			require.NoError(err)
			// PR 100 is merged, so no open-PR hint can attest to its position.
			require.NoError(database.ReplaceGitHubNativeStack(t.Context(), db.GitHubNativeStack{
				RepoID: repoID, GitHubID: 900, Number: 42, Size: 2,
				BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
				ContentFingerprint: "cached", LastObservedAt: tc.lastObservedAt,
				Members: []db.GitHubNativeStackMember{
					{Position: 1, PullRequestNumber: 100, State: "merged", HeadRef: "a", HeadSHA: "aaa"},
					{Position: 2, PullRequestNumber: 101, State: "open", HeadRef: "b", HeadSHA: "bbb"},
				},
			}))
			client := &nativeStackSyncTestClient{
				mockClient: &mockClient{},
				pages: map[int]NativeStackPage{1: {Stacks: []NativeStack{{
					ID: 900, Number: 42, BaseRef: "main", Open: true, CreatedAt: now,
					Members: []NativeStackMember{
						{Position: 1, PullRequestNumber: 100, State: "merged", HeadRef: "a", HeadSHA: "aaa"},
						{Position: 2, PullRequestNumber: 101, State: "open", HeadRef: "b", HeadSHA: "bbb"},
					},
				}}}},
			}
			syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, nil, time.Minute, nil, nil)
			syncer.now = func() time.Time { return now }

			result := syncer.refreshGitHubNativeStackCache(t.Context(), RepoRef{
				Owner: "acme", Name: "widgets", PlatformHost: "github.com",
			}, repoID, map[int]*NativeStackHint{
				101: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
			}, false)

			assert.Equal([]int{42}, result.ConfirmedNumbers)
			assert.Equal(tc.wantPageCalls, client.pageCalls)
		})
	}
}

func TestRefreshGitHubNativeStackCacheExpiresConfirmationsReusedByNotModified(t *testing.T) {
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name          string
		elapsed       time.Duration
		wantConfirmed []int
		wantInvalidem int32
	}{
		{
			name: "within the revalidation window", elapsed: time.Hour,
			wantConfirmed: []int{42}, wantInvalidem: 0,
		},
		{
			name: "past the revalidation window", elapsed: nativeStackObservationTTL + time.Hour,
			wantConfirmed: nil, wantInvalidem: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := openTestDB(t)
			repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
			require.NoError(err)
			// PR 100 is merged, so the confirmation covers membership that an
			// unchanged pull-request list can never contradict.
			require.NoError(database.ReplaceGitHubNativeStack(t.Context(), db.GitHubNativeStack{
				RepoID: repoID, GitHubID: 900, Number: 42, Size: 2,
				BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
				ContentFingerprint: "cached", LastObservedAt: now,
				Members: []db.GitHubNativeStackMember{
					{Position: 1, PullRequestNumber: 100, State: "merged", HeadRef: "a", HeadSHA: "aaa"},
					{Position: 2, PullRequestNumber: 101, State: "open", HeadRef: "b", HeadSHA: "bbb"},
				},
			}))
			client := &nativeStackSyncTestClient{mockClient: &mockClient{}}
			syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, nil, time.Minute, nil, nil)
			clock := now
			syncer.now = func() time.Time { return clock }
			repo := RepoRef{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}

			seeded := syncer.refreshGitHubNativeStackCache(t.Context(), repo, repoID, map[int]*NativeStackHint{
				101: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
			}, false)
			require.Equal([]int{42}, seeded.ConfirmedNumbers)

			clock = now.Add(tc.elapsed)
			unchanged := syncer.refreshGitHubNativeStackCache(t.Context(), repo, repoID, nil, true)

			assert.Equal(tc.wantConfirmed, unchanged.ConfirmedNumbers)
			assert.Equal(tc.wantInvalidem, client.invalidateCalls.Load())
		})
	}
}

func TestRefreshGitHubNativeStackCacheKeepsDeadlineTiedToStackObservation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	observed := time.Date(2026, time.July, 24, 0, 0, 0, 0, time.UTC)
	// The row is already 11 hours old and still holds a merged member.
	require.NoError(database.ReplaceGitHubNativeStack(t.Context(), db.GitHubNativeStack{
		RepoID: repoID, GitHubID: 900, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: observed,
		ContentFingerprint: "cached", LastObservedAt: observed,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 100, State: "merged", HeadRef: "a", HeadSHA: "aaa"},
			{Position: 2, PullRequestNumber: 101, State: "open", HeadRef: "b", HeadSHA: "bbb"},
		},
	}))
	client := &nativeStackSyncTestClient{mockClient: &mockClient{}}
	syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, nil, time.Minute, nil, nil)
	clock := observed.Add(11 * time.Hour)
	syncer.now = func() time.Time { return clock }
	repo := RepoRef{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}

	// An unrelated 200 response reconfirms the stack from cache without
	// refetching the catalog.
	seeded := syncer.refreshGitHubNativeStackCache(t.Context(), repo, repoID, map[int]*NativeStackHint{
		101: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
	}, false)
	require.Equal([]int{42}, seeded.ConfirmedNumbers)
	require.Empty(client.pageCalls)

	// Two hours later the stack is 13 hours past its own observation, so the
	// confirmation must not survive on a deadline this refresh granted.
	clock = observed.Add(13 * time.Hour)
	unchanged := syncer.refreshGitHubNativeStackCache(t.Context(), repo, repoID, nil, true)

	assert.Empty(unchanged.ConfirmedNumbers)
	assert.EqualValues(1, client.invalidateCalls.Load())
}

// TestRunOnceWithdrawsAgedNativeStacksFromProjectionInput drives the aging rule
// through a real sync rather than the cache helper: the projection only ever
// sees what RunOnce publishes, so a deadline that survives the sync path is the
// one that would keep a stale predecessor out of the merge safeguard.
func TestRunOnceWithdrawsAgedNativeStacksFromProjectionInput(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	observed := time.Date(2026, time.July, 24, 0, 0, 0, 0, time.UTC)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	// The cached stack claims a merged leading member, which no open-PR hint can
	// attest to, and it was last observed 11 hours ago.
	require.NoError(database.ReplaceGitHubNativeStack(t.Context(), db.GitHubNativeStack{
		RepoID: repoID, GitHubID: 9001, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: observed,
		ContentFingerprint: "cached", LastObservedAt: observed,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 900, State: "merged", HeadRef: "feature/z", HeadSHA: "zzz"},
			{Position: 2, PullRequestNumber: 101, State: "open", HeadRef: "feature/b", HeadSHA: "bbb"},
		},
	}))
	tip := buildOpenPR(101, observed)
	tip.Head.Ref = new("feature/b")
	client := &nativeStackSyncTestClient{
		mockClient: &mockClient{},
		pulls:      []*gh.PullRequest{tip},
		hints: map[int]*NativeStackHint{
			101: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
		},
		// A second sync finds the open-PR list byte-identical.
		listErrors: []error{nil, notModified()},
	}
	repo := RepoRef{
		Owner: "owner", Name: "repo", PlatformHost: "github.com",
		PlatformExternalID: "repo-owner-repo",
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	clock := observed.Add(11 * time.Hour)
	syncer.now = func() time.Time { return clock }
	syncer.SetPreferGitHubNativeStacks(true)
	var results []RepoSyncResult
	syncer.SetOnSyncCompleted(func(got []RepoSyncResult) { results = got })

	syncer.RunOnce(t.Context())
	require.Len(results, 1)
	require.NotNil(results[0].GitHubNativeStacks)
	require.Equal([]int{42}, results[0].GitHubNativeStacks.ConfirmedNumbers,
		"a cache-confirmed stack still inside its window must project")
	require.Empty(client.pageCalls, "reconfirming from cache must not refetch the catalog")

	// Enabling the preference and the first sync both invalidate ETags, so only
	// the delta across the aging sync says anything.
	invalidatesBefore := client.invalidateCalls.Load()

	// Two hours later the stack is past its own 12h window. The 304 must not
	// reuse a confirmation that this sync's own clock would have extended.
	clock = observed.Add(13 * time.Hour)
	syncer.RunOnce(t.Context())

	require.Len(results, 1)
	require.NotNil(results[0].GitHubNativeStacks)
	assert.Empty(results[0].GitHubNativeStacks.ConfirmedNumbers,
		"an aged stack must be withheld from projection so branch inference owns the repo")
	assert.Equal(invalidatesBefore+1, client.invalidateCalls.Load(),
		"the pull-request list ETag must be evicted so the next sync refetches the catalog")
}

func TestRefreshGitHubNativeStackCacheMarksFailedPersistenceIncomplete(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(t.Context())
	client := &nativeStackSyncTestClient{
		mockClient: &mockClient{},
		pages: map[int]NativeStackPage{1: {Stacks: []NativeStack{{
			ID: 900, Number: 42, BaseRef: "main", Open: true, CreatedAt: now,
			Members: []NativeStackMember{
				{Position: 1, PullRequestNumber: 101, State: "open", HeadRef: "a", HeadSHA: "aaa"},
				{Position: 2, PullRequestNumber: 102, State: "open", HeadRef: "b", HeadSHA: "bbb"},
			},
		}}}},
	}
	// Cancel after the catalog page is served so persistence, not the catalog
	// fetch, is what fails.
	client.onPage = cancel
	syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, nil, time.Minute, nil, nil)
	syncer.now = func() time.Time { return now }
	repo := RepoRef{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}
	hints := map[int]*NativeStackHint{
		101: {Number: 42, Size: 2, Position: 1, BaseRef: "main"},
		102: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
	}

	result := syncer.refreshGitHubNativeStackCache(ctx, repo, repoID, hints, false)

	assert.Empty(result.ConfirmedNumbers)
	assert.EqualValues(1, client.invalidateCalls.Load(),
		"a refresh whose persistence failed must force the next sync to re-list")
	unchanged := syncer.refreshGitHubNativeStackCache(t.Context(), repo, repoID, nil, true)
	assert.Empty(unchanged.ConfirmedNumbers)
}

func TestRefreshGitHubNativeStackCacheRejectsMemberClaimedByAnotherStack(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	// Cached stack 42 still lists PR 103 from when it was closed.
	require.NoError(database.ReplaceGitHubNativeStack(t.Context(), db.GitHubNativeStack{
		RepoID: repoID, GitHubID: 900, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "cached", LastObservedAt: now,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 101, State: "open", HeadRef: "a", HeadSHA: "aaa"},
			{Position: 2, PullRequestNumber: 103, State: "closed", HeadRef: "c", HeadSHA: "ccc"},
		},
	}))
	client := &nativeStackSyncTestClient{
		mockClient: &mockClient{},
		// The refetch of stack 42 still reports the reopened PR as closed.
		pages: map[int]NativeStackPage{1: {Stacks: []NativeStack{{
			ID: 900, Number: 42, BaseRef: "main", Open: true, CreatedAt: now,
			Members: []NativeStackMember{
				{Position: 1, PullRequestNumber: 101, State: "open", HeadRef: "a", HeadSHA: "aaa"},
				{Position: 2, PullRequestNumber: 103, State: "closed", HeadRef: "c", HeadSHA: "ccc"},
			},
		}}}},
	}
	syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, nil, time.Minute, nil, nil)
	syncer.now = func() time.Time { return now }

	// PR 103 is open again and GitHub now reports it in stack 43.
	result := syncer.refreshGitHubNativeStackCache(t.Context(), RepoRef{
		Owner: "acme", Name: "widgets", PlatformHost: "github.com",
	}, repoID, map[int]*NativeStackHint{
		101: {Number: 42, Size: 2, Position: 1, BaseRef: "main"},
		103: {Number: 43, Size: 2, Position: 2, BaseRef: "main"},
	}, false)

	assert.NotContains(result.ConfirmedNumbers, 42,
		"a stack whose member now belongs to another stack must not stay confirmed")
}

func TestRefreshGitHubNativeStackCacheDoesNotReuseIncompleteRefreshAfterNotModified(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	require.NoError(database.ReplaceGitHubNativeStack(t.Context(), db.GitHubNativeStack{
		RepoID: repoID, GitHubID: 900, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "cached", LastObservedAt: now,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 101, State: "open", HeadRef: "a", HeadSHA: "aaa"},
			{Position: 2, PullRequestNumber: 102, State: "open", HeadRef: "b", HeadSHA: "bbb"},
		},
	}))
	client := &nativeStackSyncTestClient{
		mockClient: &mockClient{},
		errors: map[int]error{1: &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusServiceUnavailable},
		}},
	}
	syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, nil, time.Minute, nil, nil)
	repo := RepoRef{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}
	// Stack 42 is confirmable from cache, while PR 103 points at an uncached
	// stack whose catalog fetch fails: a partial refresh.
	hints := map[int]*NativeStackHint{
		101: {Number: 42, Size: 2, Position: 1, BaseRef: "main"},
		102: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
		103: {Number: 43, Size: 1, Position: 1, BaseRef: "main"},
	}

	partial := syncer.refreshGitHubNativeStackCache(t.Context(), repo, repoID, hints, false)
	// Stack 42 alone cannot be projected either: stack 43 stayed unresolved, so
	// detection's overlap scan cannot see whether 42 claims a pull request 43
	// also holds, and projecting 42 could hide 43's predecessor.
	assert.Empty(partial.ConfirmedNumbers,
		"an incomplete refresh must project nothing rather than an unverifiable subset")
	assert.EqualValues(1, client.invalidateCalls.Load(),
		"an incomplete refresh must evict the pull-request list ETag so the next sync retries")

	unchanged := syncer.refreshGitHubNativeStackCache(t.Context(), repo, repoID, nil, true)
	assert.Empty(unchanged.ConfirmedNumbers,
		"a 304 must not reuse confirmations from an incomplete refresh")
}

func TestRunOnceDropsNativeStacksDisabledDuringSync(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	first := buildOpenPR(101, now)
	second := buildOpenPR(102, now)
	first.Head.Ref = new("feature/a")
	second.Head.Ref = new("feature/b")
	client := &nativeStackSyncTestClient{
		mockClient: &mockClient{},
		pulls:      []*gh.PullRequest{first, second},
		hints: map[int]*NativeStackHint{
			101: {Number: 42, Size: 2, Position: 1, BaseRef: "main"},
			102: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
		},
		pages: map[int]NativeStackPage{1: {
			Stacks: []NativeStack{{
				ID: 9001, Number: 42, BaseRef: "main", Open: true, CreatedAt: now,
				Members: []NativeStackMember{
					{Position: 1, PullRequestNumber: 101, State: "open", HeadRef: "feature/a", HeadSHA: "aaa"},
					{Position: 2, PullRequestNumber: 102, State: "open", HeadRef: "feature/b", HeadSHA: "bbb"},
				},
			}},
		}},
	}
	repo := RepoRef{
		Owner: "owner", Name: "repo", PlatformHost: "github.com",
		PlatformExternalID: "repo-owner-repo",
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	syncer.SetPreferGitHubNativeStacks(true)
	// Model the user turning the preview off while this sync is still running.
	client.onPage = func() { syncer.SetPreferGitHubNativeStacks(false) }
	var results []RepoSyncResult
	syncer.SetOnSyncCompleted(func(got []RepoSyncResult) { results = got })

	syncer.RunOnce(t.Context())

	require.Len(results, 1)
	assert.Nil(results[0].GitHubNativeStacks,
		"a result captured under the enabled preference must not project after it is disabled")
}

func TestRunOnceKeepsRESTHintsWhenGraphQLRejectsNativeStackFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	first := buildOpenPR(101, now)
	second := buildOpenPR(102, now)
	first.Head.Ref = new("feature/a")
	second.Head.Ref = new("feature/b")
	// GraphQL rejects the preview fields; the fallback query succeeds but says
	// nothing about stack membership.
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if bytes.Contains(body, []byte("stackEntry")) {
			_, _ = w.Write([]byte(`{"errors":[{"message":"Field 'stackEntry' doesn't exist on type 'PullRequest'"}]}`))
			return
		}
		if bytes.Contains(body, []byte("pullRequests")) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"totalCount":0,"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
	}))
	defer gqlSrv.Close()
	client := &nativeStackSyncTestClient{
		mockClient: &mockClient{},
		pulls:      []*gh.PullRequest{first, second},
		hints: map[int]*NativeStackHint{
			101: {Number: 42, Size: 2, Position: 1, BaseRef: "main"},
			102: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
		},
		pages: map[int]NativeStackPage{1: {
			Stacks: []NativeStack{{
				ID: 9001, Number: 42, BaseRef: "main", Open: true, CreatedAt: now,
				Members: []NativeStackMember{
					{Position: 1, PullRequestNumber: 101, State: "open", HeadRef: "feature/a", HeadSHA: "aaa"},
					{Position: 2, PullRequestNumber: 102, State: "open", HeadRef: "feature/b", HeadSHA: "bbb"},
				},
			}},
		}},
	}
	repo := RepoRef{
		Owner: "owner", Name: "repo", PlatformHost: "github.com",
		PlatformExternalID: "repo-owner-repo",
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client()), nil,
		),
	})
	syncer.SetPreferGitHubNativeStacks(true)
	var results []RepoSyncResult
	syncer.SetOnSyncCompleted(func(got []RepoSyncResult) { results = got })

	syncer.RunOnce(t.Context())

	require.Len(results, 1)
	require.NotNil(results[0].GitHubNativeStacks,
		"REST-derived hints must survive a GraphQL query that dropped the preview fields")
	assert.Equal([]int{42}, results[0].GitHubNativeStacks.ConfirmedNumbers)
}

func TestSetPreferGitHubNativeStacksReportsTransitionToExactlyOneCaller(t *testing.T) {
	assert := assert.New(t)
	database := openTestDB(t)
	syncer := NewSyncer(
		map[string]Client{"github.com": &mockClient{}}, database, nil, nil,
		time.Minute, nil, nil,
	)
	syncer.SetPreferGitHubNativeStacks(true)

	// Competing config writers each disable the preference. Only the caller
	// whose swap observed the enabled value may reconcile projections, so the
	// restore cannot run twice or be skipped no matter how they interleave.
	const callers = 8
	var transitions atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range callers {
		wg.Go(func() {
			<-start
			if previous := syncer.SetPreferGitHubNativeStacks(false); previous {
				transitions.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()

	assert.EqualValues(1, transitions.Load())
	assert.False(syncer.preferGitHubNativeStacks.Load())
}

func TestSetPreferGitHubNativeStacksRefreshesHintsOnEnable(t *testing.T) {
	assert := assert.New(t)
	database := openTestDB(t)
	client := &nativeStackSyncTestClient{mockClient: &mockClient{}}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	syncer.SetPreferGitHubNativeStacks(true)
	assert.EqualValues(1, client.invalidateCalls.Load())
	syncer.SetPreferGitHubNativeStacks(true)
	assert.EqualValues(1, client.invalidateCalls.Load())
	syncer.SetPreferGitHubNativeStacks(false)
	assert.EqualValues(1, client.invalidateCalls.Load())
	syncer.SetPreferGitHubNativeStacks(true)
	assert.EqualValues(2, client.invalidateCalls.Load())
}

func TestRunOncePublishesConfirmedNativeStackNumbers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	first := buildOpenPR(101, now)
	second := buildOpenPR(102, now)
	first.Head.Ref = new("feature/a")
	second.Head.Ref = new("feature/b")
	client := &nativeStackSyncTestClient{
		mockClient: &mockClient{},
		pulls:      []*gh.PullRequest{first, second},
		hints: map[int]*NativeStackHint{
			101: {Number: 42, Size: 2, Position: 1, BaseRef: "main"},
			102: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
		},
		pages: map[int]NativeStackPage{1: {
			Stacks: []NativeStack{{
				ID: 9001, Number: 42, BaseRef: "main", Open: true, CreatedAt: now,
				Members: []NativeStackMember{
					{Position: 1, PullRequestNumber: 101, State: "open", HeadRef: "feature/a", HeadSHA: "aaa"},
					{Position: 2, PullRequestNumber: 102, State: "open", HeadRef: "feature/b", HeadSHA: "bbb"},
				},
			}},
		}},
	}
	repo := RepoRef{
		Owner: "owner", Name: "repo", PlatformHost: "github.com",
		PlatformExternalID: "repo-owner-repo",
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	syncer.SetPreferGitHubNativeStacks(true)
	var results []RepoSyncResult
	syncer.SetOnSyncCompleted(func(got []RepoSyncResult) { results = got })

	syncer.RunOnce(t.Context())

	require.Len(results, 1)
	require.NotNil(results[0].GitHubNativeStacks)
	assert.Equal([]int{42}, results[0].GitHubNativeStacks.ConfirmedNumbers)
}
