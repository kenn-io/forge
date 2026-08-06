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
	database := openTestDB(t)
	ctx := t.Context()
	repoID, mrID, staleRevision := seedMetadataTestMR(t, database)
	now := time.Date(2026, 8, 5, 11, 0, 0, 0, time.UTC)
	_, _, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "advanced",
		State: MergeRequestStateOpen, PlatformHeadSHA: "head2", PlatformBaseSHA: "base",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(t, err)
	require.True(t, accepted)

	applied, err := database.CommitMergeRequestChildSnapshot(ctx, MergeRequestChildSnapshot{
		MergeRequestID:   mrID,
		ExpectedRevision: staleRevision,
		EventMetadataUpdates: map[string]string{
			"commit-1": `{"commit_order_key":1,"obsolete":true}`,
		},
	})
	require.NoError(t, err)
	assert.False(t, applied)
	assertMetadataTestEvent(t, database, mrID, `{"commit_order_key":1}`)
}

func TestParentSnapshotCarriesEventMetadataUpdatesAtomically(t *testing.T) {
	database := openTestDB(t)
	ctx := t.Context()
	repoID, mrID, _ := seedMetadataTestMR(t, database)
	now := time.Date(2026, 8, 5, 10, 30, 0, 0, time.UTC)
	updates := map[string]string{
		"commit-1": `{"commit_order_key":1,"obsolete":true}`,
	}
	upsert := func(mr *MergeRequest) bool {
		t.Helper()
		release, err := database.LockRepositoryReconciliationRead(ctx)
		require.NoError(t, err)
		defer release()
		_, _, accepted, err :=
			database.UpsertMergeRequestSnapshotWithLabelsUnderRepositoryReconciliationRead(
				ctx, mr, updates,
			)
		require.NoError(t, err)
		return accepted
	}

	// A rejected snapshot (older updated_at) must write neither the terminal
	// state nor the metadata riding with it.
	rejected := upsert(&MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "stale close",
		State: MergeRequestStateClosed, PlatformHeadSHA: "head", PlatformBaseSHA: "base",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute),
		LastActivityAt: now.Add(-time.Minute),
	})
	assert.False(t, rejected)
	assertMetadataTestEvent(t, database, mrID, `{"commit_order_key":1}`)

	// The accepted transition lands state and metadata together.
	accepted := upsert(&MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "real close",
		State: MergeRequestStateClosed, PlatformHeadSHA: "head", PlatformBaseSHA: "base",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(time.Minute),
		LastActivityAt: now.Add(time.Minute),
	})
	assert.True(t, accepted)
	assertMetadataTestEvent(t, database, mrID, `{"commit_order_key":1,"obsolete":true}`)
	fresh, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(t, err)
	require.NotNil(t, fresh)
	assert.Equal(t, MergeRequestStateClosed, fresh.State)
}

func TestMarkDetailFetchedEventMetadataUpdates(t *testing.T) {
	database := openTestDB(t)
	ctx := t.Context()
	_, mrID, revision := seedMetadataTestMR(t, database)

	// A stale-revision marker must reject the metadata updates with it.
	applied, err := database.MarkMergeRequestDetailFetchedSnapshot(
		ctx, mrID, revision+1, false, map[string]string{
			"commit-1": `{"commit_order_key":1,"obsolete":true}`,
		},
	)
	require.NoError(t, err)
	assert.False(t, applied)
	assertMetadataTestEvent(t, database, mrID, `{"commit_order_key":1}`)

	applied, err = database.MarkMergeRequestDetailFetchedSnapshot(
		ctx, mrID, revision, false, map[string]string{
			"commit-1": `{"commit_order_key":1,"obsolete":true}`,
		},
	)
	require.NoError(t, err)
	assert.True(t, applied)
	assertMetadataTestEvent(t, database, mrID, `{"commit_order_key":1,"obsolete":true}`)
}
