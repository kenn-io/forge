package db

import (
	"database/sql"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/require"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	return openTemplateTestDB(t)
}

func openDBWithMigrations(t *testing.T) *DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	return d
}

func TestOpenAndSchema(t *testing.T) {
	d := openDBWithMigrations(t)
	tables := []string{
		"middleman_repos",
		"middleman_merge_requests",
		"middleman_mr_events",
		"middleman_kanban_state",
		"middleman_labels",
		"middleman_merge_request_labels",
		"middleman_issue_labels",
		"middleman_repo_overviews",
		"middleman_mr_review_drafts",
		"middleman_mr_review_draft_comments",
		"middleman_mr_review_threads",
	}
	for _, tbl := range tables {
		var name string
		err := d.ReadDB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl,
		).Scan(&name)
		require.NoErrorf(t, err, "table %s should exist", tbl)
	}

	for _, column := range []string{"workspace_branch", "associated_pr_number", "terminal_backend"} {
		var found string
		err := d.ReadDB().QueryRow(
			`SELECT name
			 FROM pragma_table_info('middleman_workspaces')
			 WHERE name = ?`,
			column,
		).Scan(&found)
		require.NoError(t, err)
		require.Equal(t, column, found)
	}
}

func TestOpenCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.db")
	d, err := Open(path)
	require.NoError(t, err)
	d.Close()
	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestOpenIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	d1, err := Open(path)
	require.NoError(t, err)
	d1.Close()
	d2, err := Open(path)
	require.NoError(t, err)
	d2.Close()
}

func TestOpenCreatesSchemaMigrationsTable(t *testing.T) {
	require := require.New(t)
	d := openDBWithMigrations(t)

	version := latestMigrationVersionForTest(t)
	var actualVersion int
	var dirty bool
	err := d.ReadDB().QueryRow(
		`SELECT version, dirty FROM schema_migrations LIMIT 1`,
	).Scan(&actualVersion, &dirty)
	require.NoError(err)
	require.Equal(version, actualVersion)
	require.False(dirty)
}

func TestOpenMigratesLegacyDatabase(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
	}{
		{name: "schema_version_1", version: 1},
		{name: "schema_version_2", version: 2},
		{name: "schema_version_3", version: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "legacy.db")

			raw, err := sql.Open("sqlite", path)
			require.NoError(err)
			_, err = raw.Exec(legacySchemaSQLForTest(t, tc.version))
			require.NoError(err)
			_, err = raw.Exec(
				`CREATE TABLE middleman_schema_version (version INTEGER NOT NULL)`,
			)
			require.NoError(err)
			_, err = raw.Exec(
				`INSERT INTO middleman_schema_version (version) VALUES (?)`,
				tc.version,
			)
			require.NoError(err)
			require.NoError(raw.Close())

			d, err := Open(path)
			require.NoError(err)
			t.Cleanup(func() { require.NoError(d.Close()) })

			version := latestMigrationVersionForTest(t)
			var actualVersion int
			var dirty bool
			err = d.ReadDB().QueryRow(
				`SELECT version, dirty FROM schema_migrations LIMIT 1`,
			).Scan(&actualVersion, &dirty)
			require.NoError(err)
			require.Equal(version, actualVersion)
			require.False(dirty)
			require.False(tableExistsForTest(t, d.ReadDB(), "middleman_schema_version"))
		})
	}
}

func TestOpenBackfillsLegacyIssueLabelsIntoNormalizedTables(t *testing.T) {
	require := require.New(t)
	path, raw := openSchemaVersion4DBForTest(t)
	defer func() { require.NoError(raw.Close()) }()
	seedLegacyIssueForTest(t, raw, 1, 1, 101, 7, `[{"name":"bug","color":"d73a4a"}]`)

	d, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(d.Close()) })

	var issueLabelCount int
	err = d.ReadDB().QueryRow(`SELECT COUNT(*) FROM middleman_issue_labels WHERE issue_id = ?`, 1).Scan(&issueLabelCount)
	require.NoError(err)
	require.Equal(1, issueLabelCount)

	var platformID sql.NullInt64
	var name string
	var description string
	var color string
	var isDefault bool
	var updatedAt string
	err = d.ReadDB().QueryRow(
		`SELECT l.platform_id, l.name, l.description, l.color, l.is_default, l.updated_at
		 FROM middleman_labels l
		 JOIN middleman_issue_labels il ON il.label_id = l.id
		 WHERE il.issue_id = ?`,
		1,
	).Scan(&platformID, &name, &description, &color, &isDefault, &updatedAt)
	require.NoError(err)
	require.False(platformID.Valid)
	require.Equal("bug", name)
	require.Empty(description)
	require.Equal("d73a4a", color)
	require.False(isDefault)
	require.NotEmpty(updatedAt)
}

func TestOpenIgnoresMalformedLegacyIssueLabelsJSON(t *testing.T) {
	require := require.New(t)
	path, raw := openSchemaVersion4DBForTest(t)
	defer func() { require.NoError(raw.Close()) }()

	seedLegacyIssueForTest(t, raw, 1, 1, 101, 7, `[{"name":"bug","color":"d73a4a"}`)

	d, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(d.Close()) })

	var labelCount int
	err = d.ReadDB().QueryRow(`SELECT COUNT(*) FROM middleman_labels`).Scan(&labelCount)
	require.NoError(err)
	require.Equal(0, labelCount)

	var issueLabelCount int
	err = d.ReadDB().QueryRow(`SELECT COUNT(*) FROM middleman_issue_labels`).Scan(&issueLabelCount)
	require.NoError(err)
	require.Equal(0, issueLabelCount)
}

func TestOpenBackfillsDuplicateLegacyIssueLabelsDeterministically(t *testing.T) {
	require := require.New(t)
	path, raw := openSchemaVersion4DBForTest(t)
	defer func() { require.NoError(raw.Close()) }()

	seedLegacyIssueForTest(t, raw, 1, 1, 101, 7, `[{"name":"bug","color":"ff0000"}]`)
	seedLegacyIssueForTest(t, raw, 2, 1, 102, 8, `[{"name":"bug","color":"00ff00"}]`)

	d, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(d.Close()) })

	var labelCount int
	err = d.ReadDB().QueryRow(`SELECT COUNT(*) FROM middleman_labels WHERE repo_id = ? AND name = ?`, 1, "bug").Scan(&labelCount)
	require.NoError(err)
	require.Equal(1, labelCount)

	var color string
	err = d.ReadDB().QueryRow(
		`SELECT color FROM middleman_labels WHERE repo_id = ? AND name = ?`,
		1,
		"bug",
	).Scan(&color)
	require.NoError(err)
	require.Equal("00ff00", color)
}

func TestOpenCasefoldsDuplicateRepositoryRows(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	raw, err := sql.Open("sqlite", path)
	require.NoError(err)
	_, err = raw.Exec(legacySchemaSQLForTest(t, 7))
	require.NoError(err)
	_, err = raw.Exec(`CREATE TABLE schema_migrations (version uint64, dirty bool)`)
	require.NoError(err)
	_, err = raw.Exec(`INSERT INTO schema_migrations (version, dirty) VALUES (7, FALSE)`)
	require.NoError(err)
	_, err = raw.Exec(`
		INSERT INTO middleman_repos (
			id, platform, platform_host, owner, name,
			created_at, backfill_pr_page, backfill_pr_complete,
			backfill_issue_page, backfill_issue_complete
		) VALUES
			(1, 'github', 'github.com', 'Org', 'Foo', datetime('now'), 0, 0, 0, 0),
			(2, 'github', 'github.com', 'org', 'foo', datetime('now'), 0, 0, 0, 0),
			(3, 'github', 'github.com', 'ORG', 'FOO', datetime('now'), 0, 0, 0, 0)`)
	require.NoError(err)
	_, err = raw.Exec(`
		INSERT INTO middleman_merge_requests (
			id, repo_id, platform_id, number, url, title, author, state,
			created_at, updated_at, last_activity_at
		) VALUES
			(1, 1, 100, 1, 'https://github.com/Org/Foo/pull/1', 'PR', 'octo', 'open',
			 datetime('now'), datetime('now'), datetime('now')),
			(2, 2, 100, 1, 'https://github.com/org/foo/pull/1', 'PR', 'octo', 'open',
			 datetime('now'), datetime('now'), datetime('now')),
			(4, 3, 100, 1, 'https://github.com/ORG/FOO/pull/1', 'PR', 'octo', 'open',
			 datetime('now'), datetime('now'), datetime('now')),
			(3, 2, 200, 2, 'https://github.com/org/foo/pull/2', 'Unique PR', 'octo', 'open',
			 datetime('now'), datetime('now'), datetime('now'))`)
	require.NoError(err)
	_, err = raw.Exec(`
		INSERT INTO middleman_mr_events (
			merge_request_id, event_type, author, created_at, dedupe_key
		) VALUES
			(2, 'comment', 'octo', datetime('now'), 'duplicate-pr-comment'),
			(4, 'comment', 'octo', datetime('now'), 'duplicate-pr-comment'),
			(3, 'comment', 'octo', datetime('now'), 'unique-comment')`)
	require.NoError(err)
	_, err = raw.Exec(`
		INSERT INTO middleman_kanban_state (merge_request_id, status, updated_at)
		VALUES
			(1, 'new', '2024-01-01T00:00:00Z'),
			(2, 'reviewing', '2024-01-02T00:00:00Z'),
			(3, 'reviewing', '2024-01-03T00:00:00Z')`)
	require.NoError(err)
	_, err = raw.Exec(`
		INSERT INTO middleman_issues (
			id, repo_id, platform_id, number, url, title, author, state,
			created_at, updated_at, last_activity_at
		) VALUES
			(1, 1, 800, 8, 'https://github.com/Org/Foo/issues/8', 'Issue', 'octo', 'open',
			 datetime('now'), datetime('now'), datetime('now')),
			(2, 2, 800, 8, 'https://github.com/org/foo/issues/8', 'Issue', 'octo', 'open',
			 datetime('now'), datetime('now'), datetime('now')),
			(4, 3, 800, 8, 'https://github.com/ORG/FOO/issues/8', 'Issue', 'octo', 'open',
			 datetime('now'), datetime('now'), datetime('now')),
			(3, 2, 900, 9, 'https://github.com/org/foo/issues/9', 'Unique issue', 'octo', 'open',
			 datetime('now'), datetime('now'), datetime('now'))`)
	require.NoError(err)
	_, err = raw.Exec(`
		DROP INDEX idx_issue_events_created;
		ALTER TABLE middleman_issue_events RENAME TO middleman_issue_events_strict;
		CREATE TABLE middleman_issue_events (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id     INTEGER NOT NULL REFERENCES middleman_issues(id) ON DELETE CASCADE,
			platform_id  INTEGER,
			event_type   TEXT NOT NULL,
			author       TEXT NOT NULL,
			summary      TEXT NOT NULL DEFAULT '',
			body         TEXT,
			metadata_json TEXT NOT NULL DEFAULT '',
			created_at   TEXT NOT NULL,
			dedupe_key   TEXT NOT NULL,
			UNIQUE(issue_id, dedupe_key)
		);
		CREATE INDEX idx_issue_events_created
			ON middleman_issue_events(issue_id, created_at DESC);
		DROP TABLE middleman_issue_events_strict;`)
	require.NoError(err)
	_, err = raw.Exec(`
		INSERT INTO middleman_issue_events (
			issue_id, event_type, author, created_at, dedupe_key
		) VALUES
			(2, 'comment', 'octo', datetime('now'), 'duplicate-issue-comment'),
			(4, 'comment', 'octo', datetime('now'), 'duplicate-issue-comment')`)
	require.NoError(err)
	_, err = raw.Exec(`
		INSERT INTO middleman_labels (
			id, repo_id, platform_id, name, updated_at
		) VALUES
			(1, 1, 700, 'enhancement-renamed', datetime('now')),
			(2, 2, 700, 'enhancement', datetime('now')),
			(3, 2, 701, 'triage', datetime('now')),
			(4, 1, NULL, 'stale-label', datetime('now')),
			(5, 2, 702, 'stale-label', datetime('now'))`)
	require.NoError(err)
	_, err = raw.Exec(`
		INSERT INTO middleman_issue_labels (issue_id, label_id)
		VALUES
			(2, 3),
			(3, 2)`)
	require.NoError(err)
	_, err = raw.Exec(`
		INSERT INTO middleman_merge_request_labels (merge_request_id, label_id)
		VALUES (3, 2)`)
	require.NoError(err)
	_, err = raw.Exec(`
		INSERT INTO middleman_starred_items (item_type, repo_id, number)
		VALUES ('issue', 2, 9)`)
	require.NoError(err)
	_, err = raw.Exec(`
		INSERT INTO middleman_stacks (id, repo_id, base_number, name)
		VALUES (1, 2, 2, 'Unique stack')`)
	require.NoError(err)
	_, err = raw.Exec(`
		INSERT INTO middleman_workspaces (
			id, platform_host, repo_owner, repo_name, mr_number, mr_head_ref,
			worktree_path, tmux_session
		) VALUES
			('one', 'github.com', 'Org', 'Foo', 1, 'feature', '/tmp/one', 'one'),
			('two', 'github.com', 'org', 'foo', 1, 'feature', '/tmp/two', 'two'),
			('three', 'github.com', 'org', 'foo', 2, 'feature-2', '/tmp/three', 'three')`)
	require.NoError(err)
	require.NoError(raw.Close())

	d, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(d.Close()) })

	repos, err := d.ListRepos(t.Context())
	require.NoError(err)
	require.Len(repos, 1)
	require.Equal("org", repos[0].Owner)
	require.Equal("foo", repos[0].Name)

	var prCount int
	err = d.ReadDB().QueryRow(`SELECT COUNT(*) FROM middleman_merge_requests`).Scan(&prCount)
	require.NoError(err)
	require.Equal(2, prCount)

	var uniquePRRepoID int
	err = d.ReadDB().QueryRow(
		`SELECT repo_id FROM middleman_merge_requests WHERE number = 2`,
	).Scan(&uniquePRRepoID)
	require.NoError(err)
	require.Equal(1, uniquePRRepoID)

	var uniquePREventCount int
	err = d.ReadDB().QueryRow(`
		SELECT COUNT(*)
		FROM middleman_mr_events e
		JOIN middleman_merge_requests mr ON mr.id = e.merge_request_id
		WHERE mr.number = 2`,
	).Scan(&uniquePREventCount)
	require.NoError(err)
	require.Equal(1, uniquePREventCount)

	var duplicatePREventCount int
	err = d.ReadDB().QueryRow(`
		SELECT COUNT(*)
		FROM middleman_mr_events e
		JOIN middleman_merge_requests mr ON mr.id = e.merge_request_id
		WHERE mr.number = 1 AND e.dedupe_key = 'duplicate-pr-comment'`,
	).Scan(&duplicatePREventCount)
	require.NoError(err)
	require.Equal(1, duplicatePREventCount)

	var kanbanStatus string
	err = d.ReadDB().QueryRow(`
		SELECT ks.status
		FROM middleman_kanban_state ks
		JOIN middleman_merge_requests mr ON mr.id = ks.merge_request_id
		WHERE mr.number = 2`,
	).Scan(&kanbanStatus)
	require.NoError(err)
	require.Equal("reviewing", kanbanStatus)

	var mergedKanbanStatus string
	err = d.ReadDB().QueryRow(`
		SELECT ks.status
		FROM middleman_kanban_state ks
		JOIN middleman_merge_requests mr ON mr.id = ks.merge_request_id
		WHERE mr.number = 1`,
	).Scan(&mergedKanbanStatus)
	require.NoError(err)
	require.Equal("reviewing", mergedKanbanStatus)

	var duplicateIssueEventCount int
	err = d.ReadDB().QueryRow(`
		SELECT COUNT(*)
		FROM middleman_issue_events e
		JOIN middleman_issues i ON i.id = e.issue_id
		WHERE i.number = 8 AND e.dedupe_key = 'duplicate-issue-comment'`,
	).Scan(&duplicateIssueEventCount)
	require.NoError(err)
	require.Equal(1, duplicateIssueEventCount)

	var duplicateIssueLabelCount int
	err = d.ReadDB().QueryRow(`
		SELECT COUNT(*)
		FROM middleman_issue_labels il
		JOIN middleman_issues i ON i.id = il.issue_id
		JOIN middleman_labels l ON l.id = il.label_id
		WHERE i.number = 8 AND l.name = 'triage'`,
	).Scan(&duplicateIssueLabelCount)
	require.NoError(err)
	require.Equal(1, duplicateIssueLabelCount)

	var issueRepoID int
	err = d.ReadDB().QueryRow(
		`SELECT repo_id FROM middleman_issues WHERE number = 9`,
	).Scan(&issueRepoID)
	require.NoError(err)
	require.Equal(1, issueRepoID)

	var labelRepoID int
	err = d.ReadDB().QueryRow(
		`SELECT repo_id FROM middleman_labels WHERE platform_id = 700`,
	).Scan(&labelRepoID)
	require.NoError(err)
	require.Equal(1, labelRepoID)

	var issuePlatformLabelCount int
	err = d.ReadDB().QueryRow(`
		SELECT COUNT(*)
		FROM middleman_issue_labels il
		JOIN middleman_issues i ON i.id = il.issue_id
		JOIN middleman_labels l ON l.id = il.label_id
		WHERE i.number = 9 AND l.platform_id = 700`,
	).Scan(&issuePlatformLabelCount)
	require.NoError(err)
	require.Equal(1, issuePlatformLabelCount)

	var staleNamePlatformLabelCount int
	err = d.ReadDB().QueryRow(`
		SELECT COUNT(*)
		FROM middleman_labels
		WHERE repo_id = 1 AND name = 'stale-label' AND platform_id = 702`,
	).Scan(&staleNamePlatformLabelCount)
	require.NoError(err)
	require.Equal(1, staleNamePlatformLabelCount)

	var mrPlatformLabelCount int
	err = d.ReadDB().QueryRow(`
		SELECT COUNT(*)
		FROM middleman_merge_request_labels mrl
		JOIN middleman_merge_requests mr ON mr.id = mrl.merge_request_id
		JOIN middleman_labels l ON l.id = mrl.label_id
		WHERE mr.number = 2 AND l.platform_id = 700`,
	).Scan(&mrPlatformLabelCount)
	require.NoError(err)
	require.Equal(1, mrPlatformLabelCount)

	var starredRepoID int
	err = d.ReadDB().QueryRow(
		`SELECT repo_id FROM middleman_starred_items WHERE item_type = 'issue' AND number = 9`,
	).Scan(&starredRepoID)
	require.NoError(err)
	require.Equal(1, starredRepoID)

	var stackRepoID int
	err = d.ReadDB().QueryRow(
		`SELECT repo_id FROM middleman_stacks WHERE base_number = 2`,
	).Scan(&stackRepoID)
	require.NoError(err)
	require.Equal(1, stackRepoID)

	var workspaceCount int
	err = d.ReadDB().QueryRow(`SELECT COUNT(*) FROM middleman_workspaces`).Scan(&workspaceCount)
	require.NoError(err)
	require.Equal(2, workspaceCount)

	var integrityCheck string
	err = d.ReadDB().QueryRow(`PRAGMA integrity_check`).Scan(&integrityCheck)
	require.NoError(err)
	require.Equal("ok", integrityCheck)

	var foreignKeyViolations int
	err = d.ReadDB().QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations)
	require.NoError(err)
	require.Zero(foreignKeyViolations)
}

func TestOpenRepairsLegacyTimestampStorage(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	ctx := t.Context()

	d, err := Open(path)
	require.NoError(err)

	repoID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	mrID, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID:            repoID,
		PlatformID:        101,
		Number:            1,
		URL:               "https://github.com/acme/widget/pull/1",
		Title:             "Legacy PR",
		Author:            "octocat",
		AuthorDisplayName: "octocat",
		State:             "open",
		HeadBranch:        "feature",
		BaseBranch:        "main",
		CreatedAt:         time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
		UpdatedAt:         time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
		LastActivityAt:    time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(err)
	issueID, err := d.UpsertIssue(ctx, &Issue{
		RepoID:         repoID,
		PlatformID:     201,
		Number:         2,
		URL:            "https://github.com/acme/widget/issues/2",
		Title:          "Legacy issue",
		Author:         "octocat",
		State:          "open",
		CreatedAt:      time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
		LastActivityAt: time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(err)
	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: mrID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "legacy comment",
		CreatedAt:      time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
		DedupeKey:      "comment-1",
	}}))
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID:   issueID,
		EventType: "issue_comment",
		Author:    "reporter",
		Body:      "legacy issue comment",
		CreatedAt: time.Date(2026, 4, 11, 13, 0, 0, 0, time.UTC),
		DedupeKey: "issue-comment-1",
	}}))
	require.NoError(d.Close())

	raw, err := sql.Open("sqlite", path)
	require.NoError(err)
	_, err = raw.ExecContext(ctx, `
		UPDATE middleman_merge_requests
		SET created_at = ?, updated_at = ?, last_activity_at = ?
		WHERE repo_id = ? AND number = ?`,
		"2026-04-11 08:00:00 -0400 EDT",
		"2026-04-11 08:00:00 -0400 EDT",
		"2026-04-11 09:00:00 -0400 EDT",
		repoID,
		1,
	)
	require.NoError(err)
	_, err = raw.ExecContext(ctx, `
		UPDATE middleman_issues
		SET created_at = ?, updated_at = ?, last_activity_at = ?
		WHERE repo_id = ? AND number = ?`,
		"2026-04-11 08:30:00 -0400 EDT",
		"2026-04-11 08:30:00 -0400 EDT",
		"2026-04-11 09:30:00 -0400 EDT",
		repoID,
		2,
	)
	require.NoError(err)
	_, err = raw.ExecContext(ctx,
		`UPDATE middleman_mr_events SET created_at = ? WHERE dedupe_key = ?`,
		"2026-04-11 08:00:00 -0400 EDT",
		"comment-1",
	)
	require.NoError(err)
	_, err = raw.ExecContext(ctx,
		`UPDATE middleman_issue_events SET created_at = ? WHERE dedupe_key = ?`,
		"2026-04-11 09:00:00 -0400 EDT",
		"issue-comment-1",
	)
	require.NoError(err)
	_, err = raw.ExecContext(ctx, `
		UPDATE middleman_repos
		SET last_sync_started_at = ?, last_sync_completed_at = ?,
		    backfill_pr_completed_at = ?, backfill_issue_completed_at = ?
		WHERE id = ?`,
		"2026-04-11 07:00:00 -0400 EDT",
		"2026-04-11 07:30:00 -0400 EDT",
		"2026-04-11 08:00:00 -0400 EDT",
		"2026-04-11 08:30:00 -0400 EDT",
		repoID,
	)
	require.NoError(err)
	require.NoError(rewriteWorkspacesToVersion9ForTest(raw))
	require.NoError(removeWorkflowApprovalColumnsForTest(raw))
	require.NoError(removeMergeRequestLockedColumnForTest(raw))
	require.NoError(removeProviderIdentityColumnsForTest(raw))
	require.NoError(removeDiscussionColumnsForTest(raw))
	require.NoError(removeIssueAssigneesColumnForTest(raw))
	_, err = raw.ExecContext(ctx,
		`UPDATE schema_migrations SET version = ?, dirty = FALSE`,
		9,
	)
	require.NoError(err)
	require.NoError(raw.Close())

	reopened, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(reopened.Close()) })

	rows, err := reopened.ReadDB().QueryContext(ctx, `
		SELECT last_sync_started_at FROM middleman_repos
		UNION ALL
		SELECT last_sync_completed_at FROM middleman_repos
		UNION ALL
		SELECT backfill_pr_completed_at FROM middleman_repos
		UNION ALL
		SELECT backfill_issue_completed_at FROM middleman_repos
		UNION ALL
		SELECT created_at FROM middleman_merge_requests
		UNION ALL
		SELECT updated_at FROM middleman_merge_requests
		UNION ALL
		SELECT last_activity_at FROM middleman_merge_requests
		UNION ALL
		SELECT created_at FROM middleman_issues
		UNION ALL
		SELECT updated_at FROM middleman_issues
		UNION ALL
		SELECT last_activity_at FROM middleman_issues
		UNION ALL
		SELECT created_at FROM middleman_mr_events
		UNION ALL
		SELECT created_at FROM middleman_issue_events`)
	require.NoError(err)
	defer rows.Close()

	for rows.Next() {
		var value string
		require.NoError(rows.Scan(&value))
		require.NotContains(value, "EDT")
		require.NotContains(value, "-0400")
	}
	require.NoError(rows.Err())

	var firstPass []string
	rows, err = reopened.ReadDB().QueryContext(ctx, `
		SELECT last_sync_started_at FROM middleman_repos
		UNION ALL
		SELECT last_sync_completed_at FROM middleman_repos
		UNION ALL
		SELECT backfill_pr_completed_at FROM middleman_repos
		UNION ALL
		SELECT backfill_issue_completed_at FROM middleman_repos`)
	require.NoError(err)
	defer rows.Close()
	for rows.Next() {
		var value string
		require.NoError(rows.Scan(&value))
		firstPass = append(firstPass, value)
	}
	require.NoError(rows.Err())

	require.NoError(reopened.Close())
	reopened, err = Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(reopened.Close()) })

	var secondPass []string
	rows, err = reopened.ReadDB().QueryContext(ctx, `
		SELECT last_sync_started_at FROM middleman_repos
		UNION ALL
		SELECT last_sync_completed_at FROM middleman_repos
		UNION ALL
		SELECT backfill_pr_completed_at FROM middleman_repos
		UNION ALL
		SELECT backfill_issue_completed_at FROM middleman_repos`)
	require.NoError(err)
	defer rows.Close()
	for rows.Next() {
		var value string
		require.NoError(rows.Scan(&value))
		secondPass = append(secondPass, value)
	}
	require.NoError(rows.Err())
	require.Equal(firstPass, secondPass)
}

func TestOpenRepairsBrokenWorkspaceMigrationVersion11(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "broken-v11.db")

	d, err := Open(path)
	require.NoError(err)
	require.NoError(d.Close())

	raw, err := sql.Open("sqlite", path)
	require.NoError(err)
	require.NoError(rewriteWorkspacesToVersion11ForTest(raw))
	require.NoError(removeWorkflowApprovalColumnsForTest(raw))
	require.NoError(removeMergeRequestLockedColumnForTest(raw))
	require.NoError(removeProviderIdentityColumnsForTest(raw))
	require.NoError(removeDiscussionColumnsForTest(raw))
	require.NoError(removeIssueAssigneesColumnForTest(raw))
	_, err = raw.Exec(`UPDATE schema_migrations SET version = 11, dirty = FALSE`)
	require.NoError(err)
	require.NoError(raw.Close())

	reopened, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(reopened.Close()) })

	version := latestMigrationVersionForTest(t)
	var actualVersion int
	var dirty bool
	err = reopened.ReadDB().QueryRow(
		`SELECT version, dirty FROM schema_migrations LIMIT 1`,
	).Scan(&actualVersion, &dirty)
	require.NoError(err)
	require.Equal(version, actualVersion)
	require.False(dirty)

	var workspaceBranchColumn string
	err = reopened.ReadDB().QueryRow(
		`SELECT name
		 FROM pragma_table_info('middleman_workspaces')
		 WHERE name = ?`,
		"workspace_branch",
	).Scan(&workspaceBranchColumn)
	require.NoError(err)
	require.Equal("workspace_branch", workspaceBranchColumn)

	require.True(
		tableExistsForTest(
			t, reopened.ReadDB(),
			"middleman_workspace_setup_events",
		),
	)
}

func TestOpenRepairsCurrentSchemaMissingWorkspaceTerminalBackend(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, "broken-terminal-backend.db")

	d, err := Open(path)
	require.NoError(err)
	require.NoError(d.Close())

	raw, err := sql.Open("sqlite", path)
	require.NoError(err)
	_, err = raw.Exec(`ALTER TABLE middleman_workspaces DROP COLUMN terminal_backend`)
	require.NoError(err)
	require.NoError(raw.Close())

	reopened, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(reopened.Close()) })

	var terminalBackendColumn string
	err = reopened.ReadDB().QueryRow(
		`SELECT name
		 FROM pragma_table_info('middleman_workspaces')
		 WHERE name = ?`,
		"terminal_backend",
	).Scan(&terminalBackendColumn)
	require.NoError(err)
	require.Equal("terminal_backend", terminalBackendColumn)

	err = reopened.InsertWorkspace(ctx, &Workspace{
		ID:              "ws-terminal-backend",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/backend",
		WorktreePath:    "/tmp/ws-terminal-backend",
		TmuxSession:     "middleman-ws-terminal-backend",
		Status:          "creating",
		WorkspaceBranch: "feature/backend",
	})
	require.NoError(err)
}

func TestOpenInitializesBranchActivitySchema(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, "branch-activity.db")

	d, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(d.Close()) })

	for table, columns := range map[string][]string{
		"middleman_branch_commits":      {"observed_order", "created_at", "updated_at"},
		"middleman_branch_tips":         {"created_at", "updated_at"},
		"middleman_branch_force_pushes": {"before_observed_at", "created_at"},
	} {
		for _, column := range columns {
			hasColumn, err := hasColumn(d.ReadDB(), table, column)
			require.NoError(err)
			require.Truef(hasColumn, "%s.%s should exist after migration", table, column)
		}
	}

	repoID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NoError(d.UpsertBranchCommits(ctx, []BranchCommit{{
		RepoID:         repoID,
		BranchName:     "main",
		CommitSHA:      "shared-sha",
		AuthorName:     "Alice",
		AuthorEmail:    "alice@example.com",
		AuthoredAt:     time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		CommitterName:  "Alice",
		CommitterEmail: "alice@example.com",
		CommittedAt:    time.Date(2026, 5, 1, 12, 1, 0, 0, time.UTC),
		Subject:        "main work",
	}, {
		RepoID:         repoID,
		BranchName:     "release",
		CommitSHA:      "shared-sha",
		AuthorName:     "Alice",
		AuthorEmail:    "alice@example.com",
		AuthoredAt:     time.Date(2026, 5, 1, 12, 3, 0, 0, time.UTC),
		CommitterName:  "Alice",
		CommitterEmail: "alice@example.com",
		CommittedAt:    time.Date(2026, 5, 1, 12, 4, 0, 0, time.UTC),
		Subject:        "release work",
	}}))
	require.NoError(d.InsertBranchForcePush(ctx, BranchForcePush{
		RepoID:     repoID,
		BranchName: "main",
		BeforeSHA:  "before-sha",
		AfterSHA:   "after-sha",
		DetectedAt: time.Date(2026, 5, 1, 12, 2, 0, 0, time.UTC),
	}))
	require.NoError(d.InsertBranchForcePush(ctx, BranchForcePush{
		RepoID:     repoID,
		BranchName: "main",
		BeforeSHA:  "before-sha",
		AfterSHA:   "after-sha",
		DetectedAt: time.Date(2026, 5, 1, 12, 5, 0, 0, time.UTC),
	}))

	var commitRows int
	err = d.ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM middleman_branch_commits
		WHERE repo_id = ? AND commit_sha = ?`,
		repoID,
		"shared-sha",
	).Scan(&commitRows)
	require.NoError(err)
	require.Equal(2, commitRows)

	var forcePushRows int
	err = d.ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM middleman_branch_force_pushes
		WHERE repo_id = ? AND before_sha = ? AND after_sha = ?`,
		repoID,
		"before-sha",
		"after-sha",
	).Scan(&forcePushRows)
	require.NoError(err)
	require.Equal(2, forcePushRows)
}

func TestOpenMigratesWorkspaceUniquenessAndPreservesSetupEvents(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace-v11.db")

	d, err := Open(path)
	require.NoError(err)

	ws := &Workspace{
		ID:              "ws-preserve",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        WorkspaceItemTypePullRequest,
		ItemNumber:      7,
		GitHeadRef:      "feat/preserve",
		WorktreePath:    "/tmp/ws-preserve",
		TmuxSession:     "ws-preserve",
		Status:          "creating",
		WorkspaceBranch: "feat/preserve",
	}
	require.NoError(d.InsertWorkspace(ctx, ws))
	require.NoError(d.InsertWorkspaceSetupEvent(ctx, &WorkspaceSetupEvent{
		WorkspaceID: ws.ID,
		Stage:       "clone",
		Outcome:     "success",
		Message:     "cloned bare repository",
	}))
	require.NoError(d.Close())

	raw, err := sql.Open("sqlite", path)
	require.NoError(err)
	require.NoError(rewriteWorkspacesToVersion11ForTest(raw))
	require.NoError(removeWorkflowApprovalColumnsForTest(raw))
	require.NoError(removeMergeRequestLockedColumnForTest(raw))
	require.NoError(removeProviderIdentityColumnsForTest(raw))
	require.NoError(removeDiscussionColumnsForTest(raw))
	require.NoError(removeIssueAssigneesColumnForTest(raw))
	_, err = raw.Exec(`UPDATE schema_migrations SET version = 11, dirty = FALSE`)
	require.NoError(err)
	require.NoError(raw.Close())

	reopened, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(reopened.Close()) })

	events, err := reopened.ListWorkspaceSetupEvents(ctx, ws.ID)
	require.NoError(err)
	require.Len(events, 1)
	require.Equal("clone", events[0].Stage)
	require.Equal("success", events[0].Outcome)
	require.Equal("cloned bare repository", events[0].Message)

	issueWS := &Workspace{
		ID:              "ws-preserve-issue",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        WorkspaceItemTypeIssue,
		ItemNumber:      7,
		GitHeadRef:      "middleman/issue-7",
		WorktreePath:    "/tmp/ws-preserve-issue",
		TmuxSession:     "ws-preserve-issue",
		Status:          "creating",
		WorkspaceBranch: "middleman/issue-7",
	}
	require.NoError(reopened.InsertWorkspace(ctx, issueWS))
}

func TestRepoTimestampWritesStoreUTC(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	//nolint:forbidigo // Test fixture intentionally uses a non-UTC zone to verify normalization.
	edt := time.FixedZone("EDT", -4*60*60)
	startedAt := time.Date(2026, 4, 11, 8, 0, 0, 0, edt)
	completedAt := time.Date(2026, 4, 11, 8, 30, 0, 0, edt)
	backfillPRCompletedAt := time.Date(2026, 4, 11, 9, 0, 0, 0, edt)
	backfillIssueCompletedAt := time.Date(2026, 4, 11, 9, 30, 0, 0, edt)

	require.NoError(d.UpdateRepoSyncStarted(ctx, repoID, startedAt))
	require.NoError(d.UpdateRepoSyncCompleted(ctx, repoID, completedAt, ""))
	require.NoError(d.UpdateBackfillCursor(
		ctx,
		repoID,
		1, true, &backfillPRCompletedAt,
		2, true, &backfillIssueCompletedAt,
	))

	rows, err := d.ReadDB().QueryContext(ctx, `
		SELECT last_sync_started_at FROM middleman_repos WHERE id = ?
		UNION ALL
		SELECT last_sync_completed_at FROM middleman_repos WHERE id = ?
		UNION ALL
		SELECT backfill_pr_completed_at FROM middleman_repos WHERE id = ?
		UNION ALL
		SELECT backfill_issue_completed_at FROM middleman_repos WHERE id = ?`,
		repoID, repoID, repoID, repoID,
	)
	require.NoError(err)
	defer rows.Close()
	for rows.Next() {
		var value string
		require.NoError(rows.Scan(&value))
		require.NotContains(value, "EDT")
		require.NotContains(value, "-0400")
	}
	require.NoError(rows.Err())

	repo, err := d.GetRepoByID(ctx, repoID)
	require.NoError(err)
	require.NotNil(repo)
	require.NotNil(repo.LastSyncStartedAt)
	require.NotNil(repo.LastSyncCompletedAt)
	require.NotNil(repo.BackfillPRCompletedAt)
	require.NotNil(repo.BackfillIssueCompletedAt)
	require.Equal(time.UTC, repo.LastSyncStartedAt.Location())
	require.Equal(time.UTC, repo.LastSyncCompletedAt.Location())
	require.Equal(time.UTC, repo.BackfillPRCompletedAt.Location())
	require.Equal(time.UTC, repo.BackfillIssueCompletedAt.Location())
	require.Equal(startedAt.UTC(), *repo.LastSyncStartedAt)
	require.Equal(completedAt.UTC(), *repo.LastSyncCompletedAt)
	require.Equal(backfillPRCompletedAt.UTC(), *repo.BackfillPRCompletedAt)
	require.Equal(backfillIssueCompletedAt.UTC(), *repo.BackfillIssueCompletedAt)
}

func TestOpenRejectsUnsupportedLegacySchemaVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
	}{
		{name: "version_0", version: 0},
		{name: "version_99", version: 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testRejectsUnsupportedLegacySchemaVersion(t, tc.version)
		})
	}
}

func TestOpenReturnsRecreateGuidanceForDirtyMigrations(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	raw, err := sql.Open("sqlite", path)
	require.NoError(err)
	_, err = raw.Exec(
		`CREATE TABLE schema_migrations (version uint64, dirty bool)`,
	)
	require.NoError(err)
	_, err = raw.Exec(
		`INSERT INTO schema_migrations (version, dirty) VALUES (1, TRUE)`,
	)
	require.NoError(err)
	require.NoError(raw.Close())

	_, err = Open(path)
	require.Error(err)
	require.Contains(err.Error(), recreateDatabaseInstruction)
}

func TestOpenRejectsIncompleteLegacyDatabase(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "broken-legacy.db")

	raw, err := sql.Open("sqlite", path)
	require.NoError(err)
	_, err = raw.Exec(`CREATE TABLE middleman_repos (id INTEGER PRIMARY KEY)`)
	require.NoError(err)
	require.NoError(raw.Close())

	_, err = Open(path)
	require.Error(err)
	require.Contains(err.Error(), recreateDatabaseInstruction)
}

func testRejectsUnsupportedLegacySchemaVersion(t *testing.T, version int) {
	t.Helper()
	require := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	raw, err := sql.Open("sqlite", path)
	require.NoError(err)
	_, err = raw.Exec(legacySchemaSQLForTest(t, 3))
	require.NoError(err)
	_, err = raw.Exec(
		`CREATE TABLE middleman_schema_version (version INTEGER NOT NULL)`,
	)
	require.NoError(err)
	_, err = raw.Exec(
		`INSERT INTO middleman_schema_version (version) VALUES (?)`,
		version,
	)
	require.NoError(err)
	require.NoError(raw.Close())

	_, err = Open(path)
	require.Error(err)
	if version == 0 {
		require.Contains(err.Error(), recreateDatabaseInstruction)
		require.Contains(err.Error(), "is invalid")
		return
	}
	require.Contains(err.Error(), "newer than this binary")
}

func legacySchemaSQLForTest(t *testing.T, version int) string {
	t.Helper()
	parts := make([]string, 0, version)
	for i := 1; i <= version; i++ {
		contents, err := fs.ReadFile(
			migrationFiles,
			path.Join("migrations", legacyMigrationFilenameForTest(i)),
		)
		require.NoError(t, err)
		parts = append(parts, string(contents))
	}
	return strings.Join(parts, "\n")
}

func legacyMigrationFilenameForTest(version int) string {
	switch version {
	case 1:
		return "000001_initial_schema.up.sql"
	case 2:
		return "000002_update_mr_events_dedupe.up.sql"
	case 3:
		return "000003_add_backfill_and_detail_columns.up.sql"
	case 4:
		return "000004_drop_legacy_schema_version.up.sql"
	case 5:
		return "000005_graphql_sync_and_labels.up.sql"
	case 6:
		return "000006_add_stacks.up.sql"
	case 7:
		return "000007_add_workspaces.up.sql"
	default:
		return ""
	}
}

func latestMigrationVersionForTest(t *testing.T) int {
	t.Helper()
	version, err := latestMigrationVersion()
	require.NoError(t, err)
	return version
}

func tableExistsForTest(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name,
	).Scan(&count)
	require.NoError(t, err)
	return count > 0
}

func rewriteWorkspacesToVersion11ForTest(raw *sql.DB) error {
	_, err := raw.Exec(`
		DROP TRIGGER IF EXISTS middleman_workspaces_casefold_update;
		DROP TRIGGER IF EXISTS middleman_workspaces_casefold_insert;

		DROP INDEX IF EXISTS middleman_workspace_setup_events_workspace_id_idx;
		ALTER TABLE middleman_workspace_setup_events
			RENAME TO middleman_workspace_setup_events_old;

		ALTER TABLE middleman_workspaces
			RENAME TO middleman_workspaces_v12;

		CREATE TABLE middleman_workspaces (
		    id               TEXT PRIMARY KEY,
		    platform_host    TEXT NOT NULL,
		    repo_owner       TEXT NOT NULL,
		    repo_name        TEXT NOT NULL,
		    mr_number        INTEGER NOT NULL,
		    mr_head_ref      TEXT NOT NULL,
		    mr_head_repo     TEXT,
		    worktree_path    TEXT NOT NULL,
		    tmux_session     TEXT NOT NULL,
		    status           TEXT NOT NULL DEFAULT 'creating',
		    error_message    TEXT,
		    created_at       DATETIME NOT NULL DEFAULT (datetime('now')),
		    workspace_branch TEXT NOT NULL DEFAULT '__middleman_unknown__',
		    UNIQUE(platform_host, repo_owner, repo_name, mr_number)
		);

		INSERT INTO middleman_workspaces (
		    id, platform_host, repo_owner, repo_name,
		    mr_number, mr_head_ref, mr_head_repo,
		    worktree_path, tmux_session, status,
		    error_message, created_at, workspace_branch
		)
		SELECT
		    id, platform_host, repo_owner, repo_name,
		    item_number, git_head_ref, mr_head_repo,
		    worktree_path, tmux_session, status,
		    error_message, created_at, workspace_branch
		FROM middleman_workspaces_v12;

		DROP TABLE middleman_workspaces_v12;

		CREATE TRIGGER middleman_workspaces_casefold_insert
		BEFORE INSERT ON middleman_workspaces
		WHEN NEW.platform_host <> lower(NEW.platform_host)
		  OR NEW.repo_owner <> lower(NEW.repo_owner)
		  OR NEW.repo_name <> lower(NEW.repo_name)
		BEGIN
		    SELECT RAISE(ABORT, 'workspace repo identifiers must be lowercase');
		END;

		CREATE TRIGGER middleman_workspaces_casefold_update
		BEFORE UPDATE OF platform_host, repo_owner, repo_name ON middleman_workspaces
		WHEN NEW.platform_host <> lower(NEW.platform_host)
		  OR NEW.repo_owner <> lower(NEW.repo_owner)
		  OR NEW.repo_name <> lower(NEW.repo_name)
		BEGIN
		    SELECT RAISE(ABORT, 'workspace repo identifiers must be lowercase');
		END;

		CREATE TABLE middleman_workspace_setup_events (
		    id           INTEGER PRIMARY KEY AUTOINCREMENT,
		    workspace_id TEXT NOT NULL REFERENCES middleman_workspaces(id) ON DELETE CASCADE,
		    stage        TEXT NOT NULL,
		    outcome      TEXT NOT NULL,
		    message      TEXT NOT NULL,
		    created_at   DATETIME NOT NULL DEFAULT (datetime('now'))
		);

		INSERT INTO middleman_workspace_setup_events (
		    id, workspace_id, stage, outcome, message, created_at
		)
		SELECT
		    id, workspace_id, stage, outcome, message, created_at
		FROM middleman_workspace_setup_events_old;

		DROP TABLE middleman_workspace_setup_events_old;

		CREATE INDEX middleman_workspace_setup_events_workspace_id_idx
		    ON middleman_workspace_setup_events (workspace_id, id);
	`)
	return err
}

func rewriteWorkspacesToVersion9ForTest(raw *sql.DB) error {
	_, err := raw.Exec(`
		DROP TRIGGER IF EXISTS middleman_workspaces_casefold_update;
		DROP TRIGGER IF EXISTS middleman_workspaces_casefold_insert;

		DROP INDEX IF EXISTS middleman_workspace_setup_events_workspace_id_idx;
		DROP TABLE IF EXISTS middleman_workspace_setup_events;

		ALTER TABLE middleman_workspaces
			RENAME TO middleman_workspaces_v11;

		CREATE TABLE middleman_workspaces (
		    id            TEXT PRIMARY KEY,
		    platform_host TEXT NOT NULL,
		    repo_owner    TEXT NOT NULL,
		    repo_name     TEXT NOT NULL,
		    mr_number     INTEGER NOT NULL,
		    mr_head_ref   TEXT NOT NULL,
		    mr_head_repo  TEXT,
		    worktree_path TEXT NOT NULL,
		    tmux_session  TEXT NOT NULL,
		    status        TEXT NOT NULL DEFAULT 'creating',
		    error_message TEXT,
		    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
		    UNIQUE(platform_host, repo_owner, repo_name, mr_number)
		);

		INSERT INTO middleman_workspaces (
		    id, platform_host, repo_owner, repo_name,
		    mr_number, mr_head_ref, mr_head_repo,
		    worktree_path, tmux_session, status,
		    error_message, created_at
		)
		SELECT
		    id, platform_host, repo_owner, repo_name,
		    item_number, git_head_ref, mr_head_repo,
		    worktree_path, tmux_session, status,
		    error_message, created_at
		FROM middleman_workspaces_v11;

		DROP TABLE middleman_workspaces_v11;

		CREATE TRIGGER middleman_workspaces_casefold_insert
		BEFORE INSERT ON middleman_workspaces
		WHEN NEW.platform_host <> lower(NEW.platform_host)
		  OR NEW.repo_owner <> lower(NEW.repo_owner)
		  OR NEW.repo_name <> lower(NEW.repo_name)
		BEGIN
		    SELECT RAISE(ABORT, 'workspace repo identifiers must be lowercase');
		END;

		CREATE TRIGGER middleman_workspaces_casefold_update
		BEFORE UPDATE OF platform_host, repo_owner, repo_name ON middleman_workspaces
		WHEN NEW.platform_host <> lower(NEW.platform_host)
		  OR NEW.repo_owner <> lower(NEW.repo_owner)
		  OR NEW.repo_name <> lower(NEW.repo_name)
		BEGIN
		    SELECT RAISE(ABORT, 'workspace repo identifiers must be lowercase');
		END;
	`)
	return err
}

func removeProviderIdentityColumnsForTest(raw *sql.DB) error {
	_, err := raw.Exec(`
		DROP TRIGGER IF EXISTS middleman_repos_casefold_update;
		DROP TRIGGER IF EXISTS middleman_repos_casefold_insert;
		DROP INDEX IF EXISTS idx_issue_events_platform_external_id;
		DROP INDEX IF EXISTS idx_mr_events_platform_external_id;
		DROP INDEX IF EXISTS idx_labels_repo_platform_external_id;
		DROP INDEX IF EXISTS idx_issues_repo_platform_external_id;
		DROP INDEX IF EXISTS idx_merge_requests_repo_platform_external_id;
		DROP INDEX IF EXISTS idx_repos_provider_path_key;
		DROP INDEX IF EXISTS idx_repos_platform_repo_id;
		DROP INDEX IF EXISTS idx_labels_repo_catalog_name;
		ALTER TABLE middleman_repos DROP COLUMN label_catalog_sync_error;
		ALTER TABLE middleman_repos DROP COLUMN label_catalog_checked_at;
		ALTER TABLE middleman_repos DROP COLUMN label_catalog_synced_at;
		ALTER TABLE middleman_repos DROP COLUMN viewer_can_merge;
		ALTER TABLE middleman_labels DROP COLUMN catalog_seen_at;
		ALTER TABLE middleman_labels DROP COLUMN catalog_present;
		ALTER TABLE middleman_mr_events DROP COLUMN platform_external_id;
		ALTER TABLE middleman_labels DROP COLUMN platform_external_id;
		ALTER TABLE middleman_issues DROP COLUMN platform_external_id;
		ALTER TABLE middleman_merge_requests DROP COLUMN platform_external_id;
		ALTER TABLE middleman_repos DROP COLUMN default_branch;
		ALTER TABLE middleman_repos DROP COLUMN clone_url;
		ALTER TABLE middleman_repos DROP COLUMN web_url;
		ALTER TABLE middleman_repos DROP COLUMN repo_path_key;
		ALTER TABLE middleman_repos DROP COLUMN name_key;
		ALTER TABLE middleman_repos DROP COLUMN owner_key;
		ALTER TABLE middleman_repos DROP COLUMN repo_path;
		ALTER TABLE middleman_repos DROP COLUMN platform_repo_id;

		CREATE TRIGGER middleman_repos_casefold_insert
		BEFORE INSERT ON middleman_repos
		WHEN NEW.platform_host <> lower(NEW.platform_host)
		  OR NEW.owner <> lower(NEW.owner)
		  OR NEW.name <> lower(NEW.name)
		BEGIN
		    SELECT RAISE(ABORT, 'repo identifiers must be lowercase');
		END;

		CREATE TRIGGER middleman_repos_casefold_update
		BEFORE UPDATE OF platform_host, owner, name ON middleman_repos
		WHEN NEW.platform_host <> lower(NEW.platform_host)
		  OR NEW.owner <> lower(NEW.owner)
		  OR NEW.name <> lower(NEW.name)
		BEGIN
		    SELECT RAISE(ABORT, 'repo identifiers must be lowercase');
		END;
	`)
	return err
}

func removeMergeRequestLockedColumnForTest(raw *sql.DB) error {
	_, err := raw.Exec(`ALTER TABLE middleman_merge_requests DROP COLUMN is_locked`)
	return err
}

func removeWorkflowApprovalColumnsForTest(raw *sql.DB) error {
	for _, col := range []string{
		"workflow_approval_checked_at",
		"workflow_approval_head_sha",
		"workflow_approval_required",
		"workflow_approval_count",
	} {
		if _, err := raw.Exec(
			"ALTER TABLE middleman_merge_requests DROP COLUMN " + col,
		); err != nil {
			return err
		}
	}
	return nil
}

func removeDiscussionColumnsForTest(raw *sql.DB) error {
	_, err := raw.Exec(`
		DROP INDEX IF EXISTS idx_mr_events_thread;
		ALTER TABLE middleman_mr_events DROP COLUMN thread_id;
		ALTER TABLE middleman_mr_events DROP COLUMN position_json;
		ALTER TABLE middleman_mr_events DROP COLUMN resolvable;
		ALTER TABLE middleman_mr_events DROP COLUMN resolved;
		ALTER TABLE middleman_issue_events DROP COLUMN thread_id;
	`)
	return err
}

func removeIssueAssigneesColumnForTest(raw *sql.DB) error {
	_, err := raw.Exec(`ALTER TABLE middleman_issues DROP COLUMN assignees_json`)
	return err
}

func openSchemaVersion4DBForTest(t *testing.T) (string, *sql.DB) {
	t.Helper()
	require := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	raw, err := sql.Open("sqlite", path)
	require.NoError(err)
	_, err = raw.Exec(legacySchemaSQLForTest(t, 4))
	require.NoError(err)
	_, err = raw.Exec(`CREATE TABLE schema_migrations (version uint64, dirty bool)`)
	require.NoError(err)
	_, err = raw.Exec(`INSERT INTO schema_migrations (version, dirty) VALUES (4, FALSE)`)
	require.NoError(err)
	_, err = raw.Exec(
		`INSERT INTO middleman_repos (
			id, platform, platform_host, owner, name,
			created_at, backfill_pr_page, backfill_pr_complete,
			backfill_issue_page, backfill_issue_complete
		) VALUES (?, 'github', 'github.com', 'octo', 'repo', datetime('now'), 0, 0, 0, 0)`,
		1,
	)
	require.NoError(err)

	return path, raw
}

func seedLegacyIssueForTest(
	t *testing.T,
	raw *sql.DB,
	id int,
	repoID int,
	platformID int,
	number int,
	labelsJSON string,
) {
	t.Helper()
	_, err := raw.Exec(
		`INSERT INTO middleman_issues (
			id, repo_id, platform_id, number, url, title, author, state,
			body, comment_count, labels_json, created_at, updated_at, last_activity_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'), datetime('now'))`,
		id,
		repoID,
		platformID,
		number,
		"https://github.com/octo/repo/issues/test",
		"Backfill labels",
		"octocat",
		"open",
		"",
		0,
		labelsJSON,
	)
	require.NoError(t, err)
}
