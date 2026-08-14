package db

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArchiveReportActivityUsesHalfOpenAttributionAndProviderIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	githubRepo := insertArchiveReportRepo(t, database, RepoIdentity{
		Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget",
	})
	gitlabRepo := insertArchiveReportRepo(t, database, RepoIdentity{
		Platform: "gitlab", PlatformHost: "gitlab.example", Owner: "acme", Name: "widget",
	})

	issueID := insertArchiveReportIssue(t, database, githubRepo, 1, "issue-1", "Issue", "sam", start)
	mrID := insertArchiveReportMR(t, database, githubRepo, 2, "mr-2", "Merge request", "", start.Add(time.Hour))
	insertArchiveReportIssue(t, database, githubRepo, 3, "issue-end", "Excluded", "sam", end)
	insertArchiveReportIssue(t, database, gitlabRepo, 1, "issue-gl-1", "GitLab issue", "sam", start)
	insertArchiveReportIssueEvent(t, database, issueID, "issue_comment", "shared-comment", "unknown comment", "", start.Add(2*time.Hour))
	insertArchiveReportMREvent(t, database, mrID, "issue_comment", "shared-comment", "duplicate object", "sam", start.Add(2*time.Hour))
	insertArchiveReportMREvent(t, database, mrID, "review", "review-1", "approved", "reviewer", start.Add(4*time.Hour))
	insertArchiveReportMREvent(t, database, mrID, "review_comment", "inline-1", "inline", "reviewer", start.Add(5*time.Hour))
	insertArchiveReportMREvent(t, database, mrID, "review", "review-end", "excluded", "reviewer", end)

	tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(err)
	t.Cleanup(func() { _ = tx.Rollback() })
	rows, err := LoadArchiveReportActivity(t.Context(), tx, []int64{githubRepo, gitlabRepo}, start, end)
	require.NoError(err)
	require.NoError(tx.Commit())

	require.Len(rows, 6)
	assert.Equal([]ArchiveReportActivityKind{
		ArchiveReportActivityIssue,
		ArchiveReportActivityMergeRequest,
		ArchiveReportActivityOrdinaryComment,
		ArchiveReportActivityReview,
		ArchiveReportActivityInlineReviewComment,
		ArchiveReportActivityIssue,
	}, archiveReportKinds(rows))
	assert.Equal(start, rows[0].OccurredAt)
	assert.Equal("shared-comment", rows[2].ProviderExternalID)
	assert.Equal("unknown comment", rows[2].Body,
		"repository-scoped external identity must deduplicate the second copy")
	assert.Equal("github", rows[0].Platform)
	assert.Equal("gitlab", rows[5].Platform)
	assert.Equal("sam", rows[0].Author)
	assert.Equal("sam", rows[5].Author,
		"equal logins on different providers remain distinct report inputs")
}

func TestArchiveReportActivityHidesRemovedUpstreamParents(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	repoID := insertArchiveReportRepo(t, database, RepoIdentity{
		Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget",
	})

	issueID := insertArchiveReportIssue(
		t, database, repoID, 1, "issue-1", "Removed issue", "sam", start,
	)
	mrID := insertArchiveReportMR(
		t, database, repoID, 2, "mr-2", "Removed merge request", "sam", start,
	)
	insertArchiveReportIssueEvent(
		t, database, issueID, "issue_comment", "comment-1", "removed", "sam", start,
	)
	insertArchiveReportMREvent(
		t, database, mrID, "review", "review-2", "removed", "sam", start,
	)
	visibleID := insertArchiveReportMR(
		t, database, repoID, 3, "mr-3", "Inaccessible merge request", "sam", start,
	)
	insertArchiveReportMREvent(
		t, database, visibleID, "review", "review-3", "cached", "sam", start,
	)
	_, err := database.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES
			(?, 'issue', 1, 'issue-1', ?, ?, 'removed_upstream'),
			(?, 'merge_request', 2, 'mr-2', ?, ?, 'removed_upstream'),
			(?, 'merge_request', 3, 'mr-3', ?, ?, 'inaccessible')`,
		repoID, start, start, repoID, start, start, repoID, start, start,
	)
	require.NoError(err)

	tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(err)
	t.Cleanup(func() { _ = tx.Rollback() })
	rows, err := LoadArchiveReportActivity(t.Context(), tx, []int64{repoID}, start, end)
	require.NoError(err)
	require.NoError(tx.Commit())
	require.Len(rows, 2)
	require.Equal([]ArchiveReportActivityKind{
		ArchiveReportActivityMergeRequest,
		ArchiveReportActivityReview,
	}, archiveReportKinds(rows))
}

func TestArchiveReportActivityMeasuresUTF8Bytes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	repoID := insertArchiveReportRepo(t, database, RepoIdentity{
		Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget",
	})
	activeID := insertArchiveReportIssue(t, database, repoID, 1, "issue-1", "é", "sam", start)
	insertArchiveReportIssueEvent(t, database, activeID, "issue_comment", "comment-1", "猫", "sam", start)

	tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(err)
	t.Cleanup(func() { _ = tx.Rollback() })
	measurement, err := MeasureArchiveReportActivity(t.Context(), tx, []int64{repoID}, start, end)
	require.NoError(err)
	rows, err := LoadArchiveReportActivity(t.Context(), tx, []int64{repoID}, start, end)
	require.NoError(err)
	require.NoError(tx.Commit())

	assert.Equal(2, measurement.Records)
	assert.Equal(int64(2+7+2+3), measurement.TextBytes,
		"UTF-8 bytes include each detailed row's stored title and body")
	require.Len(rows, 2)
	assert.Equal("é", rows[0].Title)
	assert.Equal("猫", rows[1].Body)
}

func TestArchiveReportActivityIncludesCurrentCloseAndMergeLifecycle(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	repoID := insertArchiveReportRepo(t, database, RepoIdentity{
		Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget",
	})

	issueID := insertArchiveReportIssue(t, database, repoID, 1, "issue-1", "Issue", "author", start)
	issueClosedAt := start.Add(time.Hour)
	_, err := database.WriteDB().ExecContext(t.Context(), `
		UPDATE forge_issues SET state = 'closed', closed_at = ?, comment_count = 3
		WHERE id = ?`, issueClosedAt, issueID)
	require.NoError(err)
	insertArchiveReportIssueEvent(
		t, database, issueID, "closed", "issue-closed-1", "", "closer", issueClosedAt,
	)

	mrID := insertArchiveReportMR(
		t, database, repoID, 2, "mr-2", "Merge request", "author", start.Add(-time.Hour),
	)
	mergedAt := start.Add(2 * time.Hour)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE forge_merge_requests
		SET state = 'merged', merged_at = ?, additions = 20, deletions = 4,
			files_changed = 17, merge_commit_sha = 'abc123'
		WHERE id = ?`, mergedAt, mrID)
	require.NoError(err)
	insertArchiveReportMREvent(
		t, database, mrID, "merged", "mr-merged-2", "", "merger", mergedAt,
	)

	reopenedID := insertArchiveReportIssue(
		t, database, repoID, 3, "issue-3", "Reopened", "author", start.Add(-time.Hour),
	)
	insertArchiveReportIssueEvent(
		t, database, reopenedID, "closed", "issue-closed-3", "", "old-closer", start.Add(3*time.Hour),
	)

	tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(err)
	t.Cleanup(func() { _ = tx.Rollback() })
	rows, err := LoadArchiveReportActivity(t.Context(), tx, []int64{repoID}, start, end)
	require.NoError(err)
	counts, err := LoadArchiveReportCounts(t.Context(), tx, []int64{repoID}, start, end)
	require.NoError(err)
	require.NoError(tx.Commit())

	require.Len(rows, 3)
	assert.Equal([]ArchiveReportActivityKind{
		ArchiveReportActivityIssue,
		ArchiveReportActivityIssueClosed,
		ArchiveReportActivityMergeRequestMerged,
	}, archiveReportKinds(rows))
	assert.Equal(3, rows[0].Comments)
	assert.Equal("closer", rows[1].Actor)
	assert.Equal("merger", rows[2].Actor)
	assert.Equal(20, rows[2].Additions)
	assert.Equal(4, rows[2].Deletions)
	require.NotNil(rows[2].FilesChanged)
	assert.Equal(17, *rows[2].FilesChanged)
	assert.Equal("abc123", rows[2].MergeCommitSHA)
	contributorByKind := make(map[ArchiveReportActivityKind]string, len(counts))
	for _, count := range counts {
		contributorByKind[count.Kind] = count.Author
	}
	assert.Equal("author", contributorByKind[ArchiveReportActivityIssue])
	assert.Equal("closer", contributorByKind[ArchiveReportActivityIssueClosed])
	assert.Equal("merger", contributorByKind[ArchiveReportActivityMergeRequestMerged])
}

func TestArchiveReportActivityOmitsActorFromEarlierCloseCycle(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	repoID := insertArchiveReportRepo(t, database, RepoIdentity{
		Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget",
	})
	issueID := insertArchiveReportIssue(
		t, database, repoID, 1, "issue-1", "Issue", "author", start.Add(-time.Hour),
	)
	closedAt := start.Add(2 * time.Hour)
	_, err := database.WriteDB().ExecContext(t.Context(), `
		UPDATE forge_issues SET state = 'closed', closed_at = ? WHERE id = ?`,
		closedAt, issueID)
	require.NoError(err)
	insertArchiveReportIssueEvent(
		t, database, issueID, "closed", "old-close", "", "old-closer",
		start.Add(time.Hour),
	)

	tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(err)
	t.Cleanup(func() { _ = tx.Rollback() })
	rows, err := LoadArchiveReportActivity(t.Context(), tx, []int64{repoID}, start, end)
	require.NoError(err)
	require.NoError(tx.Commit())

	require.Len(rows, 1)
	assert.Equal(ArchiveReportActivityIssueClosed, rows[0].Kind)
	assert.Empty(rows[0].Actor)
}

func TestArchiveReportActivityOmitsActorWhenNewestCloseDoesNotMatch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	repoID := insertArchiveReportRepo(t, database, RepoIdentity{
		Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget",
	})
	issueID := insertArchiveReportIssue(
		t, database, repoID, 1, "issue-1", "Issue", "author", start.Add(-time.Hour),
	)
	closedAt := start.Add(2 * time.Hour)
	_, err := database.WriteDB().ExecContext(t.Context(), `
		UPDATE forge_issues SET state = 'closed', closed_at = ? WHERE id = ?`,
		closedAt, issueID)
	require.NoError(err)
	insertArchiveReportIssueEvent(
		t, database, issueID, "closed", "current-close", "", "current-closer", closedAt,
	)
	insertArchiveReportIssueEvent(
		t, database, issueID, "closed", "newer-mismatch", "", "newer-closer",
		closedAt.Add(time.Hour),
	)

	tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(err)
	t.Cleanup(func() { _ = tx.Rollback() })
	rows, err := LoadArchiveReportActivity(t.Context(), tx, []int64{repoID}, start, end)
	require.NoError(err)
	require.NoError(tx.Commit())

	require.Len(rows, 1)
	assert.Equal(ArchiveReportActivityIssueClosed, rows[0].Kind)
	assert.Empty(rows[0].Actor)
}

func TestArchiveReportRepositoriesAreSnapshotCoverageOrderedByFullIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	second := insertArchiveReportRepo(t, database, RepoIdentity{
		Platform: "gitlab", PlatformHost: "gitlab.example", Owner: "zeta", Name: "repo",
	})
	first := insertArchiveReportRepo(t, database, RepoIdentity{
		Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "repo",
	})
	_, err := database.WriteDB().ExecContext(t.Context(), `
		UPDATE forge_archive_repos
		SET reviews_coverage = 'unsupported'
		WHERE repo_id = ?`, second)
	require.NoError(err)

	tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(err)
	t.Cleanup(func() { _ = tx.Rollback() })
	rows, err := LoadArchiveReportRepositories(t.Context(), tx, []int64{second, first}, now)
	require.NoError(err)
	require.NoError(tx.Commit())

	require.Len(rows, 2)
	assert.Equal(first, rows[0].RepoID)
	assert.Equal(second, rows[1].RepoID)
	assert.Equal(ArchiveStatusCurrent, rows[0].Progress.Status)
	assert.Equal(ArchiveStatusPartial, rows[1].Progress.Status)
	assert.Equal(ArchiveCoverageUnsupported, rows[1].State.ReviewsCoverage)
	assert.Equal("acme/repo", rows[0].RepoPath)
}

func insertArchiveReportRepo(t *testing.T, database *DB, identity RepoIdentity) int64 {
	t.Helper()
	repoID, err := database.UpsertRepo(t.Context(), identity)
	require.NoError(t, err)
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, database.EnsureDiscoveryArchives(t.Context(), []int64{repoID}, now))
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE forge_archive_repos
		SET collection_mode = 'full', initial_started_at = ?,
			initial_completed_at = ?, maintenance_watermark = ?,
			maintenance_succeeded_at = ?, comments_coverage = 'supported',
			reviews_coverage = 'supported', inline_comments_coverage = 'supported'
		WHERE repo_id = ?`, now, now, now, now, repoID)
	require.NoError(t, err)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE forge_archive_repo_scans SET status = 'complete'
		WHERE repo_id = ? AND scan IN ('issue_inventory', 'merge_request_inventory')`, repoID)
	require.NoError(t, err)
	return repoID
}

func insertArchiveReportIssue(
	t *testing.T, database *DB, repoID int64, number int, externalID, title, author string, createdAt time.Time,
) int64 {
	t.Helper()
	result, err := database.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_issues (
			repo_id, platform_id, platform_external_id, number, url, title, author,
			state, body, created_at, updated_at, last_activity_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'open', ?, ?, ?, ?)`,
		repoID, number, externalID, number, "https://example.test/item", title, author,
		"body:"+title, createdAt, createdAt, createdAt)
	require.NoError(t, err)
	id, err := result.LastInsertId()
	require.NoError(t, err)
	return id
}

func insertArchiveReportMR(
	t *testing.T, database *DB, repoID int64, number int, externalID, title, author string, createdAt time.Time,
) int64 {
	t.Helper()
	result, err := database.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_merge_requests (
			repo_id, platform_id, platform_external_id, number, url, title, author,
			state, body, created_at, updated_at, last_activity_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'open', ?, ?, ?, ?)`,
		repoID, number, externalID, number, "https://example.test/item", title, author,
		"body:"+title, createdAt, createdAt, createdAt)
	require.NoError(t, err)
	id, err := result.LastInsertId()
	require.NoError(t, err)
	return id
}

func insertArchiveReportIssueEvent(
	t *testing.T, database *DB, issueID int64, kind, externalID, body, author string, createdAt time.Time,
) {
	t.Helper()
	_, err := database.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_issue_events (
			issue_id, platform_external_id, event_type, author, body, created_at, dedupe_key, direct_url
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, issueID, externalID, kind, author, body,
		createdAt, kind+":"+externalID, "https://example.test/comment")
	require.NoError(t, err)
}

func insertArchiveReportMREvent(
	t *testing.T, database *DB, mrID int64, kind, externalID, body, author string, createdAt time.Time,
) {
	t.Helper()
	_, err := database.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_mr_events (
			merge_request_id, platform_external_id, event_type, author, body,
			created_at, dedupe_key, direct_url
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, mrID, externalID, kind, author, body,
		createdAt, kind+":"+externalID+":"+body, "https://example.test/event")
	require.NoError(t, err)
}

func archiveReportKinds(rows []ArchiveReportActivityRow) []ArchiveReportActivityKind {
	kinds := make([]ArchiveReportActivityKind, len(rows))
	for i := range rows {
		kinds[i] = rows[i].Kind
	}
	return kinds
}
