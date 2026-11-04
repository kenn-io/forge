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
		name         string
		state        MergeRequestState
		autoMerge    bool
		viaAutoMerge bool
		queue        *MergeQueueState
		want         MergeWorkspaceCleanupOutcome
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
		{
			name: "auto-merge not yet in the queue", state: MergeRequestStateOpen, autoMerge: true,
			queue: new(queueRead(false, requestedAt.Add(time.Minute))), want: MergeWorkspaceCleanupWaiting,
		},
		{
			name: "auto-merge request with a newer queue read", state: MergeRequestStateOpen, viaAutoMerge: true,
			queue: new(queueRead(false, requestedAt.Add(time.Minute))), want: MergeWorkspaceCleanupWaiting,
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
			open := testMR(repoID, 7)
			open.AutoMergeEnabled = tt.autoMerge
			insertTestMRWithOptions(t, database, open)
			if tt.queue != nil {
				_, err := database.UpsertMergeQueueState(ctx, repoID, *tt.queue)
				require.NoError(t, err)
			}
			cleanup := MergeWorkspaceCleanup{
				RepoID: repoID, Number: 7, WorkspaceID: "ws-1", HostKey: "spoke", RequestedAt: requestedAt,
				ViaAutoMerge: tt.viaAutoMerge,
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

func TestCancelledAutoMergeSnapshotDiscardsItsWorkspaceCleanup(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	requestedAt := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		viaAutoMerge bool
		autoMerge    bool
		snapshotAt   time.Time
		wantKept     bool
	}{
		{name: "auto-merge turned off after the request", viaAutoMerge: true, snapshotAt: requestedAt.Add(time.Minute)},
		{
			name: "snapshot older than the request", viaAutoMerge: true,
			snapshotAt: requestedAt.Add(-time.Minute), wantKept: true,
		},
		{
			name: "auto-merge still on", viaAutoMerge: true, autoMerge: true,
			snapshotAt: requestedAt.Add(time.Minute), wantKept: true,
		},
		{name: "merge queue enqueue", snapshotAt: requestedAt.Add(time.Minute), wantKept: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			database := openTestDB(t)
			repoID, err := seedTestRepo(ctx, database, GitHubRepoIdentity("github.com", "acme", "widget"))
			require.NoError(t, err)
			open := testMR(repoID, 7)
			open.UpdatedAt = requestedAt.Add(-time.Hour)
			open.AutoMergeEnabled = true
			insertTestMRWithOptions(t, database, open)
			require.NoError(t, database.SaveMergeWorkspaceCleanup(ctx, MergeWorkspaceCleanup{
				RepoID: repoID, Number: 7, WorkspaceID: "ws-1", RequestedAt: requestedAt, ViaAutoMerge: tt.viaAutoMerge,
			}))

			snapshot := testMR(repoID, 7)
			snapshot.UpdatedAt = tt.snapshotAt
			snapshot.AutoMergeEnabled = tt.autoMerge
			insertTestMRWithOptions(t, database, snapshot)

			listed, err := database.ListMergeWorkspaceCleanups(ctx)
			require.NoError(t, err)
			if tt.wantKept {
				assert.Len(t, listed, 1)
			} else {
				assert.Empty(t, listed)
			}
		})
	}
}

func TestManualMergeAfterAutoMergeWasCancelledKeepsTheWorkspace(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	requestedAt := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	database := openTestDB(t)
	repoID, err := seedTestRepo(ctx, database, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	open := testMR(repoID, 7)
	open.UpdatedAt = requestedAt.Add(-time.Hour)
	open.AutoMergeEnabled = true
	insertTestMRWithOptions(t, database, open)
	require.NoError(t, database.SaveMergeWorkspaceCleanup(ctx, MergeWorkspaceCleanup{
		RepoID: repoID, Number: 7, WorkspaceID: "ws-1", RequestedAt: requestedAt, ViaAutoMerge: true,
	}))

	// Sync records auto-merge turned off, then a manual merge, before any
	// cleanup pass runs.
	cancelled := testMR(repoID, 7)
	cancelled.UpdatedAt = requestedAt.Add(time.Minute)
	insertTestMRWithOptions(t, database, cancelled)
	merged := testMR(repoID, 7)
	merged.State = MergeRequestStateMerged
	merged.UpdatedAt = requestedAt.Add(2 * time.Minute)
	insertTestMRWithOptions(t, database, merged)

	listed, err := database.ListMergeWorkspaceCleanups(ctx)
	require.NoError(t, err)
	assert.Empty(t, listed, "the cancelled auto-merge's deletion must not run on a later manual merge")
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
