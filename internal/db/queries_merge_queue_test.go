package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaleMergeQueueLookupLeavesLaterDepartureAlone(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	repoID, err := seedTestRepo(ctx, database, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	base := time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)
	insertTestMRWithOptions(t, database, testMR(repoID, 7))

	queued := func(attempt int, enqueuedAt, observedAt time.Time) MergeQueueSnapshot {
		return MergeQueueSnapshot{
			Configured: true, Branch: "main", MergeMethod: "squash",
			Entries: []MergeQueueEntry{{
				Number: 7, Attempt: attempt, Position: 1, State: "QUEUED", EnqueuedAt: enqueuedAt,
			}},
			ObservedAt: observedAt,
		}
	}
	empty := func(observedAt time.Time) MergeQueueSnapshot {
		return MergeQueueSnapshot{Configured: true, Branch: "main", MergeMethod: "squash", ObservedAt: observedAt}
	}

	// First attempt leaves the queue; a lookup for it starts but is delayed.
	_, err = database.ReplaceMergeQueue(ctx, repoID, queued(1, base, base))
	require.NoError(err)
	_, err = database.ReplaceMergeQueue(ctx, repoID, empty(base.Add(time.Minute)))
	require.NoError(err)
	first, err := database.ListPendingMergeQueueDepartures(ctx, repoID)
	require.NoError(err)
	require.Len(first, 1)

	// Meanwhile the pull request is queued again and leaves again.
	_, err = database.ReplaceMergeQueue(ctx, repoID, queued(2, base.Add(2*time.Minute), base.Add(2*time.Minute)))
	require.NoError(err)
	_, err = database.ReplaceMergeQueue(ctx, repoID, empty(base.Add(3*time.Minute)))
	require.NoError(err)

	// The delayed lookup for the first attempt must not settle the second.
	require.NoError(database.ResolveMergeQueueDeparture(ctx, repoID, first[0], MergeQueueRemoval{
		Number: 7, RemovedAt: base.Add(30 * time.Second), Reason: "branch_protection_failure",
	}))
	require.NoError(database.DropMergeQueueDeparture(ctx, repoID, first[0]))
	removal, err := database.GetMergeQueueRemoval(ctx, repoID, 7)
	require.NoError(err)
	assert.Nil(removal)
	pending, err := database.ListPendingMergeQueueDepartures(ctx, repoID)
	require.NoError(err)
	require.Len(pending, 1)
	assert.Equal(2, pending[0].Attempt)

	require.NoError(database.ResolveMergeQueueDeparture(ctx, repoID, pending[0], MergeQueueRemoval{
		Number: 7, RemovedAt: base.Add(150 * time.Second), Reason: "dequeued",
	}))
	removal, err = database.GetMergeQueueRemoval(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(removal)
	assert.Equal("dequeued", removal.Reason)
}

func TestClosingQueuedPullRequestEvictsItsQueueEntry(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	repoID, err := seedTestRepo(ctx, database, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	base := time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)
	insertTestMRWithOptions(t, database, testMR(repoID, 7, withMRActivity(base)))
	_, err = database.ReplaceMergeQueue(ctx, repoID, MergeQueueSnapshot{
		Configured: true, Branch: "main", MergeMethod: "squash",
		Entries:    []MergeQueueEntry{{Number: 7, Position: 1, State: "QUEUED", EnqueuedAt: base}},
		ObservedAt: base,
	})
	require.NoError(err)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	closedAt := base.Add(time.Minute)
	mr.State = MergeRequestStateClosed
	mr.ClosedAt = &closedAt
	mr.UpdatedAt = closedAt
	_, err = database.UpsertMergeRequest(ctx, mr)
	require.NoError(err)
	mr.State = MergeRequestStateOpen
	mr.ClosedAt = nil
	mr.UpdatedAt = closedAt.Add(time.Minute)
	_, err = database.UpsertMergeRequest(ctx, mr)
	require.NoError(err)

	// Reopened before any queue read: the earlier attempt is gone, so a
	// later empty snapshot records no departure for it.
	entry, err := database.GetMergeQueueEntry(ctx, repoID, 7)
	require.NoError(err)
	require.Nil(entry)
	changes, err := database.ReplaceMergeQueue(ctx, repoID, MergeQueueSnapshot{
		Configured: true, Branch: "main", MergeMethod: "squash", ObservedAt: base.Add(3 * time.Minute),
	})
	require.NoError(err)
	require.Empty(changes.Departed)
}

func TestInsertingClosedPullRequestEvictsCachedQueueEntry(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	repoID, err := seedTestRepo(ctx, database, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	base := time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)
	// The queue read stores the entry before Forge has a row for the pull
	// request.
	_, err = database.ReplaceMergeQueue(ctx, repoID, MergeQueueSnapshot{
		Configured: true, Branch: "main", MergeMethod: "squash",
		Entries:    []MergeQueueEntry{{Number: 8, Position: 1, State: "QUEUED", EnqueuedAt: base}},
		ObservedAt: base,
	})
	require.NoError(err)

	closedAt := base.Add(time.Minute)
	mr := testMR(repoID, 8, withMRActivity(closedAt))
	mr.State = MergeRequestStateClosed
	mr.ClosedAt = &closedAt
	insertTestMRWithOptions(t, database, mr)
	mr.State = MergeRequestStateOpen
	mr.ClosedAt = nil
	mr.UpdatedAt = closedAt.Add(time.Minute)
	_, err = database.UpsertMergeRequest(ctx, mr)
	require.NoError(err)

	entry, err := database.GetMergeQueueEntry(ctx, repoID, 8)
	require.NoError(err)
	require.Nil(entry, "a pull request first stored as closed must not reopen into stale queue membership")
}
