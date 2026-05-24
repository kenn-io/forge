package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	branchCommitIdentityMaxBytes = 256
	branchCommitSubjectMaxBytes  = 512
)

func (d *DB) UpsertBranchCommits(
	ctx context.Context,
	commits []BranchCommit,
) error {
	if len(commits) == 0 {
		return nil
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		observedBase := time.Now().UTC().UnixNano()
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO middleman_branch_commits (
			    repo_id, branch_name, commit_sha, author_name, author_email,
			    authored_at, committer_name, committer_email, committed_at,
			    subject, observed_order
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(repo_id, branch_name, commit_sha) DO UPDATE SET
			    author_name     = excluded.author_name,
			    author_email    = excluded.author_email,
			    authored_at     = excluded.authored_at,
			    committer_name  = excluded.committer_name,
			    committer_email = excluded.committer_email,
			    committed_at    = excluded.committed_at,
			    subject         = excluded.subject,
			    observed_order  = excluded.observed_order,
			    updated_at      = datetime('now')`)
		if err != nil {
			return fmt.Errorf("prepare upsert branch commits: %w", err)
		}
		defer stmt.Close()

		for i := range commits {
			commit := &commits[i]
			canonicalizeBranchCommitTimestamps(commit)
			canonicalizeBranchCommitMetadata(commit)
			if commit.ObservedOrder == 0 {
				commit.ObservedOrder = observedBase - int64(i)
			}
			if _, err := stmt.ExecContext(ctx,
				commit.RepoID,
				commit.BranchName,
				commit.CommitSHA,
				commit.AuthorName,
				commit.AuthorEmail,
				commit.AuthoredAt,
				commit.CommitterName,
				commit.CommitterEmail,
				commit.CommittedAt,
				commit.Subject,
				commit.ObservedOrder,
			); err != nil {
				return fmt.Errorf(
					"upsert branch commit (repo_id=%d sha=%s): %w",
					commit.RepoID,
					commit.CommitSHA,
					err,
				)
			}
		}
		return nil
	})
}

func (d *DB) GetBranchTip(
	ctx context.Context,
	repoID int64,
	branch string,
) (*BranchTip, error) {
	var tip BranchTip
	var observedAt string
	var createdAt string
	var updatedAt string
	err := d.ro.QueryRowContext(ctx, `
		SELECT repo_id, branch_name, tip_sha, observed_at, created_at, updated_at
		FROM middleman_branch_tips
		WHERE repo_id = ? AND branch_name = ?`,
		repoID,
		branch,
	).Scan(
		&tip.RepoID,
		&tip.BranchName,
		&tip.TipSHA,
		&observedAt,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf(
			"get branch tip (repo_id=%d branch=%s): %w",
			repoID,
			branch,
			err,
		)
	}
	tip.ObservedAt, err = parseDBTime(observedAt)
	if err != nil {
		return nil, fmt.Errorf("parse branch tip observed_at: %w", err)
	}
	tip.CreatedAt, err = parseDBTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse branch tip created_at: %w", err)
	}
	tip.UpdatedAt, err = parseDBTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse branch tip updated_at: %w", err)
	}
	return &tip, nil
}

func (d *DB) UpsertBranchTip(ctx context.Context, tip BranchTip) error {
	canonicalizeBranchTipTimestamps(&tip)
	_, err := d.rw.ExecContext(ctx, `
		INSERT INTO middleman_branch_tips (
		    repo_id, branch_name, tip_sha, observed_at
		)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(repo_id, branch_name) DO UPDATE SET
		    tip_sha     = excluded.tip_sha,
		    observed_at = excluded.observed_at,
		    updated_at  = datetime('now')`,
		tip.RepoID,
		tip.BranchName,
		tip.TipSHA,
		tip.ObservedAt,
	)
	if err != nil {
		return fmt.Errorf(
			"upsert branch tip (repo_id=%d branch=%s): %w",
			tip.RepoID,
			tip.BranchName,
			err,
		)
	}
	return nil
}

func (d *DB) InsertBranchForcePush(
	ctx context.Context,
	fp BranchForcePush,
) error {
	canonicalizeBranchForcePushTimestamps(&fp)
	_, err := d.rw.ExecContext(ctx, `
		INSERT INTO middleman_branch_force_pushes (
		    repo_id, branch_name, before_sha, after_sha, before_observed_at,
		    detected_at
		)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, branch_name, before_sha, after_sha, before_observed_at) DO NOTHING`,
		fp.RepoID,
		fp.BranchName,
		fp.BeforeSHA,
		fp.AfterSHA,
		fp.BeforeObservedAt,
		fp.DetectedAt,
	)
	if err != nil {
		return fmt.Errorf(
			"insert branch force push (repo_id=%d branch=%s before=%s after=%s): %w",
			fp.RepoID,
			fp.BranchName,
			fp.BeforeSHA,
			fp.AfterSHA,
			err,
		)
	}
	return nil
}

func (d *DB) PruneBranchActivity(
	ctx context.Context,
	before time.Time,
	maxCommitsPerBranch int,
) error {
	before = canonicalUTCTime(before)
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM middleman_branch_commits
			WHERE committed_at < ?`,
			before,
		); err != nil {
			return fmt.Errorf("prune branch commits: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM middleman_branch_force_pushes
			WHERE detected_at < ?`,
			before,
		); err != nil {
			return fmt.Errorf("prune branch force pushes: %w", err)
		}
		if maxCommitsPerBranch > 0 {
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM middleman_branch_commits
				WHERE id IN (
				    SELECT id
				    FROM (
				        SELECT id,
				               ROW_NUMBER() OVER (
				                   PARTITION BY repo_id, branch_name
				                   ORDER BY committed_at DESC, observed_order DESC, id DESC
				               ) AS rn
				        FROM middleman_branch_commits
				    )
				    WHERE rn > ?
				)`,
				maxCommitsPerBranch,
			); err != nil {
				return fmt.Errorf("cap branch commits: %w", err)
			}
		}
		return nil
	})
}

func canonicalizeBranchCommitTimestamps(commit *BranchCommit) {
	if commit == nil {
		return
	}
	commit.AuthoredAt = canonicalUTCTime(commit.AuthoredAt)
	commit.CommittedAt = canonicalUTCTime(commit.CommittedAt)
}

func canonicalizeBranchCommitMetadata(commit *BranchCommit) {
	if commit == nil {
		return
	}
	commit.AuthorName = truncateUTF8Bytes(commit.AuthorName, branchCommitIdentityMaxBytes)
	commit.AuthorEmail = truncateUTF8Bytes(commit.AuthorEmail, branchCommitIdentityMaxBytes)
	commit.CommitterName = truncateUTF8Bytes(commit.CommitterName, branchCommitIdentityMaxBytes)
	commit.CommitterEmail = truncateUTF8Bytes(commit.CommitterEmail, branchCommitIdentityMaxBytes)
	commit.Subject = truncateUTF8Bytes(commit.Subject, branchCommitSubjectMaxBytes)
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	s = strings.ToValidUTF8(s, "")
	if len(s) <= maxBytes {
		return s
	}
	truncated := s[:maxBytes]
	for !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

func canonicalizeBranchTipTimestamps(tip *BranchTip) {
	if tip == nil {
		return
	}
	tip.ObservedAt = canonicalUTCTime(tip.ObservedAt)
}

func canonicalizeBranchForcePushTimestamps(fp *BranchForcePush) {
	if fp == nil {
		return
	}
	if fp.BeforeObservedAt.IsZero() {
		fp.BeforeObservedAt = fp.DetectedAt
	}
	fp.BeforeObservedAt = canonicalUTCTime(fp.BeforeObservedAt)
	fp.DetectedAt = canonicalUTCTime(fp.DetectedAt)
}
