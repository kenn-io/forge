package db

import (
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBranchActivityPersistence(t *testing.T) {
	t.Run("upserts commits and prunes outside retention", func(t *testing.T) {
		assert := Assert.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")

		err := d.UpsertBranchCommits(ctx, []BranchCommit{
			{
				RepoID:         repoID,
				BranchName:     "main",
				CommitSHA:      "sha-recent-1",
				AuthorName:     "Alice",
				AuthorEmail:    "alice@example.com",
				AuthoredAt:     mustParseTestTime(t, "2024-01-15T06:00:00-05:00"),
				CommitterName:  "Alice Committer",
				CommitterEmail: "alice-committer@example.com",
				CommittedAt:    base,
				Subject:        "initial subject",
			},
			{
				RepoID:         repoID,
				BranchName:     "main",
				CommitSHA:      "sha-recent-2",
				AuthorName:     "Bob",
				AuthorEmail:    "bob@example.com",
				AuthoredAt:     base.Add(2 * time.Minute),
				CommitterName:  "Bob Committer",
				CommitterEmail: "bob-committer@example.com",
				CommittedAt:    base.Add(3 * time.Minute),
				Subject:        "recent subject",
			},
			{
				RepoID:         repoID,
				BranchName:     "main",
				CommitSHA:      "sha-old",
				AuthorName:     "Carol",
				AuthorEmail:    "carol@example.com",
				AuthoredAt:     base.Add(-72 * time.Hour),
				CommitterName:  "Carol Committer",
				CommitterEmail: "carol-committer@example.com",
				CommittedAt:    base.Add(-72 * time.Hour),
				Subject:        "old subject",
			},
			{
				RepoID:         repoID,
				BranchName:     "main",
				CommitSHA:      "sha-recent-1",
				AuthorName:     "Alice Updated",
				AuthorEmail:    "alice-updated@example.com",
				AuthoredAt:     mustParseTestTime(t, "2024-01-15T06:30:00-05:00"),
				CommitterName:  "Alice Updated Committer",
				CommitterEmail: "alice-updated-committer@example.com",
				CommittedAt:    base.Add(time.Minute),
				Subject:        "updated subject",
			},
		})
		require.NoError(t, err)

		err = d.PruneBranchActivity(ctx, base.Add(-24*time.Hour))
		require.NoError(t, err)

		rows := loadTestBranchCommits(t, d, repoID)
		require.Len(t, rows, 2)
		first := rows["sha-recent-1"]
		second := rows["sha-recent-2"]
		assert.Equal("updated subject", first.Subject)
		assert.Equal("Alice Updated", first.AuthorName)
		assert.Equal(base.Add(time.Minute).UTC(), first.CommittedAt)
		assert.Equal(base.Add(-30*time.Minute).UTC(), first.AuthoredAt)
		assert.Equal("recent subject", second.Subject)
		assert.NotContains(rows, "sha-old")
	})

	t.Run("records force pushes idempotently and tracks tips", func(t *testing.T) {
		assert := Assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")

		err := d.UpsertBranchTip(ctx, BranchTip{
			RepoID:     repoID,
			BranchName: "main",
			TipSHA:     "before-sha",
			ObservedAt: mustParseTestTime(t, "2024-01-15T14:00:00+02:00"),
		})
		require.NoError(err)

		err = d.UpsertBranchTip(ctx, BranchTip{
			RepoID:     repoID,
			BranchName: "main",
			TipSHA:     "after-sha",
			ObservedAt: mustParseTestTime(t, "2024-01-15T14:05:00+02:00"),
		})
		require.NoError(err)

		tip, err := d.GetBranchTip(ctx, repoID, "main")
		require.NoError(err)
		require.NotNil(tip)
		assert.Equal(repoID, tip.RepoID)
		assert.Equal("main", tip.BranchName)
		assert.Equal("after-sha", tip.TipSHA)
		assert.Equal(base.Add(5*time.Minute).UTC(), tip.ObservedAt)

		fp := BranchForcePush{
			RepoID:     repoID,
			BranchName: "main",
			BeforeSHA:  "before-sha",
			AfterSHA:   "after-sha",
			DetectedAt: base.Add(6 * time.Minute),
		}
		require.NoError(d.InsertBranchForcePush(ctx, fp))
		require.NoError(d.InsertBranchForcePush(ctx, fp))

		assert.Equal(1, countTestBranchForcePushes(t, d, repoID))
	})

	t.Run("prunes old force pushes outside retention", func(t *testing.T) {
		assert := Assert.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")

		require.NoError(t, d.InsertBranchForcePush(ctx, BranchForcePush{
			RepoID:     repoID,
			BranchName: "main",
			BeforeSHA:  "old-before",
			AfterSHA:   "old-after",
			DetectedAt: base.Add(-72 * time.Hour),
		}))
		require.NoError(t, d.InsertBranchForcePush(ctx, BranchForcePush{
			RepoID:     repoID,
			BranchName: "main",
			BeforeSHA:  "recent-before",
			AfterSHA:   "recent-after",
			DetectedAt: base,
		}))

		err := d.PruneBranchActivity(ctx, base.Add(-24*time.Hour))
		require.NoError(t, err)

		assert.Equal(1, countTestBranchForcePushes(t, d, repoID))
		assert.Equal("recent-after", loadOnlyTestBranchForcePushAfterSHA(t, d, repoID))
	})
}

func loadTestBranchCommits(
	t *testing.T,
	d *DB,
	repoID int64,
) map[string]BranchCommit {
	t.Helper()
	rows, err := d.ro.Query(`
		SELECT repo_id, branch_name, commit_sha, author_name, author_email,
		       authored_at, committer_name, committer_email, committed_at,
		       subject
		FROM middleman_branch_commits
		WHERE repo_id = ?`,
		repoID,
	)
	require.NoError(t, err)
	defer rows.Close()

	commits := make(map[string]BranchCommit)
	for rows.Next() {
		var commit BranchCommit
		var authoredAt string
		var committedAt string
		err := rows.Scan(
			&commit.RepoID,
			&commit.BranchName,
			&commit.CommitSHA,
			&commit.AuthorName,
			&commit.AuthorEmail,
			&authoredAt,
			&commit.CommitterName,
			&commit.CommitterEmail,
			&committedAt,
			&commit.Subject,
		)
		require.NoError(t, err)
		commit.AuthoredAt, err = parseDBTime(authoredAt)
		require.NoError(t, err)
		commit.CommittedAt, err = parseDBTime(committedAt)
		require.NoError(t, err)
		commits[commit.CommitSHA] = commit
	}
	require.NoError(t, rows.Err())
	return commits
}

func mustParseTestTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return parsed
}

func countTestBranchForcePushes(t *testing.T, d *DB, repoID int64) int {
	t.Helper()
	var count int
	err := d.ro.QueryRow(`
		SELECT COUNT(*)
		FROM middleman_branch_force_pushes
		WHERE repo_id = ?`,
		repoID,
	).Scan(&count)
	require.NoError(t, err)
	return count
}

func loadOnlyTestBranchForcePushAfterSHA(
	t *testing.T,
	d *DB,
	repoID int64,
) string {
	t.Helper()
	var afterSHA string
	err := d.ro.QueryRow(`
		SELECT after_sha
		FROM middleman_branch_force_pushes
		WHERE repo_id = ?`,
		repoID,
	).Scan(&afterSHA)
	require.NoError(t, err)
	return afterSHA
}
