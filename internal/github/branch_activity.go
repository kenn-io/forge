package github

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
)

const (
	defaultBranchActivityRetention  = 90 * 24 * time.Hour
	defaultBranchActivityMaxCommits = 5000
)

func (s *Syncer) defaultBranchForActivity(ctx context.Context, repoID int64, repo RepoRef) string {
	repoRow, err := s.db.GetRepoByID(ctx, repoID)
	if err != nil {
		slog.Warn("get repo default branch failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return strings.TrimSpace(repo.DefaultBranch)
	}
	if repoRow != nil && strings.TrimSpace(repoRow.DefaultBranch) != "" {
		return strings.TrimSpace(repoRow.DefaultBranch)
	}
	return strings.TrimSpace(repo.DefaultBranch)
}

func (s *Syncer) syncDefaultBranchActivity(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	preferredBranch string,
	previousTip *db.BranchTip,
) {
	if s.clones == nil {
		return
	}
	host := repoHost(repo)
	branch, currentTip, err := s.clones.ResolveDefaultBranch(
		ctx,
		string(repoPlatform(repo)), host,
		repo.Owner,
		repo.Name,
		preferredBranch,
	)
	if errors.Is(err, gitclone.ErrEmptyRepository) {
		return
	}
	if err != nil {
		slog.Warn("resolve default branch activity ref failed",
			"repo", repo.Owner+"/"+repo.Name,
			"branch", preferredBranch,
			"err", err,
		)
		return
	}
	if branch == "" || currentTip == "" {
		slog.Warn("default branch activity skipped: no branch resolved",
			"repo", repo.Owner+"/"+repo.Name,
			"branch", preferredBranch,
		)
		return
	}
	if previousTip == nil || previousTip.BranchName != branch {
		previousTip, err = s.db.GetBranchTip(ctx, repoID, branch)
		if err != nil {
			slog.Warn("get resolved default branch tip failed",
				"repo", repo.Owner+"/"+repo.Name,
				"branch", branch,
				"err", err,
			)
			return
		}
	}

	now := time.Now().UTC()
	retention, maxCommits := s.branchActivityLimits()
	retentionStart := now.Add(-retention)
	afterSHA := ""
	var beforeObservedAt time.Time
	forcePush := false
	if previousTip != nil && previousTip.TipSHA != "" {
		afterSHA = previousTip.TipSHA
		beforeObservedAt = previousTip.ObservedAt
		if previousTip.TipSHA != currentTip {
			ancestor, err := s.clones.IsAncestor(
				ctx,
				string(repoPlatform(repo)), host,
				repo.Owner,
				repo.Name,
				previousTip.TipSHA,
				currentTip,
			)
			switch {
			case errors.Is(err, gitclone.ErrNotFound):
				// SQLite can retain a tip after its object disappears from a
				// rebuilt or pruned clone. Resume bounded history indexing;
				// missing history does not prove a force push.
				afterSHA = ""
			case err != nil:
				slog.Warn("check default branch ancestry failed",
					"repo", repo.Owner+"/"+repo.Name,
					"branch", branch,
					"err", err,
				)
				return
			default:
				forcePush = !ancestor
			}
		}
	}

	gitCommits, err := s.clones.ListBranchCommitsSince(
		ctx,
		string(repoPlatform(repo)), host,
		repo.Owner,
		repo.Name,
		branch,
		retentionStart,
		afterSHA,
		maxCommits,
	)
	if err != nil {
		logSyncFailure(ctx, slog.LevelWarn, "list default branch commits failed", err,
			"repo", repo.Owner+"/"+repo.Name,
			"branch", branch,
		)
		return
	}
	if err := s.db.UpsertBranchCommits(
		ctx,
		dbBranchCommits(repoID, branch, gitCommits),
	); err != nil {
		slog.Warn("upsert default branch commits failed",
			"repo", repo.Owner+"/"+repo.Name,
			"branch", branch,
			"err", err,
		)
		return
	}
	if forcePush {
		if err := s.db.InsertBranchForcePush(ctx, db.BranchForcePush{
			RepoID:           repoID,
			BranchName:       branch,
			BeforeSHA:        afterSHA,
			AfterSHA:         currentTip,
			BeforeObservedAt: beforeObservedAt,
			DetectedAt:       now,
		}); err != nil {
			slog.Warn("insert default branch force push failed",
				"repo", repo.Owner+"/"+repo.Name,
				"branch", branch,
				"err", err,
			)
			return
		}
	}
	if err := s.db.UpsertBranchTip(ctx, db.BranchTip{
		RepoID:     repoID,
		BranchName: branch,
		TipSHA:     currentTip,
		ObservedAt: now,
	}); err != nil {
		slog.Warn("upsert default branch tip failed",
			"repo", repo.Owner+"/"+repo.Name,
			"branch", branch,
			"err", err,
		)
		return
	}
	if err := s.db.PruneBranchActivity(ctx, retentionStart, maxCommits); err != nil {
		slog.Warn("prune default branch activity failed",
			"repo", repo.Owner+"/"+repo.Name,
			"branch", branch,
			"err", err,
		)
	}
}

func dbBranchCommits(
	repoID int64,
	branch string,
	commits []gitclone.Commit,
) []db.BranchCommit {
	out := make([]db.BranchCommit, 0, len(commits))
	for _, commit := range commits {
		out = append(out, db.BranchCommit{
			RepoID:         repoID,
			BranchName:     branch,
			CommitSHA:      commit.SHA,
			AuthorName:     commit.AuthorName,
			AuthorEmail:    commit.AuthorEmail,
			AuthoredAt:     commit.AuthoredAt,
			CommitterName:  commit.CommitterName,
			CommitterEmail: commit.CommitterEmail,
			CommittedAt:    commit.CommittedAt,
			Subject:        commit.Message,
		})
	}
	return out
}
