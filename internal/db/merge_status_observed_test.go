package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nonUTCTestTime returns an instant expressed in a non-UTC offset, so tests
// can assert the storage layer converts it before persisting.
func nonUTCTestTime() time.Time {
	instant, err := time.Parse(time.RFC3339, "2026-03-04T10:00:00+05:00")
	if err != nil {
		panic(err)
	}
	return instant
}

func assertUTCInstant(t *testing.T, want time.Time, got *time.Time) {
	t.Helper()
	require.NotNil(t, got)
	assert.True(t, want.Equal(*got), "expected same instant as %s, got %s", want, got)
	assert.Equal(t, time.UTC, got.Location(), "stored time must be in UTC")
}

func TestUpsertMergeRequestSnapshotStoresObservedTimesInUTC(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "observed-times")
	observedAt := nonUTCTestTime()
	mr := testMR(repoID, 1)
	mr.CIObservedAt = &observedAt
	mr.ReviewDecisionObservedAt = &observedAt
	mr.MergeableStateObservedAt = &observedAt

	_, accepted, err := d.UpsertMergeRequestSnapshot(ctx, mr)
	require.NoError(err)
	require.True(accepted)

	byRoute, err := d.GetMergeRequest(ctx, "github", "github.com", "acme", "observed-times", 1)
	require.NoError(err)
	require.NotNil(byRoute)
	assertUTCInstant(t, observedAt, byRoute.CIObservedAt)
	assertUTCInstant(t, observedAt, byRoute.ReviewDecisionObservedAt)
	assertUTCInstant(t, observedAt, byRoute.MergeableStateObservedAt)

	byRepoAndNumber, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(byRepoAndNumber)
	assertUTCInstant(t, observedAt, byRepoAndNumber.CIObservedAt)
	assertUTCInstant(t, observedAt, byRepoAndNumber.ReviewDecisionObservedAt)
	assertUTCInstant(t, observedAt, byRepoAndNumber.MergeableStateObservedAt)

	listed, err := d.ListMergeRequests(ctx, ListMergeRequestsOpts{})
	require.NoError(err)
	require.Len(listed, 1)
	assertUTCInstant(t, observedAt, listed[0].CIObservedAt)
	assertUTCInstant(t, observedAt, listed[0].ReviewDecisionObservedAt)
	assertUTCInstant(t, observedAt, listed[0].MergeableStateObservedAt)
}

func TestUpsertMergeRequestRejectedByGuardLeavesPreviousObservedTimes(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "guard-leaves-times")
	acceptedAt := nonUTCTestTime()
	newer := testMR(repoID, 1, withMRActivity(baseTime().Add(time.Hour)))
	newer.CIObservedAt = &acceptedAt
	newer.ReviewDecisionObservedAt = &acceptedAt
	newer.MergeableStateObservedAt = &acceptedAt
	_, accepted, err := d.UpsertMergeRequestSnapshot(ctx, newer)
	require.NoError(err)
	require.True(accepted)

	rejectedAt := acceptedAt.Add(time.Hour)
	older := testMR(repoID, 1, withMRActivity(baseTime()))
	older.CIObservedAt = &rejectedAt
	older.ReviewDecisionObservedAt = &rejectedAt
	older.MergeableStateObservedAt = &rejectedAt
	_, accepted, err = d.UpsertMergeRequestSnapshot(ctx, older)
	require.NoError(err)
	assert.False(accepted)

	got, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(got)
	assertUTCInstant(t, acceptedAt, got.CIObservedAt)
	assertUTCInstant(t, acceptedAt, got.ReviewDecisionObservedAt)
	assertUTCInstant(t, acceptedAt, got.MergeableStateObservedAt)
}

func TestUpdateMergeRequestCISnapshotSetsCIObservedAtOnMatchingRevision(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "ci-snapshot-observed")
	mrID, revision, accepted, err := d.UpsertMergeRequestSnapshotWithLabels(ctx, testMR(repoID, 1))
	require.NoError(err)
	require.True(accepted)

	observedAt := nonUTCTestTime()
	applied, err := d.UpdateMergeRequestCISnapshot(ctx, mrID, revision, "success", `[{"name":"build"}]`, &observedAt)
	require.NoError(err)
	require.True(applied)

	got, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("success", got.CIStatus)
	assertUTCInstant(t, observedAt, got.CIObservedAt)

	staleAt := observedAt.Add(time.Hour)
	applied, err = d.UpdateMergeRequestCISnapshot(ctx, mrID, revision-1, "failure", `[{"name":"stale"}]`, &staleAt)
	require.NoError(err)
	assert.False(applied)

	unchanged, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(unchanged)
	assert.Equal("success", unchanged.CIStatus)
	assertUTCInstant(t, observedAt, unchanged.CIObservedAt)
}

func TestClearMRCISnapshotNullsCIObservedAt(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "clear-ci-observed")
	mr := testMR(repoID, 1, withMRBranches("feature", "main"))
	mr.PlatformHeadSHA = "head-sha"
	mrID, revision, accepted, err := d.UpsertMergeRequestSnapshotWithLabels(ctx, mr)
	require.NoError(err)
	require.True(accepted)

	observedAt := nonUTCTestTime()
	applied, err := d.UpdateMergeRequestCISnapshot(ctx, mrID, revision, "success", `[{"name":"build"}]`, &observedAt)
	require.NoError(err)
	require.True(applied)

	applied, err = d.ClearMRCISnapshot(ctx, mrID, revision, "head-sha")
	require.NoError(err)
	require.True(applied)

	got, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(got)
	assert.Empty(got.CIStatus)
	assert.Nil(got.CIObservedAt)
}

func TestUpdateMRCIStatusForHeadSetsObservedAtOnlyOnMatchingHead(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "ci-status-for-head-observed")
	mr := testMR(repoID, 1)
	mr.PlatformHeadSHA = "new-head"
	_, err := d.UpsertMergeRequest(ctx, mr)
	require.NoError(err)

	staleAt := nonUTCTestTime()
	require.NoError(d.UpdateMRCIStatusForHead(ctx, repoID, 1, "old-head", "success", `[]`, false, &staleAt))
	afterStaleHead, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(afterStaleHead)
	assert.Nil(afterStaleHead.CIObservedAt)

	observedAt := nonUTCTestTime()
	require.NoError(d.UpdateMRCIStatusForHead(ctx, repoID, 1, "new-head", "success", `[]`, false, &observedAt))
	afterMatchingHead, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(afterMatchingHead)
	assertUTCInstant(t, observedAt, afterMatchingHead.CIObservedAt)
}

func TestCommitMergeRequestChildSnapshotWritesReviewDecisionObservedAt(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "child-snapshot-review-decision")
	mrID, revision, accepted, err := d.UpsertMergeRequestSnapshotWithLabels(ctx, testMR(repoID, 1))
	require.NoError(err)
	require.True(accepted)

	observedAt := nonUTCTestTime()
	applied, err := d.CommitMergeRequestChildSnapshot(ctx, MergeRequestChildSnapshot{
		MergeRequestID:   mrID,
		ExpectedRevision: revision,
		DerivedFields: &MRDerivedFields{
			ReviewDecision:           "approved",
			ReviewDecisionObservedAt: &observedAt,
		},
	})
	require.NoError(err)
	require.True(applied)

	got, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("approved", got.ReviewDecision)
	assertUTCInstant(t, observedAt, got.ReviewDecisionObservedAt)

	mismatchedAt := observedAt.Add(time.Hour)
	applied, err = d.CommitMergeRequestChildSnapshot(ctx, MergeRequestChildSnapshot{
		MergeRequestID:   mrID,
		ExpectedRevision: revision - 1,
		DerivedFields: &MRDerivedFields{
			ReviewDecision:           "changes_requested",
			ReviewDecisionObservedAt: &mismatchedAt,
		},
	})
	require.NoError(err)
	assert.False(applied)

	unchanged, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(unchanged)
	assert.Equal("approved", unchanged.ReviewDecision)
	assertUTCInstant(t, observedAt, unchanged.ReviewDecisionObservedAt)
}

// mergeStatusObservedColumnExists reports whether forge_merge_requests has the
// named column at the raw connection's current schema version.
func mergeStatusObservedColumnExists(t *testing.T, raw *sql.DB, column string) bool {
	t.Helper()
	rows, err := raw.QueryContext(context.Background(), `PRAGMA table_info(forge_merge_requests)`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk))
		if name == column {
			return true
		}
	}
	require.NoError(t, rows.Err())
	return false
}

func TestMigration000062DownUpRoundTrips(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	dbPath := filepath.Join(t.TempDir(), "merge-status-observed-v62.db")
	raw, migrator := openMigratorForTest(t, dbPath)
	require.NoError(migrator.Migrate(62))

	_, err := raw.ExecContext(t.Context(), `
		INSERT INTO forge_repos (
			id, platform, platform_host, platform_repo_id,
			owner, name, repo_path, owner_key, name_key, repo_path_key,
			created_at
		) VALUES (
			1, 'github', 'github.com', 'provider-1',
			'acme', 'widget', 'acme/widget', 'acme', 'widget', 'acme/widget',
			'2026-01-01T00:00:00Z'
		);
		INSERT INTO forge_merge_requests (
			repo_id, platform_id, number, created_at, updated_at, last_activity_at,
			ci_observed_at, review_decision_observed_at, mergeable_state_observed_at
		) VALUES (
			1, 1, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z',
			'2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'
		)`)
	require.NoError(err)

	require.NoError(migrator.Steps(-1))
	assert.False(mergeStatusObservedColumnExists(t, raw, "ci_observed_at"))
	assert.False(mergeStatusObservedColumnExists(t, raw, "review_decision_observed_at"))
	assert.False(mergeStatusObservedColumnExists(t, raw, "mergeable_state_observed_at"))

	require.NoError(migrator.Steps(1))
	assert.True(mergeStatusObservedColumnExists(t, raw, "ci_observed_at"))
	assert.True(mergeStatusObservedColumnExists(t, raw, "review_decision_observed_at"))
	assert.True(mergeStatusObservedColumnExists(t, raw, "mergeable_state_observed_at"))

	var ciObservedAt sql.NullString
	require.NoError(raw.QueryRowContext(t.Context(),
		`SELECT ci_observed_at FROM forge_merge_requests WHERE id = 1`,
	).Scan(&ciObservedAt))
	assert.False(ciObservedAt.Valid, "re-added column starts NULL after down/up")

	var number int
	require.NoError(raw.QueryRowContext(t.Context(),
		`SELECT number FROM forge_merge_requests WHERE id = 1`,
	).Scan(&number))
	assert.Equal(1, number, "unrelated data survives the down/up round trip")

	assertDatabaseIntegrityForTest(t, raw)
	require.NoError(raw.Close())
}
