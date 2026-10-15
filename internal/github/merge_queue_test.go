package github

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/activityrelay"
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
			"pullRequest":{"number":7,"enqueues":{"totalCount":1}}
		}],"pageInfo":{"hasNextPage":false,"endCursor":""}}
	}
}}}`

// queueOutcome is a merge queue outcome response: the pull request's state,
// its enqueue count, and its latest queue event. An empty reason makes the
// latest event an enqueue, meaning the latest attempt has not ended.
func queueOutcome(state string, attempts int, removedAt, reason string) string {
	latest := `{"__typename":"AddedToMergeQueueEvent"}`
	if reason != "" {
		latest = `{"__typename":"RemovedFromMergeQueueEvent","createdAt":"` + removedAt + `","reason":"` + reason + `"}`
	}
	return `{"data":{"repository":{"pullRequest":{"state":"` + state + `",` +
		`"enqueues":{"totalCount":` + strconv.Itoa(attempts) + `},` +
		`"latestQueueEvent":{"nodes":[` + latest + `]}}}}}`
}

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
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
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
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:09:58Z", "merged"))
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
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
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
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
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
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
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

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))

	// Forge adds it again as attempt 2, which leaves the queue before any
	// read observes it.
	require.NoError(database.RecordProvisionalMergeQueueEntry(t.Context(), repoID, 7, 2, time.Now().UTC()))
	// GitHub's timeline still ends with the first attempt's removal.
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(removal, "the previous attempt's removal must not settle the new attempt")

	// The new attempt is recorded but has not ended yet.
	gql.set(emptyQueue, queueOutcome("OPEN", 2, "", ""))
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	removal, err = database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(removal)

	gql.set(emptyQueue, queueOutcome("OPEN", 2, "2026-10-14T19:06:30Z", "dequeued"))
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
	// The first attempt leaves, and its outcome lookup fails, so the
	// departure is still pending when Forge adds the pull request again.
	gql.set(emptyQueue, `{"errors":[{"message":"Something went wrong while executing your query."}]}`)
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	require.NoError(database.RecordProvisionalMergeQueueEntry(t.Context(), repoID, 7, 2, time.Now().UTC()))

	// The new attempt leaves before GitHub records it; the timeline still
	// ends with the first attempt's removal.
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
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

	// While GitHub is still confirming the enqueue of attempt 1, sync sees
	// it enter the queue, leave it, and resolves why.
	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))

	// The enqueue acknowledgement for attempt 1 arrives afterwards.
	require.NoError(database.RecordProvisionalMergeQueueEntry(t.Context(), repoID, 7, 1, time.Now().UTC()))

	entry, err := database.GetMergeQueueEntry(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(entry, "an attempt sync already saw leave must not be re-tracked")
	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal, "the observed outcome must survive the late acknowledgement")
	assert.Equal("branch_protection_failure", removal.Reason)
}

func TestDepartureResolvesWhateverForgesClockSays(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	// GitHub's removal time is far behind Forge's clock and before the
	// entry's enqueue time; only GitHub's event order decides the attempt.
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2001-01-01T00:00:00Z", "branch_protection_failure"))
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))

	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal)
	assert.Equal("branch_protection_failure", removal.Reason)
}

func TestRequeueOverStaleCachedEntryWaitsForNewAttempt(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	// Attempt 1 is still cached as queued when Forge enqueues attempt 2.
	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	require.NoError(database.RecordProvisionalMergeQueueEntry(t.Context(), repoID, 7, 2, time.Now().UTC()))

	// Both attempts are gone by the next read, and GitHub's timeline has
	// not caught up past attempt 1's removal.
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(removal, "attempt 1's removal must not settle attempt 2")

	gql.set(emptyQueue, queueOutcome("OPEN", 2, "2026-10-14T19:07:00Z", "dequeued"))
	assert.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	removal, err = database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal)
	assert.Equal("dequeued", removal.Reason)
}

func TestManualRefreshRetriesPendingDepartureAfterQueueRemoved(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	// The repository drops its merge queue, and the removal lookup fails.
	noQueue := `{"data":{"repository":{"defaultBranchRef":{"name":"main"},"mergeQueue":null}}}`
	gql.set(noQueue, `{"errors":[{"message":"Something went wrong while executing your query."}]}`)
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	queue, err := database.GetRepoMergeQueue(t.Context(), repoID)
	require.NoError(err)
	require.Nil(queue)

	gql.set(noQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "dequeued"))
	assert.True(syncer.RefreshMergeQueueForRepository(t.Context(), repoID))
	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal)
	assert.Equal("dequeued", removal.Reason)
}

func TestManualRefreshSkipsArchivedRepository(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	gql.set(queuedPR7, "")
	syncer, _, repo, repoID := newMergeQueueTestSyncer(t, gql)
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	queries := gql.queries.Load()

	archived := repo
	archived.Archived = true
	syncer.SetRepos([]RepoRef{archived})

	assert.False(syncer.RefreshMergeQueueForRepository(t.Context(), repoID))
	assert.Equal(queries, gql.queries.Load(), "an archived repository gets no live queue read")
}

func TestUnknownAttemptDepartureIsNeverMatchedToARemoval(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	// The attempt count could not be read before Forge enqueued, and the
	// attempt left before any read observed it.
	require.NoError(database.RecordProvisionalMergeQueueEntry(t.Context(), repoID, 7, 0, time.Now().UTC()))
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)

	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(removal, "a removal cannot be attributed to an attempt Forge could not identify")
}

func TestAlreadyQueuedAcknowledgementKeepsAttempt(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	// Merging again while queued starts no new attempt.
	require.NoError(database.RecordProvisionalMergeQueueEntry(t.Context(), repoID, 7, 1, time.Now().UTC()))
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))

	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal)
	assert.Equal("branch_protection_failure", removal.Reason)
}

func TestUnidentifiedAttemptOverStaleCachedEntryDoesNotInheritItsOutcome(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	// Attempt 1 is still cached as queued; the count read before the new
	// enqueue failed, so the new attempt is unidentified.
	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	require.NoError(database.RecordProvisionalMergeQueueEntry(t.Context(), repoID, 7, 0, time.Now().UTC()))

	// Both attempts are gone and the timeline still ends with attempt 1's
	// removal.
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(removal, "an unidentified attempt must not take the cached attempt's outcome")
}

func TestUnidentifiedAttemptReplacesEarlierDeparture(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))

	// A new attempt whose number could not be read replaces the recorded
	// failure, and the timeline has not caught up with it.
	require.NoError(database.RecordProvisionalMergeQueueEntry(t.Context(), repoID, 7, 0, time.Now().UTC()))
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)

	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(removal, "the earlier failure must not be shown as the new attempt's outcome")
}

func TestQueueReadReidentifiesUnidentifiedAttempt(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	require.NoError(database.RecordProvisionalMergeQueueEntry(t.Context(), repoID, 7, 0, time.Now().UTC()))

	// The next read shows the pull request queued as attempt 2.
	gql.set(strings.Replace(queuedPR7, `"enqueues":{"totalCount":1}`, `"enqueues":{"totalCount":2}`, 1), "")
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	gql.set(emptyQueue, queueOutcome("OPEN", 2, "2026-10-14T19:07:00Z", "dequeued"))
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))

	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal)
	assert.Equal("dequeued", removal.Reason)
}

func TestRecordEnqueueKeepsAttemptIdentifiedByFollowingRead(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	// The count read before the enqueue failed, but the read right after
	// it shows the pull request queued as attempt 2.
	gql.set(strings.Replace(queuedPR7, `"enqueues":{"totalCount":1}`, `"enqueues":{"totalCount":2}`, 1), "")
	syncer.RecordEnqueue(t.Context(), repo, repoID, 7, 0, true, time.Now().UTC())

	// It leaves before another read observes it.
	gql.set(emptyQueue, queueOutcome("OPEN", 2, "2026-10-14T19:07:00Z", "branch_protection_failure"))
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))

	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal, "the attempt the read identified must keep its outcome")
	assert.Equal("branch_protection_failure", removal.Reason)
}

// countingHandler counts requests to an inner handler.
type countingHandler struct {
	inner    http.Handler
	requests atomic.Int32
}

func (h *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.requests.Add(1)
	h.inner.ServeHTTP(w, r)
}

// refusedMergeQueue answers like GitHub does for a GitHub App without the
// Merge queues permission.
var refusedMergeQueue = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"data":{"repository":{"defaultBranchRef":{"name":"main"},"mergeQueue":null}},`+
		`"errors":[{"type":"FORBIDDEN","path":["repository","mergeQueue"],"message":"Resource not accessible by integration"}]}`)
})

// newSplitCredentialMergeQueueSyncer routes reads through app and writes
// through user, like a GitHub App sync credential with a user merge token.
func newSplitCredentialMergeQueueSyncer(
	t *testing.T, app, user http.Handler,
) (*Syncer, *db.DB, RepoRef, int64) {
	t.Helper()
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, app)
	appSrv := httptest.NewServer(app)
	t.Cleanup(appSrv.Close)
	userSrv := httptest.NewServer(user)
	t.Cleanup(userSrv.Close)
	router, err := NewHostRouter("github.com", &Route{
		Key:    RouteKey{Host: "github.com", Owner: "acme"},
		Client: &mockClient{},
		Fetcher: NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(appSrv.URL, appSrv.Client()), nil,
		),
		WriteFetcher: NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(userSrv.URL, userSrv.Client()), nil,
		),
		ReadIdentity:  IdentityKey{Host: "github.com", Principal: "app:1"},
		WriteIdentity: IdentityKey{Host: "github.com", Principal: "user:7"},
	})
	require.NoError(t, err)
	syncer.SetGitHubRouters(map[string]*HostRouter{"github.com": router})
	return syncer, database, repo, repoID
}

func TestMergeQueueReadsFallBackToMergeCredentialWhenAppIsRefused(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	user := &mergeQueueGraphQL{}
	user.set(queuedPR7, "")
	syncer, database, repo, repoID := newSplitCredentialMergeQueueSyncer(t, refusedMergeQueue, user)

	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	queue, err := database.GetRepoMergeQueue(t.Context(), repoID)
	require.NoError(err)
	require.NotNil(queue, "the merge credential must observe the queue the App cannot read")
	entry, err := database.GetMergeQueueEntry(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(entry)
	assert.Equal(1, entry.Position)
}

func TestMergeQueueReadsStayOnAppWhenItHasPermission(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	app := &mergeQueueGraphQL{}
	app.set(queuedPR7, "")
	user := &countingHandler{inner: refusedMergeQueue}
	syncer, _, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)

	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	require.Zero(user.requests.Load(), "a permitted App must not spend the merge credential's quota")
}

func TestRefusedAppIsRetriedAfterRecheckWindow(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	app := &countingHandler{inner: refusedMergeQueue}
	user := &mergeQueueGraphQL{}
	user.set(queuedPR7, "")
	syncer, _, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)
	now := time.Date(2026, time.October, 15, 3, 0, 0, 0, time.UTC)
	syncer.now = func() time.Time { return now }

	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	assert.EqualValues(1, app.requests.Load(), "a refused App is not asked again inside the recheck window")

	now = now.Add(mergeQueueReadRecheck + time.Minute)
	syncer.RefreshMergeQueue(t.Context(), repo, repoID)
	assert.EqualValues(2, app.requests.Load(), "the App is tried again once the window passes")
}

// mergeQueueQueryCounter answers merge queue reads with response and counts
// them; other GraphQL reads get empty lists.
type mergeQueueQueryCounter struct {
	response string
	queries  atomic.Int32
}

func (c *mergeQueueQueryCounter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case bytes.Contains(body, []byte("mergeQueue")):
		c.queries.Add(1)
		_, _ = io.WriteString(w, c.response)
	case bytes.Contains(body, []byte("pullRequests")):
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequests":{"totalCount":0,"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)
	default:
		_, _ = io.WriteString(w, `{"data":{"repository":{"issues":{"totalCount":0,"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)
	}
}

func TestRepositoryPassesReadAbsentMergeQueueRarely(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	gql := &mergeQueueQueryCounter{
		response: `{"data":{"repository":{"defaultBranchRef":{"name":"main"},"mergeQueue":null}}}`,
	}
	client := &mockClient{listOpenPRsErr: notModifiedErr()}
	syncer, _, _, _ := newMergeQueueTestSyncerWithClient(t, gql, client)
	now := time.Now().UTC()
	syncer.now = func() time.Time { return now }

	syncer.RunOnce(t.Context())
	syncer.RunOnce(t.Context())
	assert.EqualValues(1, gql.queries.Load(), "a repository without a merge queue is not re-read every pass")

	now = now.Add(mergeQueueAbsentCheckInterval + time.Minute)
	syncer.RunOnce(t.Context())
	assert.EqualValues(2, gql.queries.Load(), "it is checked again once the interval passes")
}

func TestRepositoryPassesReadKnownMergeQueueEveryPass(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	gql := &mergeQueueQueryCounter{response: queuedPR7}
	client := &mockClient{listOpenPRsErr: notModifiedErr()}
	syncer, _, _, _ := newMergeQueueTestSyncerWithClient(t, gql, client)

	syncer.RunOnce(t.Context())
	syncer.RunOnce(t.Context())
	assert.EqualValues(2, gql.queries.Load(), "a repository with a merge queue is read every pass")
}

func TestRelayPullRequestHintRefreshesKnownMergeQueue(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := &mergeQueueGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	gql.set(queuedPR7, "")
	require.True(syncer.RefreshMergeQueue(t.Context(), repo, repoID))

	// GitHub removes the pull request and sends a pull_request webhook; the
	// relay forwards it as a hint before any repository pass runs.
	gql.set(emptyQueue, queueOutcome("OPEN", 1, "2026-10-14T19:06:07Z", "branch_protection_failure"))
	_ = syncer.refreshRelayHint(WithSyncBudget(t.Context()), activityrelay.Hint{
		Provider: "github", Host: "github.com", RepositoryID: testRepoID("acme", "widgets"),
		Target: activityrelay.PullRequest, Number: 7,
	})

	entry, err := database.GetMergeQueueEntry(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(entry)
	removal, err := database.GetMergeQueueRemoval(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(removal, "the relay hint must surface the removal without waiting for a pass")
	assert.Equal("branch_protection_failure", removal.Reason)
}

func TestRelayPullRequestHintSkipsRepositoryWithoutMergeQueue(t *testing.T) {
	t.Parallel()

	gql := &mergeQueueQueryCounter{response: queuedPR7}
	syncer, _, _, _ := newMergeQueueTestSyncer(t, gql)

	_ = syncer.refreshRelayHint(WithSyncBudget(t.Context()), activityrelay.Hint{
		Provider: "github", Host: "github.com", RepositoryID: testRepoID("acme", "widgets"),
		Target: activityrelay.PullRequest, Number: 7,
	})
	assert.Zero(t, gql.queries.Load(), "a hint for a repository without a known queue reads no queue")
}
