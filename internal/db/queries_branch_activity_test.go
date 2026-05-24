package db

import (
	"strings"
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

		err = d.PruneBranchActivity(ctx, base.Add(-24*time.Hour), 0)
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

		lifecycle := loadTestBranchCommitLifecycle(t, d, repoID, "sha-recent-1")
		assert.False(lifecycle.CreatedAt.IsZero())
		assert.False(lifecycle.UpdatedAt.IsZero())
		assert.Condition(func() bool {
			return !lifecycle.UpdatedAt.Before(lifecycle.CreatedAt)
		})
	})

	t.Run("keeps the same commit sha on different tracked branches", func(t *testing.T) {
		assert := Assert.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")

		err := d.UpsertBranchCommits(ctx, []BranchCommit{
			{
				RepoID:         repoID,
				BranchName:     "master",
				CommitSHA:      "shared-sha",
				AuthorName:     "Alice",
				AuthorEmail:    "alice@example.com",
				AuthoredAt:     base,
				CommitterName:  "Alice",
				CommitterEmail: "alice@example.com",
				CommittedAt:    base,
				Subject:        "master subject",
			},
			{
				RepoID:         repoID,
				BranchName:     "main",
				CommitSHA:      "shared-sha",
				AuthorName:     "Alice",
				AuthorEmail:    "alice@example.com",
				AuthoredAt:     base.Add(time.Minute),
				CommitterName:  "Alice",
				CommitterEmail: "alice@example.com",
				CommittedAt:    base.Add(time.Minute),
				Subject:        "main subject",
			},
		})
		require.NoError(t, err)

		rows := loadTestBranchCommitsByBranch(t, d, repoID)
		require.Len(t, rows, 2)
		assert.Equal("master subject", rows["master/shared-sha"].Subject)
		assert.Equal("main subject", rows["main/shared-sha"].Subject)
	})

	t.Run("caps stored commit metadata", func(t *testing.T) {
		assert := Assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")

		require.NoError(d.UpsertBranchCommits(ctx, []BranchCommit{{
			RepoID:         repoID,
			BranchName:     "main",
			CommitSHA:      "long-metadata-sha",
			AuthorName:     strings.Repeat("a", branchCommitIdentityMaxBytes+20),
			AuthorEmail:    strings.Repeat("e", branchCommitIdentityMaxBytes+20),
			AuthoredAt:     base,
			CommitterName:  strings.Repeat("c", branchCommitIdentityMaxBytes+20),
			CommitterEmail: strings.Repeat("m", branchCommitIdentityMaxBytes+20),
			CommittedAt:    base,
			Subject:        strings.Repeat("s", branchCommitSubjectMaxBytes+20),
		}}))

		rows := loadTestBranchCommits(t, d, repoID)
		require.Len(rows, 1)
		commit := rows["long-metadata-sha"]
		assert.Len(commit.AuthorName, branchCommitIdentityMaxBytes)
		assert.Len(commit.AuthorEmail, branchCommitIdentityMaxBytes)
		assert.Len(commit.CommitterName, branchCommitIdentityMaxBytes)
		assert.Len(commit.CommitterEmail, branchCommitIdentityMaxBytes)
		assert.Len(commit.Subject, branchCommitSubjectMaxBytes)
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
		assert.False(tip.CreatedAt.IsZero())
		assert.False(tip.UpdatedAt.IsZero())
		assert.Condition(func() bool {
			return !tip.UpdatedAt.Before(tip.CreatedAt)
		})

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
		forcePushCreatedAt := loadOnlyTestBranchForcePushCreatedAt(t, d, repoID)
		assert.False(forcePushCreatedAt.IsZero())
	})

	t.Run("records repeated force push pairs as distinct observed rewrites", func(t *testing.T) {
		assert := Assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")

		first := BranchForcePush{
			RepoID:     repoID,
			BranchName: "main",
			BeforeSHA:  "sha-a",
			AfterSHA:   "sha-b",
			DetectedAt: base,
		}
		require.NoError(d.InsertBranchForcePush(ctx, first))
		require.NoError(d.InsertBranchForcePush(ctx, first))
		require.NoError(d.InsertBranchForcePush(ctx, BranchForcePush{
			RepoID:     repoID,
			BranchName: "main",
			BeforeSHA:  "sha-b",
			AfterSHA:   "sha-a",
			DetectedAt: base.Add(time.Minute),
		}))
		require.NoError(d.InsertBranchForcePush(ctx, BranchForcePush{
			RepoID:     repoID,
			BranchName: "main",
			BeforeSHA:  "sha-a",
			AfterSHA:   "sha-b",
			DetectedAt: base.Add(2 * time.Minute),
		}))

		assert.Equal(3, countTestBranchForcePushes(t, d, repoID))
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

		err := d.PruneBranchActivity(ctx, base.Add(-24*time.Hour), 0)
		require.NoError(t, err)

		assert.Equal(1, countTestBranchForcePushes(t, d, repoID))
		assert.Equal("recent-after", loadOnlyTestBranchForcePushAfterSHA(t, d, repoID))
	})

	t.Run("caps commits per repo branch", func(t *testing.T) {
		assert := Assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		firstRepo := insertTestRepo(t, d, "alice", "alpha")
		secondRepo := insertTestRepo(t, d, "bob", "beta")

		var commits []BranchCommit
		for i := range 4 {
			suffix := string(rune('0' + i))
			timestamp := base.Add(time.Duration(i) * time.Minute)
			commits = append(commits, BranchCommit{
				RepoID:         firstRepo,
				BranchName:     "main",
				CommitSHA:      "main-sha-" + suffix,
				AuthorName:     "Alice",
				AuthorEmail:    "alice@example.com",
				AuthoredAt:     timestamp,
				CommitterName:  "Alice",
				CommitterEmail: "alice@example.com",
				CommittedAt:    timestamp,
				Subject:        "main subject",
			})
			commits = append(commits, BranchCommit{
				RepoID:         firstRepo,
				BranchName:     "release",
				CommitSHA:      "release-sha-" + suffix,
				AuthorName:     "Alice",
				AuthorEmail:    "alice@example.com",
				AuthoredAt:     timestamp,
				CommitterName:  "Alice",
				CommitterEmail: "alice@example.com",
				CommittedAt:    timestamp,
				Subject:        "release subject",
			})
			commits = append(commits, BranchCommit{
				RepoID:         secondRepo,
				BranchName:     "main",
				CommitSHA:      "second-main-sha-" + suffix,
				AuthorName:     "Bob",
				AuthorEmail:    "bob@example.com",
				AuthoredAt:     timestamp,
				CommitterName:  "Bob",
				CommitterEmail: "bob@example.com",
				CommittedAt:    timestamp,
				Subject:        "second subject",
			})
		}
		require.NoError(d.UpsertBranchCommits(ctx, commits))

		require.NoError(d.PruneBranchActivity(ctx, base.Add(-24*time.Hour), 2))

		firstRows := loadTestBranchCommitsByBranch(t, d, firstRepo)
		secondRows := loadTestBranchCommitsByBranch(t, d, secondRepo)
		require.Len(firstRows, 4)
		require.Len(secondRows, 2)
		assert.Contains(firstRows, "main/main-sha-3")
		assert.Contains(firstRows, "main/main-sha-2")
		assert.NotContains(firstRows, "main/main-sha-1")
		assert.NotContains(firstRows, "main/main-sha-0")
		assert.Contains(firstRows, "release/release-sha-3")
		assert.Contains(firstRows, "release/release-sha-2")
		assert.Contains(secondRows, "main/second-main-sha-3")
		assert.Contains(secondRows, "main/second-main-sha-2")
	})

	t.Run("cap preserves git log order for same timestamp commits", func(t *testing.T) {
		assert := Assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")

		var commits []BranchCommit
		for _, sha := range []string{"newest", "second", "third", "oldest"} {
			commits = append(commits, BranchCommit{
				RepoID:         repoID,
				BranchName:     "main",
				CommitSHA:      sha,
				AuthorName:     "Alice",
				AuthorEmail:    "alice@example.com",
				AuthoredAt:     base,
				CommitterName:  "Alice",
				CommitterEmail: "alice@example.com",
				CommittedAt:    base,
				Subject:        sha,
			})
		}
		require.NoError(d.UpsertBranchCommits(ctx, commits))

		require.NoError(d.PruneBranchActivity(ctx, base.Add(-24*time.Hour), 2))

		rows := loadTestBranchCommits(t, d, repoID)
		require.Len(rows, 2)
		assert.Contains(rows, "newest")
		assert.Contains(rows, "second")
		assert.NotContains(rows, "third")
		assert.NotContains(rows, "oldest")
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

func loadTestBranchCommitsByBranch(
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
		commits[commit.BranchName+"/"+commit.CommitSHA] = commit
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

type testLifecycleTimestamps struct {
	CreatedAt time.Time
	UpdatedAt time.Time
}

func loadTestBranchCommitLifecycle(
	t *testing.T,
	d *DB,
	repoID int64,
	commitSHA string,
) testLifecycleTimestamps {
	t.Helper()
	var createdAt string
	var updatedAt string
	err := d.ro.QueryRow(`
		SELECT created_at, updated_at
		FROM middleman_branch_commits
		WHERE repo_id = ? AND commit_sha = ?`,
		repoID,
		commitSHA,
	).Scan(&createdAt, &updatedAt)
	require.NoError(t, err)
	created, err := parseDBTime(createdAt)
	require.NoError(t, err)
	updated, err := parseDBTime(updatedAt)
	require.NoError(t, err)
	return testLifecycleTimestamps{
		CreatedAt: created,
		UpdatedAt: updated,
	}
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

func loadOnlyTestBranchForcePushCreatedAt(
	t *testing.T,
	d *DB,
	repoID int64,
) time.Time {
	t.Helper()
	var createdAt string
	err := d.ro.QueryRow(`
		SELECT created_at
		FROM middleman_branch_force_pushes
		WHERE repo_id = ?`,
		repoID,
	).Scan(&createdAt)
	require.NoError(t, err)
	parsed, err := parseDBTime(createdAt)
	require.NoError(t, err)
	return parsed
}
