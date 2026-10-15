package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertMergeQueueStateReportsShownChanges(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	repoID, err := seedTestRepo(ctx, database, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	insertTestMRWithOptions(t, database, testMR(repoID, 7))
	base := time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)
	eta := 4 * time.Minute
	queued := MergeQueueState{
		Number: 7, BaseBranch: "main", Enabled: true, MergeMethod: "squash",
		Queued: true, Position: 2, State: "AWAITING_CHECKS", EnqueuedAt: &base,
		EstimatedTimeToMerge: &eta, ObservedAt: base,
	}

	changed, err := database.UpsertMergeQueueState(ctx, repoID, queued)
	require.NoError(err)
	assert.True(changed, "a first observation is a change")

	// Only the estimate moved, and the merge method was not read this time.
	next := queued
	next.EstimatedTimeToMerge = new(2 * time.Minute)
	next.MergeMethod = ""
	next.ObservedAt = base.Add(time.Minute)
	changed, err = database.UpsertMergeQueueState(ctx, repoID, next)
	require.NoError(err)
	assert.False(changed, "an estimate-only update does not refresh views")
	stored, err := database.GetMergeQueueState(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("squash", stored.MergeMethod, "an unread merge method keeps the stored one")
	require.NotNil(stored.EstimatedTimeToMerge)
	assert.Equal(2*time.Minute, *stored.EstimatedTimeToMerge)

	removedAt := base.Add(2 * time.Minute)
	changed, err = database.UpsertMergeQueueState(ctx, repoID, MergeQueueState{
		Number: 7, BaseBranch: "main", Enabled: true,
		RemovedAt: &removedAt, RemovalReason: "branch_protection_failure",
		ObservedAt: base.Add(3 * time.Minute),
	})
	require.NoError(err)
	assert.True(changed, "leaving the queue is a change")
	stored, err = database.GetMergeQueueState(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(stored)
	assert.False(stored.Queued)
	assert.Equal("branch_protection_failure", stored.RemovalReason)
}

func TestListQueuedMergeRequestNumbersListsOpenQueuedPullRequests(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	repoID, err := seedTestRepo(ctx, database, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)
	for _, number := range []int{7, 8} {
		insertTestMRWithOptions(t, database, testMR(repoID, number))
	}
	_, err = database.UpsertMergeQueueState(ctx, repoID, MergeQueueState{
		Number: 7, BaseBranch: "main", Enabled: true, Queued: true, State: "QUEUED", ObservedAt: now,
	})
	require.NoError(err)
	_, err = database.UpsertMergeQueueState(ctx, repoID, MergeQueueState{
		Number: 8, BaseBranch: "main", Enabled: true, ObservedAt: now,
	})
	require.NoError(err)

	numbers, err := database.ListQueuedMergeRequestNumbers(ctx, repoID)
	require.NoError(err)
	require.Equal([]int{7}, numbers)
}

func TestClosingPullRequestEvictsItsMergeQueueState(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	repoID, err := seedTestRepo(ctx, database, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	base := time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)
	insertTestMRWithOptions(t, database, testMR(repoID, 7, withMRActivity(base)))
	removedAt := base
	_, err = database.UpsertMergeQueueState(ctx, repoID, MergeQueueState{
		Number: 7, BaseBranch: "main", Enabled: true,
		RemovedAt: &removedAt, RemovalReason: "branch_protection_failure", ObservedAt: base,
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

	state, err := database.GetMergeQueueState(ctx, repoID, 7)
	require.NoError(err)
	require.Nil(state, "reopening must not revive queue state from before the close")
}

func TestInsertingClosedPullRequestEvictsMergeQueueState(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	repoID, err := seedTestRepo(ctx, database, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	base := time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)
	// Queue state can be stored before Forge has a row for the pull request.
	_, err = database.UpsertMergeQueueState(ctx, repoID, MergeQueueState{
		Number: 8, BaseBranch: "main", Enabled: true, Queued: true, State: "QUEUED", ObservedAt: base,
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

	state, err := database.GetMergeQueueState(ctx, repoID, 8)
	require.NoError(err)
	require.Nil(state, "a pull request first stored as closed must not reopen into stale queue state")
}
