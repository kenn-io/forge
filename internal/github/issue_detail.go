package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"go.kenn.io/forge/internal/platformdb"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// fetchIssueDetail refreshes one issue and returns the number of API calls.
// Page polls refresh comments while leaving full-detail freshness alone.
func (s *Syncer) fetchIssueDetail(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	options ...IssueSyncOptions,
) (int, error) {
	removed, err := s.removedUpstreamForLiveSync(
		ctx, repoID, db.ArchiveItemTypeIssue, number,
	)
	if err != nil {
		return 0, fmt.Errorf("check issue #%d visibility: %w", number, err)
	}
	if removed {
		return 0, nil
	}
	calls := 0
	issueReader, err := s.issueReaderFor(repo)
	if err != nil {
		return calls, fmt.Errorf("resolve issue reader for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	if _, ok := issueReader.(interface {
		GetGitHubIssue(context.Context, platform.RepoRef, int) (*gh.Issue, error)
	}); !ok {
		return s.fetchProviderIssueDetail(
			ctx, issueReader, repo, repoID, number,
		)
	}

	client, err := s.clientFor(repo)
	if err != nil {
		return calls, fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	existing, err := s.db.GetIssueByRepoIDAndNumber(ctx, repoID, number)
	if err != nil {
		return calls, fmt.Errorf("get existing issue #%d: %w", number, err)
	}

	pagePoll := len(options) > 0 && options[0].PagePoll
	etagKind := "issue"
	if pagePoll {
		etagKind = "issue-page"
	}
	ghIssue, newETag, notModified, err := s.getIssueForDetail(
		ctx, client, repo, number, etagKind,
	)
	calls++
	if pagePoll && notModified {
		return calls, nil
	}
	// Route fetch failures and detected transfers through the canonical
	// lookup classification so removed, inaccessible, and moved items
	// surface typed outcomes instead of generic upstream failures.
	if provider, ok := issueReader.(*platformgithub.Provider); ok {
		if outcomeErr := provider.IssueLookupOutcomeError(
			ctx, platformRepoRef(repo), number, ghIssue, err,
		); outcomeErr != nil {
			return calls, fmt.Errorf("get issue #%d: %w", number, outcomeErr)
		}
		if outcomeErr := provider.IssuePullRequestOutcomeError(
			platformRepoRef(repo), number, ghIssue,
		); outcomeErr != nil {
			return calls, fmt.Errorf("get issue #%d: %w", number, outcomeErr)
		}
	}
	if err == nil && ghIssue == nil {
		if notModified {
			if existing == nil {
				return calls, fmt.Errorf("mark unchanged detail fetched for issue #%d: issue is missing", number)
			}
			return s.markUnchangedIssueDetailFetched(
				ctx, repo, number, existing, calls,
			)
		}
		err = errors.New("client returned nil issue")
	}
	if err != nil {
		return calls, fmt.Errorf(
			"get issue #%d: %w", number, err,
		)
	}
	if pagePoll && (existing == nil || ghIssue.GetID() != existing.PlatformID) {
		return calls, nil
	}
	normalized, err := NormalizeIssue(repoID, ghIssue)
	if err != nil {
		return calls, fmt.Errorf("normalize issue #%d: %w", number, err)
	}
	issueID, revision, accepted, err := s.commitIssueParentSnapshot(
		ctx, normalized,
	)
	if err != nil {
		return calls, fmt.Errorf(
			"upsert issue #%d: %w", number, err,
		)
	}
	if !accepted {
		return calls, nil
	}

	if pagePoll {
		// The parent write above replaced the issue's comment-derived activity,
		// and a comments validator cached by another refresh may vouch for
		// comments that refresh never committed, so read them unconditionally.
		comments, err := client.ListIssueComments(ctx, repo.Owner, repo.Name, number)
		calls++
		if err != nil {
			return calls, err
		}
		normalized.ID = issueID
		normalized.SnapshotRevision = revision
		applied, err := s.persistIssueCommentsApplied(ctx, repo, normalized, comments)
		if err != nil {
			client.InvalidateListETagsForRepo(repo.Owner, repo.Name, "comments")
			return calls, err
		}
		if !applied {
			// The comments were not committed, so neither validator may
			// vouch for them; the next poll must fetch both again.
			client.InvalidateListETagsForRepo(repo.Owner, repo.Name, "comments")
			return calls, nil
		}

		if newETag != "" {
			if err := s.db.UpsertHTTPEtag(
				ctx, string(repoPlatform(repo)), repoHost(repo), repo.Owner, repo.Name,
				"issue-page", number, newETag,
			); err != nil {
				slog.Warn("persist issue ETag failed",
					"repo", repo.Owner+"/"+repo.Name,
					"number", number,
					"err", err,
				)
			}
		}

		return calls, nil
	}

	if err := s.refreshIssueTimeline(
		ctx, repo, issueID, revision, ghIssue, nil,
	); err != nil {
		calls++ // comments
		if errors.Is(err, errParentSnapshotAdvanced) {
			return calls, nil
		}
		if errors.Is(err, db.ErrRepositoryIdentityChanged) {
			return calls, nil
		}
		return calls, err
	}
	calls++ // comments

	detailApplied, err := s.db.MarkIssueDetailFetchedSnapshot(
		ctx, issueID, revision,
	)
	if err != nil {
		return calls, fmt.Errorf(
			"mark detail fetched for issue #%d: %w", number, err,
		)
	}
	if !detailApplied {
		return calls, nil
	}

	if newETag != "" {
		if err := s.db.UpsertHTTPEtag(
			ctx, string(repoPlatform(repo)), repoHost(repo), repo.Owner, repo.Name,
			"issue", number, newETag,
		); err != nil {
			slog.Warn("persist issue ETag failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
		}
	}

	return calls, nil
}

func (s *Syncer) markUnchangedIssueDetailFetched(
	ctx context.Context,
	repo RepoRef,
	number int,
	existing *db.Issue,
	calls int,
) (int, error) {
	if existing.State == "open" {
		client, err := s.clientFor(repo)
		if err != nil {
			return calls, err
		}
		comments, err := s.listCommentsForRefresh(ctx, client, repo, number, existing.CommentCount)
		calls++
		if err != nil && !platformgithub.IsNotModified(err) {
			return calls, err
		}
		if err == nil {
			if err := s.persistIssueComments(ctx, repo, existing, comments); err != nil {
				client.InvalidateListETagsForRepo(repo.Owner, repo.Name, "comments")
				return calls, err
			}
		}
	}
	if err := s.refreshStoredIssueCommentVisibility(
		ctx, repo, existing.ID, existing.SnapshotRevision, number,
	); err != nil {
		if errors.Is(err, errParentSnapshotAdvanced) ||
			errors.Is(err, db.ErrRepositoryIdentityChanged) {
			return calls, nil
		}
		return calls, err
	}
	detailApplied, err := s.db.MarkIssueDetailFetchedSnapshot(
		ctx, existing.ID, existing.SnapshotRevision,
	)
	if err != nil {
		return calls, fmt.Errorf(
			"mark unchanged detail fetched for issue #%d: %w", number, err,
		)
	}
	if !detailApplied {
		return calls, nil
	}
	return calls, nil
}

func (s *Syncer) getIssueForDetail(
	ctx context.Context,
	client Client,
	repo RepoRef,
	number int,
	kind string,
) (*gh.Issue, string, bool, error) {
	if IsArchiveSyncBudgetContext(ctx) || platformgithub.UnconditionalRead(ctx) {
		issue, err := client.GetIssue(ctx, repo.Owner, repo.Name, number)
		return issue, "", false, err
	}
	conditional, ok := client.(conditionalIssueGetter)
	if !ok {
		issue, err := client.GetIssue(ctx, repo.Owner, repo.Name, number)
		return issue, "", false, err
	}

	etag, err := s.db.GetHTTPEtag(
		ctx, string(repoPlatform(repo)), repoHost(repo),
		repo.Owner, repo.Name, kind, number,
	)
	if err != nil {
		slog.Warn("load issue ETag failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
			"err", err,
		)
		issue, err := client.GetIssue(ctx, repo.Owner, repo.Name, number)
		return issue, "", false, err
	}
	return conditional.GetIssueIfChanged(
		ctx, repo.Owner, repo.Name, number, etag,
	)
}

func (s *Syncer) fetchProviderIssueDetail(
	ctx context.Context,
	reader platform.IssueReader,
	repo RepoRef,
	repoID int64,
	number int,
) (int, error) {
	calls := 0
	issueReader, err := s.issueReaderFor(repo)
	if err != nil {
		return calls, fmt.Errorf("resolve issue reader for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	issue, err := issueReader.GetIssue(ctx, platformRepoRef(repo), number)
	calls++
	if err != nil {
		return calls, fmt.Errorf(
			"get issue #%d: %w", number,
			s.classifyProviderItemLookupError(ctx, repo, err),
		)
	}

	normalized := platformdb.DBIssue(repoID, issue)
	existing, err := s.db.GetIssueByRepoIDAndNumber(ctx, repoID, number)
	if err != nil {
		return calls, fmt.Errorf("get existing issue #%d: %w", number, err)
	}
	if existing != nil {
		normalized.CommentCount = existing.CommentCount
	}
	issueID, revision, accepted, err := s.commitIssueParentSnapshot(
		ctx, normalized,
	)
	if err != nil {
		return calls, fmt.Errorf(
			"upsert issue #%d: %w", number, err,
		)
	}
	if !accepted {
		return calls, nil
	}
	events, eventsErr := reader.ListIssueEvents(ctx, platformRepoRef(repo), number)
	calls++
	if eventsErr != nil && !errors.Is(eventsErr, platform.ErrUnsupportedCapability) {
		return calls, fmt.Errorf("list issue events for #%d: %w", number, eventsErr)
	}
	if eventsErr == nil {
		comments := make([]db.IssueEvent, 0, len(events))
		dbEvents := make([]db.IssueEvent, 0, len(events))
		for _, event := range events {
			dbEvent := platformdb.DBIssueEvent(issueID, event)
			if dbEvent.EventType == "issue_comment" {
				comments = append(comments, dbEvent)
			} else {
				dbEvents = append(dbEvents, dbEvent)
			}
		}
		applied, commitErr := s.commitIssueCommentsSnapshot(ctx, repo, issueID, number, revision, comments, dbEvents, nil)
		if commitErr != nil {
			if errors.Is(commitErr, db.ErrRepositoryIdentityChanged) {
				return calls, nil
			}
			return calls, fmt.Errorf("replace provider issue comments for #%d: %w", number, commitErr)
		}
		if !applied {
			return calls, nil
		}
	}

	detailApplied, err := s.db.MarkIssueDetailFetchedSnapshot(
		ctx, issueID, revision,
	)
	if err != nil {
		return calls, fmt.Errorf(
			"mark detail fetched for issue #%d: %w", number, err,
		)
	}
	if !detailApplied {
		return calls, nil
	}

	return calls, nil
}
