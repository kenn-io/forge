package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordHotMergeRequestViewMaintainsPersistedMRU(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "hot-merge-requests.db")
	database, err := Open(dbPath)
	require.NoError(err)

	repoID, err := database.UpsertRepo(
		ctx,
		GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)

	base := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	ids := make([]int64, 12)
	for i := range ids {
		ids[i] = insertTestMRWithOptions(
			t,
			database,
			testMR(repoID, i+1, withMRActivity(base.Add(time.Duration(i)*time.Minute))),
		)
		require.NoError(database.RecordHotMergeRequestView(
			ctx,
			ids[i],
			base.Add(time.Duration(i)*time.Minute),
		))
	}

	// Revisiting the oldest retained entry moves it to the front without
	// creating a duplicate or growing the bounded hot set.
	require.NoError(database.RecordHotMergeRequestView(ctx, ids[2], base.Add(12*time.Minute)))
	require.NoError(database.Close())

	// The recency set is local durable state, not an in-memory scheduler cache.
	database, err = Open(dbPath)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(database.Close()) })

	got, err := database.ListHotMergeRequestIDs(ctx, 10)
	require.NoError(err)
	assert.Equal(t, []int64{
		ids[2], ids[11], ids[10], ids[9], ids[8],
		ids[7], ids[6], ids[5], ids[4], ids[3],
	}, got)
}

func TestHotMergeRequestTerminalEviction(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	repoID := insertTestRepo(t, database, "acme", "widget")
	base := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	closedID := insertTestMRWithOptions(
		t,
		database,
		testMR(repoID, 1, withMRState(MergeRequestStateClosed)),
	)
	require.NoError(database.RecordHotMergeRequestView(ctx, closedID, base))
	got, err := database.ListHotMergeRequestIDs(ctx, 10)
	require.NoError(err)
	assert.Empty(t, got, "closed PRs must never enter the hot set")

	t.Run("state update", func(t *testing.T) {
		mrID := insertTestMRWithOptions(t, database, testMR(repoID, 2))
		require.NoError(database.RecordHotMergeRequestView(ctx, mrID, base))
		require.NoError(database.UpdateMRState(
			ctx,
			repoID,
			2,
			string(MergeRequestStateMerged),
			&base,
			nil,
		))

		got, listErr := database.ListHotMergeRequestIDs(ctx, 10)
		require.NoError(listErr)
		assert.NotContains(t, got, mrID)
	})

	t.Run("snapshot upsert", func(t *testing.T) {
		mr := testMR(repoID, 3)
		mrID := insertTestMRWithOptions(t, database, mr)
		require.NoError(database.RecordHotMergeRequestView(ctx, mrID, base))
		mr.State = MergeRequestStateClosed
		mr.UpdatedAt = mr.UpdatedAt.Add(time.Minute)
		mr.LastActivityAt = mr.UpdatedAt
		mr.ClosedAt = &mr.UpdatedAt
		_, upsertErr := database.UpsertMergeRequest(ctx, mr)
		require.NoError(upsertErr)

		got, listErr := database.ListHotMergeRequestIDs(ctx, 10)
		require.NoError(listErr)
		assert.NotContains(t, got, mrID)
	})
}
