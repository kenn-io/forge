package github

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	gh "github.com/google/go-github/v88/github"
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
	// onPage runs before each stacks page is served so a test can model state
	// changing while the sync is in flight.
	onPage func()
}

func (c *nativeStackSyncTestClient) ListOpenPullRequestsWithNativeStackHints(
	context.Context, string, string,
) ([]*gh.PullRequest, map[int]*NativeStackHint, error) {
	return c.pulls, c.hints, nil
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
		name          string
		page          NativeStackPage
		hintSize      int
		wantConfirmed []int
		wantCached    int
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
			name:     "target resource disagrees with pull request size",
			page:     NativeStackPage{Stacks: []NativeStack{stack(42)}},
			hintSize: 3, wantConfirmed: []int{}, wantCached: 0,
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
	assert.Equal([]int{42}, partial.ConfirmedNumbers)
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
