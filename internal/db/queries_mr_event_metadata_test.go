package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedMetadataTestMR(t *testing.T, database *DB) (int64, int64, int64) {
	t.Helper()
	ctx := t.Context()
	now := time.Date(2026, 8, 5, 10, 30, 0, 0, time.UTC)
	repoID := insertTestRepo(t, database, "owner", "repo")
	mrID, revision, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "Synthetic merge request",
		State: MergeRequestStateOpen, PlatformHeadSHA: "head", PlatformBaseSHA: "base",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(t, err)
	require.True(t, accepted)
	require.NoError(t, database.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: mrID,
		EventType:      "commit",
		Author:         "developer",
		Summary:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Body:           "Synthetic commit body",
		MetadataJSON:   `{"commit_order_key":1}`,
		CreatedAt:      now,
		DedupeKey:      "commit-1",
	}}))
	return repoID, mrID, revision
}

func assertMetadataTestEvent(t *testing.T, database *DB, mrID int64, wantMetadata string) {
	t.Helper()
	assert := assert.New(t)
	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	event := events[0]
	assert.JSONEq(wantMetadata, event.MetadataJSON)
	assert.Equal("developer", event.Author)
	assert.Equal("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", event.Summary)
	assert.Equal("Synthetic commit body", event.Body)
}

func TestChildSnapshotEventMetadataUpdates(t *testing.T) {
	tests := []struct {
		name         string
		updateKey    string
		wantMetadata string
	}{
		{
			name:         "matching event",
			updateKey:    "commit-1",
			wantMetadata: `{"commit_order_key":1,"obsolete":true}`,
		},
		{
			name:         "non-matching event",
			updateKey:    "commit-missing",
			wantMetadata: `{"commit_order_key":1}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := openTestDB(t)
			_, mrID, revision := seedMetadataTestMR(t, database)

			applied, err := database.CommitMergeRequestChildSnapshot(t.Context(), MergeRequestChildSnapshot{
				MergeRequestID:   mrID,
				ExpectedRevision: revision,
				EventMetadataUpdates: map[string]string{
					tt.updateKey: `{"commit_order_key":1,"obsolete":true}`,
				},
			})
			require.NoError(t, err)
			require.True(t, applied)
			assertMetadataTestEvent(t, database, mrID, tt.wantMetadata)
		})
	}
}

func TestChildSnapshotEventMetadataUpdatesRejectStaleRevision(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	repoID, mrID, staleRevision := seedMetadataTestMR(t, database)
	now := time.Date(2026, 8, 5, 11, 0, 0, 0, time.UTC)
	_, _, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "advanced",
		State: MergeRequestStateOpen, PlatformHeadSHA: "head2", PlatformBaseSHA: "base",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.True(accepted)

	applied, err := database.CommitMergeRequestChildSnapshot(ctx, MergeRequestChildSnapshot{
		MergeRequestID:   mrID,
		ExpectedRevision: staleRevision,
		EventMetadataUpdates: map[string]string{
			"commit-1": `{"commit_order_key":1,"obsolete":true}`,
		},
	})
	require.NoError(err)
	assert.False(applied)
	assertMetadataTestEvent(t, database, mrID, `{"commit_order_key":1}`)
}

func TestParentSnapshotComputesTerminalEventMetadataInTransaction(t *testing.T) {
	assert := assert.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	repoID, mrID, _ := seedMetadataTestMR(t, database)
	now := time.Date(2026, 8, 5, 10, 30, 0, 0, time.UTC)
	invocations := 0
	computer := func(id int64, events []MREvent) map[string]string {
		invocations++
		assert.Equal(mrID, id)
		if assert.Len(events, 1) {
			assert.Equal("commit-1", events[0].DedupeKey)
		}
		return map[string]string{
			"commit-1": `{"commit_order_key":1,"obsolete":true}`,
		}
	}
	upsert := func(state MergeRequestState, title string, updatedAt time.Time) bool {
		t.Helper()
		release, err := database.LockRepositoryReconciliationRead(ctx)
		require.NoError(t, err)
		defer release()
		_, _, accepted, err :=
			database.UpsertMergeRequestSnapshotWithLabelsUnderRepositoryReconciliationRead(
				ctx, &MergeRequest{
					RepoID: repoID, PlatformID: 1, Number: 1, Title: title,
					State: state, PlatformHeadSHA: "head", PlatformBaseSHA: "base",
					CreatedAt: now.Add(-time.Hour), UpdatedAt: updatedAt,
					LastActivityAt: updatedAt,
				}, computer,
			)
		require.NoError(t, err)
		return accepted
	}

	// A rejected snapshot (older updated_at) must invoke nothing and write
	// neither the terminal state nor any metadata.
	assert.False(upsert(MergeRequestStateClosed, "stale close", now.Add(-time.Minute)))
	assert.Equal(0, invocations)
	assertMetadataTestEvent(t, database, mrID, `{"commit_order_key":1}`)

	// An accepted open-state snapshot is not a terminal transition.
	assert.True(upsert(MergeRequestStateOpen, "still open", now.Add(30*time.Second)))
	assert.Equal(0, invocations)
	assertMetadataTestEvent(t, database, mrID, `{"commit_order_key":1}`)

	// The accepted transition runs the computation inside the snapshot
	// transaction and lands state and metadata together.
	assert.True(upsert(MergeRequestStateClosed, "real close", now.Add(time.Minute)))
	assert.Equal(1, invocations)
	assertMetadataTestEvent(t, database, mrID, `{"commit_order_key":1,"obsolete":true}`)
	fresh, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(t, err)
	require.NotNil(t, fresh)
	assert.Equal(MergeRequestStateClosed, fresh.State)

	// A later snapshot of the already-terminal MR is no transition: the
	// terminal record is never recomputed.
	assert.True(upsert(MergeRequestStateClosed, "closed again", now.Add(2*time.Minute)))
	assert.Equal(1, invocations)
}

func TestMarkDetailFetchedEventMetadataUpdates(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	_, mrID, revision := seedMetadataTestMR(t, database)

	// A stale-revision marker must reject the metadata updates with it.
	applied, err := database.MarkMergeRequestDetailFetchedSnapshot(
		ctx, mrID, revision+1, false, map[string]string{
			"commit-1": `{"commit_order_key":1,"obsolete":true}`,
		},
	)
	require.NoError(err)
	assert.False(applied)
	assertMetadataTestEvent(t, database, mrID, `{"commit_order_key":1}`)

	applied, err = database.MarkMergeRequestDetailFetchedSnapshot(
		ctx, mrID, revision, false, map[string]string{
			"commit-1": `{"commit_order_key":1,"obsolete":true}`,
		},
	)
	require.NoError(err)
	assert.True(applied)
	assertMetadataTestEvent(t, database, mrID, `{"commit_order_key":1,"obsolete":true}`)
}
