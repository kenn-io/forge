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
	mr.AuthorAccountType = nil
	_, err = database.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
	var got *string
	require.NoError(t, database.ReadDB().QueryRowContext(t.Context(), `SELECT author_account_type FROM forge_merge_requests WHERE repo_id=? AND number=1`, repoID).Scan(&got))
	assert.Equal(t, new("bot"), got)
	mr.Author = "other"
	_, err = database.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
	require.NoError(t, database.ReadDB().QueryRowContext(t.Context(), `SELECT author_account_type FROM forge_merge_requests WHERE repo_id=? AND number=1`, repoID).Scan(&got))
	assert.Nil(t, got)
}

func TestSnapshotMeasurementCountsBudget(t *testing.T) {
	database := openTestDB(t)
	now := time.Now().UTC()
	repoID := insertArchiveReportRepo(t, database, RepoIdentity{Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget"})
	closed := now.Add(-time.Minute)
	id, err := database.UpsertMergeRequest(t.Context(), &MergeRequest{RepoID: repoID, PlatformID: 1, Number: 1, Author: "author", AuthorAssociation: new("CONTRIBUTOR"), AuthorAccountType: new("user"), State: MergeRequestStateClosed, ClosedAt: &closed, CreatedAt: now.Add(-time.Hour), UpdatedAt: now})
	require.NoError(t, err)
	measure := func(recent bool) ArchiveReportMeasurement {
		tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		value, err := MeasureArchiveSnapshot(t.Context(), tx, []int64{repoID}, now.Add(-time.Hour), now, false, recent, now)
		require.NoError(t, err)
		return value
	}
	baseline := measure(true)
	_, err = database.WriteDB().ExecContext(t.Context(), `UPDATE forge_merge_requests SET body=? WHERE id=?`, strings.Repeat("x", 12000), id)
	require.NoError(t, err)
	before := measure(true)
	assert.Equal(t, int64(12000), before.TextBytes-baseline.TextBytes)
	require.NoError(t, database.UpsertMREvents(t.Context(), []MREvent{
		{MergeRequestID: id, EventType: "assigned", Author: "author", DedupeKey: "assigned-1", CreatedAt: now},
		{MergeRequestID: id, EventType: "assigned", Author: "author", DedupeKey: "assigned-2", CreatedAt: now},
		{MergeRequestID: id, EventType: "assigned", Author: "author", DedupeKey: "assigned-3", CreatedAt: now},
	}))
	actions := measure(true)
	assert.Equal(t, before.Records+1, actions.Records)
	comment := strings.Repeat("x", 12000)
	require.NoError(t, database.UpsertMREvents(t.Context(), []MREvent{{MergeRequestID: id, EventType: "issue_comment", Author: "author", Body: comment, DedupeKey: "comment", CreatedAt: now}}))
	after := measure(true)
	assert.Equal(t, actions.Records+1, after.Records)
	assert.GreaterOrEqual(t, after.TextBytes-actions.TextBytes, int64(len(comment)))
}

func TestSnapshotActivitySelection(t *testing.T) {
	database := openTestDB(t)
	now := time.Now().UTC()
	repoID := insertArchiveReportRepo(t, database, RepoIdentity{Platform: "gitea", PlatformHost: "git.example", Owner: "acme", Name: "widget"})
	id, err := database.UpsertMergeRequest(t.Context(), &MergeRequest{RepoID: repoID, PlatformID: 1, Number: 1, Author: "author", State: MergeRequestStateOpen, PlatformHeadSHA: "head-sha", CreatedAt: now, UpdatedAt: now})
	require.NoError(t, err)
	require.NoError(t, database.UpsertMREvents(t.Context(), []MREvent{{MergeRequestID: id, PlatformExternalID: "head-sha", EventType: "commit", Author: "Commit Author", Summary: "commit message", DedupeKey: "commit-head", CreatedAt: now}}))
	githubID := insertArchiveReportRepo(t, database, RepoIdentity{Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget"})
	githubMR, err := database.UpsertMergeRequest(t.Context(), &MergeRequest{RepoID: githubID, PlatformID: 2, Number: 2, Author: "author", State: MergeRequestStateOpen, PlatformHeadSHA: "head-sha", CreatedAt: now, UpdatedAt: now})
	require.NoError(t, err)
	require.NoError(t, database.UpsertMREvents(t.Context(), []MREvent{
		{MergeRequestID: githubMR, EventType: "issue_comment", Author: "older-maintainer", AuthorAssociation: new("OWNER"), MetadataJSON: `{"actor_account_type":"user"}`, DedupeKey: "older-review", CreatedAt: now.Add(-5 * time.Minute)},
		{MergeRequestID: githubMR, EventType: "issue_comment", Author: "author", AuthorAssociation: new("CONTRIBUTOR"), MetadataJSON: `{"actor_account_type":"user"}`, DedupeKey: "older-author", CreatedAt: now.Add(-3 * time.Minute)},
		{MergeRequestID: githubMR, EventType: "issue_comment", Author: "author", MetadataJSON: `{"actor_account_type":"user"}`, DedupeKey: "author-unknown-role", CreatedAt: now.Add(-3 * time.Minute)},
		{MergeRequestID: githubMR, EventType: "review", Author: "maintainer", AuthorAssociation: new("MEMBER"), MetadataJSON: `{"actor_account_type":"user"}`, DedupeKey: "review", CreatedAt: now.Add(-4 * time.Minute)},
		{MergeRequestID: githubMR, EventType: "issue_comment", Author: "collaborator", AuthorAssociation: new("COLLABORATOR"), MetadataJSON: `{"actor_account_type":"user"}`, DedupeKey: "collaborator", CreatedAt: now.Add(-150 * time.Second)},
		{MergeRequestID: githubMR, EventType: "commit", Author: "author", AuthorAssociation: new("CONTRIBUTOR"), MetadataJSON: `{"actor_account_type":"user"}`, Summary: "head-sha", DedupeKey: "head", CreatedAt: now.Add(-2 * time.Minute)},
		{MergeRequestID: githubMR, EventType: "commit", Author: "author", AuthorAssociation: new("CONTRIBUTOR"), MetadataJSON: `{"actor_account_type":"user"}`, Summary: "old-sha", DedupeKey: "old", CreatedAt: now},
		{MergeRequestID: githubMR, EventType: "issue_comment", Author: "automation", AuthorAssociation: new("MEMBER"), MetadataJSON: `{"actor_account_type":"bot"}`, DedupeKey: "bot", CreatedAt: now},
		{MergeRequestID: githubMR, EventType: "unassigned", Author: "maintainer", AuthorAssociation: new("MEMBER"), MetadataJSON: `{"actor_account_type":"user"}`, DedupeKey: "unassigned", CreatedAt: now},
	}))
	tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	activity, err := LoadArchiveSnapshotActivity(t.Context(), tx, []int64{id, githubMR})
	require.NoError(t, err)
	require.Len(t, activity, 4)
	assert.Equal(t, "push", activity[0].Kind)
	assert.Equal(t, "Commit Author", activity[0].Actor)
	assert.Equal(t, "unknown", activity[0].Class)
	assert.Equal(t, "maintainer", activity[1].Class)
	assert.Equal(t, "comment", activity[1].Kind)
	assert.Equal(t, "collaborator", activity[1].Actor)
	assert.Equal(t, "author", activity[2].Class)
	assert.Equal(t, "push", activity[2].Kind)
	assert.Equal(t, now.Add(-2*time.Minute), activity[2].OccurredAt)
	assert.Equal(t, "unknown", activity[3].Class)
	assert.Equal(t, "author", activity[3].Actor)
	require.NoError(t, tx.Rollback())
	require.NoError(t, database.UpsertMREvents(t.Context(), []MREvent{{MergeRequestID: githubMR, EventType: "issue_comment", Author: "author", AuthorAssociation: new("COLLABORATOR"), MetadataJSON: `{"actor_account_type":"user"}`, DedupeKey: "collaborator-author", CreatedAt: now.Add(-time.Minute)}}))
	tx, err = database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	activity, err = LoadArchiveSnapshotActivity(t.Context(), tx, []int64{githubMR})
	require.NoError(t, err)
	require.Len(t, activity, 3)
	assert.Equal(t, "collaborator", activity[0].Actor)
	assert.Equal(t, "maintainer", activity[0].Class)
	assert.Equal(t, now.Add(-150*time.Second), activity[0].OccurredAt)
	assert.Equal(t, "author", activity[1].Actor)
	assert.Equal(t, "author", activity[1].Class)
	assert.Equal(t, now.Add(-time.Minute), activity[1].OccurredAt)
}

func TestCommentVisibilityRefreshPreservesActorType(t *testing.T) {
	database := openTestDB(t)
	now := time.Now().UTC()
	repoID := insertArchiveReportRepo(t, database, RepoIdentity{Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget"})
	id := insertArchiveReportMR(t, database, repoID, 1, "mr-1", "Title", "author", now)
	commentID := int64(123)
	require.NoError(t, database.UpsertMREvents(t.Context(), []MREvent{{MergeRequestID: id, PlatformID: &commentID, EventType: "issue_comment", Author: "maintainer", MetadataJSON: `{"actor_account_type":"user"}`, DedupeKey: "comment", CreatedAt: now}}))
	var revision int64
	require.NoError(t, database.ReadDB().QueryRowContext(t.Context(), `SELECT snapshot_revision FROM forge_merge_requests WHERE id=?`, id).Scan(&revision))
	applied, err := database.UpdateMergeRequestCommentMetadataSnapshot(t.Context(), id, revision, []CommentMetadataUpdate{{PlatformID: commentID, MetadataJSON: `{"provider_hidden":true}`}})
	require.NoError(t, err)
	require.True(t, applied)
	var metadata string
	require.NoError(t, database.ReadDB().QueryRowContext(t.Context(), `SELECT metadata_json FROM forge_mr_events WHERE merge_request_id=? AND platform_id=?`, id, commentID).Scan(&metadata))
	assert.JSONEq(t, `{"provider_hidden":true,"actor_account_type":"user"}`, metadata)
}
