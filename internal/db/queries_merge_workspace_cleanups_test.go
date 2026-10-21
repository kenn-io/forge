package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeWorkspaceCleanupOutcomeFollowsSyncedPullRequest(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	requestedAt := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	queueRead := func(queued bool, observedAt time.Time) MergeQueueState {
		return MergeQueueState{
			Number: 7, BaseBranch: "main", Enabled: true, Queued: queued, ObservedAt: observedAt,
		}
	}

	tests := []struct {
		name  string
		state MergeRequestState
		queue *MergeQueueState
		want  MergeWorkspaceCleanupOutcome
	}{
		{
			name: "open and queued", state: MergeRequestStateOpen,
			queue: new(queueRead(true, requestedAt.Add(time.Minute))), want: MergeWorkspaceCleanupWaiting,
		},
		{
			name: "open with queue never read", state: MergeRequestStateOpen,
			want: MergeWorkspaceCleanupWaiting,
		},
		{
			name: "queue read before the request", state: MergeRequestStateOpen,
			queue: new(queueRead(false, requestedAt.Add(-time.Minute))), want: MergeWorkspaceCleanupWaiting,
		},
		{
			name: "queue read after the request finds it out of the queue", state: MergeRequestStateOpen,
			queue: new(queueRead(false, requestedAt.Add(time.Minute))), want: MergeWorkspaceCleanupAbandoned,
		},
		{name: "merged", state: MergeRequestStateMerged, want: MergeWorkspaceCleanupMerged},
		{name: "closed", state: MergeRequestStateClosed, want: MergeWorkspaceCleanupAbandoned},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			database := openTestDB(t)
			repoID, err := seedTestRepo(ctx, database, GitHubRepoIdentity("github.com", "acme", "widget"))
			require.NoError(t, err)
			insertTestMRWithOptions(t, database, testMR(repoID, 7))
			if tt.queue != nil {
				_, err := database.UpsertMergeQueueState(ctx, repoID, *tt.queue)
				require.NoError(t, err)
			}
			cleanup := MergeWorkspaceCleanup{
				RepoID: repoID, Number: 7, WorkspaceID: "ws-1", HostKey: "spoke", RequestedAt: requestedAt,
			}
			require.NoError(t, database.SaveMergeWorkspaceCleanup(ctx, cleanup))
			if tt.state != MergeRequestStateOpen {
				terminal := testMR(repoID, 7)
				terminal.State = tt.state
				terminal.UpdatedAt = terminal.UpdatedAt.Add(time.Hour)
				insertTestMRWithOptions(t, database, terminal)
			}

			listed, err := database.ListMergeWorkspaceCleanups(ctx)
			require.NoError(t, err)
			assert.Equal(t, []ObservedMergeWorkspaceCleanup{{MergeWorkspaceCleanup: cleanup, Outcome: tt.want}}, listed)
		})
	}
}

func TestDeleteMergeWorkspaceCleanupKeepsNewerRequest(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	repoID, err := seedTestRepo(ctx, database, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	insertTestMRWithOptions(t, database, testMR(repoID, 7))
	older := MergeWorkspaceCleanup{
		RepoID: repoID, Number: 7, WorkspaceID: "ws-1",
		RequestedAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
	}
	require.NoError(database.SaveMergeWorkspaceCleanup(ctx, older))
	newer := older
	newer.RequestedAt = older.RequestedAt.Add(time.Minute)
	require.NoError(database.SaveMergeWorkspaceCleanup(ctx, newer))

	// Resolving the request read earlier must not forget the newer one.
	require.NoError(database.DeleteMergeWorkspaceCleanup(ctx, older))
	kept, err := database.ListMergeWorkspaceCleanups(ctx)
	require.NoError(err)
	require.Len(kept, 1)
	assert.Equal(t, newer, kept[0].MergeWorkspaceCleanup)

	require.NoError(database.DeleteMergeWorkspaceCleanup(ctx, newer))
	kept, err = database.ListMergeWorkspaceCleanups(ctx)
	require.NoError(err)
	assert.Empty(t, kept)
}
