package github

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

// mergeQueueGraphQL serves the merge queue and removal queries with values the
// test swaps between refreshes.
type mergeQueueGraphQL struct {
	mu      sync.Mutex
	queue   string
	removal string
	queries atomic.Int32
}

func (g *mergeQueueGraphQL) set(queue, removal string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.queue = queue
	g.removal = removal
}

func (g *mergeQueueGraphQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.queries.Add(1)
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	g.mu.Lock()
	defer g.mu.Unlock()
	if bytes.Contains(body, []byte("RemovedFromMergeQueueEvent")) {
		_, _ = io.WriteString(w, g.removal)
		return
	}
	_, _ = io.WriteString(w, g.queue)
}

func newMergeQueueTestSyncer(
	t *testing.T, gql http.Handler,
) (*Syncer, *db.DB, RepoRef, int64) {
	t.Helper()
	return newMergeQueueTestSyncerWithClient(t, gql, &mockClient{})
}

func newMergeQueueTestSyncerWithClient(
	t *testing.T, gql http.Handler, client *mockClient,
) (*Syncer, *db.DB, RepoRef, int64) {
	t.Helper()
	database := openTestDB(t)
	repoID, err := reposeed.Seed(t.Context(), database, verifiedGitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 7001, Number: 7,
		Title: "Queued change", Author: "author", State: db.MergeRequestStateOpen,
		HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(t, err)

	srv := httptest.NewServer(gql)
	t.Cleanup(srv.Close)
	repo := RepoRef{
		Owner: "acme", Name: "widgets", PlatformHost: "github.com",
		Key: platform.RepositoryIDKey(testRepoID("acme", "widgets")),
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(srv.URL, srv.Client()), nil,
		),
	})
	return syncer, database, repo, repoID
}

const queuedPR7 = `{"data":{"repository":{
	"defaultBranchRef":{"name":"main"},
	"mergeQueue":{
		"configuration":{"mergeMethod":"SQUASH"},
		"entries":{"nodes":[{
			"position":1,"state":"AWAITING_CHECKS",
			"enqueuedAt":"2026-10-14T19:05:15Z","estimatedTimeToMerge":240,
			"pullRequest":{"number":7}
		}],"pageInfo":{"hasNextPage":false,"endCursor":""}}
	}
}}}`

const emptyQueue = `{"data":{"repository":{
	"defaultBranchRef":{"name":"main"},
	"mergeQueue":{
		"configuration":{"mergeMethod":"SQUASH"},
		"entries":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}
	}
}}}`

func TestRefreshMergeQueueRecordsFailedRemovalUntilRequeued(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	var announced []int
	syncer.SetOnMergeQueueChanged(func(_ context.Context, gotRepoID int64, number int) {
		assert.Equal(repoID, gotRepoID)
		announced = append(announced, number)
	})

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	queue, err := database.GetRepoMergeQueue(t.Context(), repoID)
	require.NoError(err)
	require.NotNil(queue)
	assert.Equal("main", queue.Branch)
	assert.Equal("squash", queue.MergeMethod)
	entry, err := database.GetMergeQueueEntry(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(entry)
	assert.Equal(1, entry.Position)
	assert.Equal("AWAITING_CHECKS", entry.State)
	require.NotNil(entry.EstimatedTimeToMerge)
	assert.Equal(4*time.Minute, *entry.EstimatedTimeToMerge)

	// The queue drops the pull request after a failed check; the pull
	// request itself does not change, so only the queue read observes it.
	gql.set(emptyQueue, `{"data":{"repository":{"pullRequest":{
		"state":"OPEN",
		"timelineItems":{"nodes":[{"createdAt":"2026-10-14T19:06:07Z","reason":"branch_protection_failure"}]}
	}}}}`)
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	entry, err = database.GetMergeQueueEntry(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(entry)
	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal)
	assert.Equal("branch_protection_failure", removal.Reason)
	assert.Equal(time.Date(2026, time.October, 14, 19, 6, 7, 0, time.UTC), removal.RemovedAt)

	// Queuing it again supersedes the failure.
	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	removal, err = database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(removal)

	assert.Equal([]int{7, 7, 7}, announced)
}

func TestRefreshMergeQueueIgnoresQueueMergeDeparture(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	gql.set(emptyQueue, `{"data":{"repository":{"pullRequest":{
		"state":"OPEN",
		"timelineItems":{"nodes":[{"createdAt":"2026-10-14T19:09:58Z","reason":"merged"}]}
	}}}}`)
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))

	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(removal, "leaving the queue because it merged is not a failure")
}

func TestRefreshMergeQueueClearsConfigurationWhenQueueRemoved(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	gql.set(emptyQueue, "")
	assert.False(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	queue, err := database.GetRepoMergeQueue(t.Context(), repoID)
	require.NoError(err)
	require.NotNil(queue)

	gql.set(`{"data":{"repository":{"defaultBranchRef":{"name":"main"},"mergeQueue":null}}}`, "")
	assert.False(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	queue, err = database.GetRepoMergeQueue(t.Context(), repoID)
	require.NoError(err)
	assert.Nil(queue, "a branch without a merge queue merges directly")
}

func TestRefreshMergeQueueStopsQueryingHostWithoutMergeQueueSchema(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	gql.set(`{"errors":[{"message":"Field 'mergeQueue' doesn't exist on type 'Repository'"}]}`, "")
	assert.False(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	assert.False(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	assert.EqualValues(1, gql.queries.Load())

	queue, err := database.GetRepoMergeQueue(t.Context(), repoID)
	require.NoError(err)
	assert.Nil(queue)
}

func TestRunOnceObservesMergeQueue(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case bytes.Contains(body, []byte("mergeQueue")):
			_, _ = io.WriteString(w, queuedPR7)
		case bytes.Contains(body, []byte("pullRequests")):
			_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequests":{"totalCount":0,"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)
		default:
			_, _ = io.WriteString(w, `{"data":{"repository":{"issues":{"totalCount":0,"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)
		}
	})
	syncer, database, _, repoID := newMergeQueueTestSyncer(t, gql)

	syncer.RunOnce(t.Context())

	entry, err := database.GetMergeQueueEntry(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(entry, "a repository pass must read the merge queue")
	assert.Equal(1, entry.Position)
}

func TestRefreshMergeQueueRetriesFailedRemovalLookup(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	// The pull request leaves the queue, but the removal lookup fails.
	gql.set(emptyQueue, `{"errors":[{"message":"Something went wrong while executing your query."}]}`)
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.Nil(removal)

	// A later pass observes the same empty queue and still resolves why it left.
	gql.set(emptyQueue, `{"data":{"repository":{"pullRequest":{
		"state":"OPEN",
		"timelineItems":{"nodes":[{"createdAt":"2026-10-14T19:06:07Z","reason":"branch_protection_failure"}]}
	}}}}`)
	assert.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	removal, err = database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal)
	assert.Equal("branch_protection_failure", removal.Reason)
}

func TestClosingPullRequestClearsMergeQueueRemoval(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	gql.set(emptyQueue, `{"data":{"repository":{"pullRequest":{
		"state":"OPEN",
		"timelineItems":{"nodes":[{"createdAt":"2026-10-14T19:06:07Z","reason":"branch_protection_failure"}]}
	}}}}`)
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	closedAt := mr.UpdatedAt.Add(time.Minute)
	mr.State = db.MergeRequestStateClosed
	mr.ClosedAt = &closedAt
	mr.UpdatedAt = closedAt
	_, err = database.UpsertMergeRequest(t.Context(), mr)
	require.NoError(err)
	reopenedAt := closedAt.Add(time.Minute)
	mr.State = db.MergeRequestStateOpen
	mr.ClosedAt = nil
	mr.UpdatedAt = reopenedAt
	_, err = database.UpsertMergeRequest(t.Context(), mr)
	require.NoError(err)

	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(removal, "a removal from before the pull request closed must not return when it reopens")
}

func TestRunOnceObservesMergeQueueWhenPullListUnchanged(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	gql.set(queuedPR7, "")
	// Entering the queue does not touch the pull request, so the list
	// validator still matches.
	client := &mockClient{listOpenPRsErr: notModifiedErr()}
	syncer, database, _, repoID := newMergeQueueTestSyncerWithClient(t, gql, client)

	syncer.RunOnce(t.Context())

	require.True(client.listOpenPRsCalled.Load())
	entry, err := database.GetMergeQueueEntry(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(entry, "an unchanged pull list must not skip the merge queue read")
	assert.Equal(1, entry.Position)
}

func TestRunOnceObservesMergeQueueWhenPullListFails(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	gql.set(queuedPR7, "")
	client := &mockClient{listOpenPRsErr: errors.New("GitHub returned 502 Bad Gateway")}
	syncer, database, _, repoID := newMergeQueueTestSyncerWithClient(t, gql, client)

	syncer.RunOnce(t.Context())

	entry, err := database.GetMergeQueueEntry(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(entry, "a failing pull list must not freeze merge queue state")
}

func TestRefreshMergeQueueForRepositoryReadsOnlyConfiguredQueues(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	assert.False(syncer.RefreshMergeQueueForRepository(t.Context(), repoID))
	assert.Zero(gql.queries.Load(), "a repository without a merge queue costs no request")

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	// The queue drops the pull request while scheduled sync is paused; an
	// explicit refresh is what observes it.
	gql.set(emptyQueue, `{"data":{"repository":{"pullRequest":{
		"state":"OPEN",
		"timelineItems":{"nodes":[{"createdAt":"2026-10-14T19:06:07Z","reason":"branch_protection_failure"}]}
	}}}}`)
	require.True(syncer.RefreshMergeQueueForRepository(t.Context(), repoID))

	entry, err := database.GetMergeQueueEntry(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(entry)
	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal)
	assert.Equal("branch_protection_failure", removal.Reason)
}

func TestRequeuedAttemptIgnoresPreviousRemovalWhileNewEventIsLate(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	firstRemoval := `{"data":{"repository":{"pullRequest":{
		"state":"OPEN",
		"timelineItems":{"nodes":[{"createdAt":"2026-10-14T19:06:07Z","reason":"branch_protection_failure"}]}
	}}}}`

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	gql.set(emptyQueue, firstRemoval)
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))

	// The user adds it again 23 seconds after that failure; no queue read
	// observes the new attempt, so Forge tracks it provisionally with a
	// skew-backdated start that would otherwise predate the old removal.
	boundary, err := database.MergeQueueAttemptBoundary(t.Context(), repoID, 7)
	require.NoError(err)
	require.NoError(database.RecordProvisionalMergeQueueEntry(
		t.Context(), repoID, 7, time.Date(2026, time.October, 14, 19, 5, 30, 0, time.UTC), boundary,
	))

	// The new attempt leaves too, but GitHub has not recorded its removal
	// yet: the latest event is still the first attempt's.
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(removal, "the previous attempt's removal must not settle the new attempt")

	// The new attempt's own removal happens after the user queued it again.
	gql.set(emptyQueue, `{"data":{"repository":{"pullRequest":{
		"state":"OPEN",
		"timelineItems":{"nodes":[{"createdAt":"`+boundary.Add(time.Minute).Format(time.RFC3339)+`","reason":"dequeued"}]}
	}}}}`)
	assert.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	removal, err = database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal)
	assert.Equal("dequeued", removal.Reason)
}

func TestRequeueWhileEarlierDepartureIsUnresolvedIgnoresItsRemoval(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	// The first attempt leaves, and its removal lookup fails, so the
	// departure is still pending when the user adds the pull request again.
	gql.set(emptyQueue, `{"errors":[{"message":"Something went wrong while executing your query."}]}`)
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	boundary, err := database.MergeQueueAttemptBoundary(t.Context(), repoID, 7)
	require.NoError(err)
	require.False(boundary.IsZero(), "a pending departure bounds the next attempt")
	require.NoError(database.RecordProvisionalMergeQueueEntry(
		t.Context(), repoID, 7, boundary.Add(-time.Minute), boundary,
	))

	// The new attempt leaves before GitHub records its removal; the only
	// removal event GitHub reports belongs to the first attempt.
	gql.set(emptyQueue, `{"data":{"repository":{"pullRequest":{
		"state":"OPEN",
		"timelineItems":{"nodes":[{"createdAt":"`+boundary.Add(-30*time.Second).Format(time.RFC3339)+`","reason":"branch_protection_failure"}]}
	}}}}`)
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(removal, "an earlier attempt's removal must not settle the new attempt")
}

func TestEnqueueBookkeepingKeepsOutcomeObservedBeforeAcknowledgement(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	// Captured before the enqueue request: no earlier attempt is recorded.
	boundary, err := database.MergeQueueAttemptBoundary(t.Context(), repoID, 7)
	require.NoError(err)

	// While GitHub is still confirming the enqueue, sync sees the attempt
	// enter the queue, leave it, and resolves why.
	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	gql.set(emptyQueue, `{"data":{"repository":{"pullRequest":{
		"state":"OPEN",
		"timelineItems":{"nodes":[{"createdAt":"2026-10-14T19:06:07Z","reason":"branch_protection_failure"}]}
	}}}}`)
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))

	// The enqueue acknowledgement arrives afterwards.
	require.NoError(database.RecordProvisionalMergeQueueEntry(
		t.Context(), repoID, 7, time.Date(2026, time.October, 14, 19, 4, 15, 0, time.UTC), boundary,
	))

	entry, err := database.GetMergeQueueEntry(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(entry, "an attempt sync already saw leave must not be re-tracked")
	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal, "the observed outcome must survive the late acknowledgement")
	assert.Equal("branch_protection_failure", removal.Reason)
}
