package db

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMeasureArchiveSnapshotCountsOwnershipJSON guards against the preflight
// text measure silently missing assignees_json/reviewers_json growth: a
// snapshot whose only growth is a long assignee list must still be counted
// toward the 32 MiB export budget before any item body is loaded into Go.
func TestMeasureArchiveSnapshotCountsOwnershipJSON(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	repoID := insertArchiveReportRepo(t, database, RepoIdentity{
		Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget",
	})
	mrID := insertArchiveReportMR(t, database, repoID, 1, "mr-1", "Ownership growth", "author", now)

	baseline := measureArchiveSnapshotText(t, database, repoID, now)

	names := make([]string, 5000)
	for i := range names {
		names[i] = `"user"`
	}
	longAssignees := "[" + strings.Join(names, ",") + "]"
	_, err := database.WriteDB().ExecContext(t.Context(),
		`UPDATE forge_merge_requests SET assignees_json = ? WHERE id = ?`, longAssignees, mrID)
	require.NoError(err)

	grown := measureArchiveSnapshotText(t, database, repoID, now)
	assert.GreaterOrEqual(grown-baseline, int64(len(longAssignees)),
		"assignees_json growth must be counted in the preflight text measure")
}

func measureArchiveSnapshotText(t *testing.T, database *DB, repoID int64, now time.Time) int64 {
	t.Helper()
	tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	measurement, err := MeasureArchiveSnapshot(t.Context(), tx, []int64{repoID}, now.Add(-time.Hour), now.Add(time.Hour), false, false, now)
	require.NoError(t, err)
	return measurement.TextBytes
}

func TestMergeRequestAuthorAccountTypeRefresh(t *testing.T) {
	database := openTestDB(t)
	now := time.Now().UTC()
	repoID := insertArchiveReportRepo(t, database, RepoIdentity{Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget"})
	mr := &MergeRequest{RepoID: repoID, PlatformID: 1, Number: 1, Author: "author", AuthorAccountType: new("bot"), State: MergeRequestStateOpen, CreatedAt: now, UpdatedAt: now}
	_, err := database.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
	mr.AuthorAccountType = new("unknown")
	_, err = database.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
	var got *string
	require.NoError(t, database.ReadDB().QueryRowContext(t.Context(), `SELECT author_account_type FROM forge_merge_requests WHERE repo_id=? AND number=1`, repoID).Scan(&got))
	assert.Equal(t, new("bot"), got)
	mr.Author = "other"
	_, err = database.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
	require.NoError(t, database.ReadDB().QueryRowContext(t.Context(), `SELECT author_account_type FROM forge_merge_requests WHERE repo_id=? AND number=1`, repoID).Scan(&got))
	assert.Equal(t, new("unknown"), got)
}

func TestSnapshotMeasurementCountsAuthorTypeOnlyForExpandedScope(t *testing.T) {
	database := openTestDB(t)
	now := time.Now().UTC()
	repoID := insertArchiveReportRepo(t, database, RepoIdentity{Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget"})
	mrID := insertArchiveReportMR(t, database, repoID, 1, "mr-1", "Title", "author", now)
	measure := func(recent bool) ArchiveReportMeasurement {
		tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		value, err := MeasureArchiveSnapshot(t.Context(), tx, []int64{repoID}, now.Add(-time.Hour), now, false, recent, now)
		require.NoError(t, err)
		return value
	}
	beforeDefault, beforeExpanded := measure(false), measure(true)
	_, err := database.WriteDB().ExecContext(t.Context(), `UPDATE forge_merge_requests SET author_account_type=? WHERE id=?`, strings.Repeat("a", 100), mrID)
	require.NoError(t, err)
	assert.Equal(t, beforeDefault, measure(false))
	assert.Equal(t, int64(93), measure(true).TextBytes-beforeExpanded.TextBytes)
}

func TestMergeRepositoryKeepsPullReferenceSource(t *testing.T) {
	database := openTestDB(t)
	now := time.Now().UTC()
	original := insertArchiveReportRepo(t, database, RepoIdentity{Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "original"})
	duplicate := insertArchiveReportRepo(t, database, RepoIdentity{Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "old"})
	survivor := insertArchiveReportRepo(t, database, RepoIdentity{Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "new"})
	targetID := insertArchiveReportMR(t, database, original, 1, "target", "Target", "author", now)
	_ = insertArchiveReportMR(t, database, duplicate, 2, "source", "Source", "author", now)
	event := MREvent{MergeRequestID: targetID, EventType: "cross_referenced", DedupeKey: "reference", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_owner":"acme","source_repo":"old","source_number":2,"source_url":"https://example.com/2","is_cross_repository":true}`}
	require.NoError(t, database.UpsertMREvents(t.Context(), []MREvent{event}))
	require.NoError(t, database.Tx(t.Context(), func(tx *sql.Tx) error { return mergeDuplicateRepositoryTx(t.Context(), tx, duplicate, survivor) }))
	var got int64
	require.NoError(t, database.ReadDB().QueryRowContext(t.Context(), `SELECT source_repo_id FROM forge_mr_events WHERE merge_request_id=? AND dedupe_key='reference'`, targetID).Scan(&got))
	assert.Equal(t, survivor, got)
}
