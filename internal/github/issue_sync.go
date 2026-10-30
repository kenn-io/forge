package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"go.kenn.io/forge/internal/platformdb"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// --- Issue sync ---

// syncIssuesFromList processes a pre-fetched list of open issues
// via the REST path. Handles per-issue upsert and closure detection.
func (s *Syncer) syncIssuesFromList(
	ctx context.Context,
	client Client,
	repo RepoRef,
	repoID int64,
	ghIssues []*gh.Issue,
	forceRefresh bool,
) error {
	stillOpen := make(map[int]bool, len(ghIssues))
	for _, issue := range ghIssues {
		stillOpen[issue.GetNumber()] = true
	}

	var hadItemFailure bool
	var failureCause error
	progress := newIssueSyncProgressLogger(repo, "rest", len(ghIssues))
	for i, ghIssue := range ghIssues {
		if err := s.syncOpenIssue(ctx, client, repo, repoID, ghIssue, forceRefresh); err != nil {
			if disabledErr := repositoryFeatureDisabledError(
				repo, platform.RepositoryFeatureIssues, err,
			); disabledErr != nil {
				return preservePartialSyncFailure(
					failIssues, disabledErr, failureCause,
				)
			}
			failureCause = retainSyncFailureCause(failureCause, err)
			logSyncFailure(ctx, slog.LevelError, "sync issue failed", err,
				"repo", repo.Owner+"/"+repo.Name,
				"number", ghIssue.GetNumber(),
			)
			hadItemFailure = true
		}
		progress.record(i + 1)
	}

	closedNumbers, err := s.db.GetPreviouslyOpenIssueNumbers(
		ctx, repoID, stillOpen,
	)
	if err != nil {
		return joinPartialFailureCause(
			failureCause, fmt.Errorf("get previously open issues: %w", err),
		)
	}
	for _, number := range closedNumbers {
		if err := s.fetchAndUpdateClosedIssue(
			ctx, repo, repoID, number,
		); err != nil {
			if disabledErr := repositoryFeatureDisabledError(
				repo, platform.RepositoryFeatureIssues, err,
			); disabledErr != nil {
				return preservePartialSyncFailure(
					failIssues, disabledErr, failureCause,
				)
			}
			failureCause = retainSyncFailureCause(failureCause, err)
			logSyncFailure(ctx, slog.LevelError, "update closed issue failed", err,
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
			)
			hadItemFailure = true
		}
	}

	if hadItemFailure {
		return partialItemFailureError("one or more issue sync items failed", failureCause)
	}
	progress.done()
	return nil
}

func (s *Syncer) syncPlatformIssuesFromList(
	ctx context.Context,
	reader platform.IssueReader,
	repo RepoRef,
	repoID int64,
	issues []platform.Issue,
	forceRefresh bool,
) error {
	stillOpen := make(map[int]bool, len(issues))
	for _, issue := range issues {
		stillOpen[issue.Number] = true
	}

	var hadItemFailure bool
	var failureCause error
	progress := newIssueSyncProgressLogger(repo, "provider", len(issues))
	for i, issue := range issues {
		if err := s.syncOpenPlatformIssue(ctx, reader, repo, repoID, issue, forceRefresh); err != nil {
			if errors.Is(err, platform.ErrRepositoryFeatureDisabled) {
				return preservePartialSyncFailure(
					failIssues, err, failureCause,
				)
			}
			failureCause = retainSyncFailureCause(failureCause, err)
			logSyncFailure(ctx, slog.LevelError, "sync issue failed", err,
				"repo", repo.Owner+"/"+repo.Name,
				"number", issue.Number,
			)
			hadItemFailure = true
		}
		progress.record(i + 1)
	}

	closedNumbers, err := s.db.GetPreviouslyOpenIssueNumbers(
		ctx, repoID, stillOpen,
	)
	if err != nil {
		return joinPartialFailureCause(
			failureCause, fmt.Errorf("get previously open issues: %w", err),
		)
	}
	for _, number := range closedNumbers {
		if err := s.fetchAndUpdateClosedPlatformIssue(
			ctx, repo, repoID, number,
		); err != nil {
			if errors.Is(err, platform.ErrRepositoryFeatureDisabled) {
				return preservePartialSyncFailure(
					failIssues, err, failureCause,
				)
			}
			failureCause = retainSyncFailureCause(failureCause, err)
			logSyncFailure(ctx, slog.LevelError, "update closed issue failed", err,
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
			)
			hadItemFailure = true
		}
	}

	if hadItemFailure {
		return partialItemFailureError("one or more issue sync items failed", failureCause)
	}
	progress.done()
	return nil
}

func (s *Syncer) syncOpenPlatformIssue(
	ctx context.Context,
	reader platform.IssueReader,
	repo RepoRef,
	repoID int64,
	issue platform.Issue,
	forceRefresh bool,
) error {
	normalized := platformdb.DBIssue(repoID, issue)

	existing, err := s.db.GetIssueByRepoIDAndNumber(
		ctx, repoID, issue.Number,
	)
	if err != nil {
		return fmt.Errorf(
			"get existing issue #%d: %w", issue.Number, err,
		)
	}

	needsTimeline := forceRefresh || existing == nil ||
		!existing.UpdatedAt.Equal(normalized.UpdatedAt)

	issueID, revision, accepted, err := s.commitIssueParentSnapshot(ctx, normalized)
	if err != nil {
		return fmt.Errorf(
			"upsert issue #%d: %w", issue.Number, err,
		)
	}
	if !accepted {
		return nil
	}

	if !needsTimeline {
		if existing != nil && existing.DetailFetchedAt != nil {
			s.queueIssueCommentSync(repo, existing.RepoID, existing.Number, existing.DetailFetchedAt)
		}
		return nil
	}

	events, err := reader.ListIssueEvents(ctx, platformRepoRef(repo), issue.Number)
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) {
			return nil
		}
		return fmt.Errorf("list issue events for #%d: %w", issue.Number, err)
	}
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
	applied, err := s.commitIssueCommentsSnapshot(ctx, repo, issueID, issue.Number, revision, comments, dbEvents, nil)
	if err != nil {
		return fmt.Errorf("replace issue comments for #%d: %w", issue.Number, err)
	}
	if !applied {
		return nil
	}
	return nil
}

func (s *Syncer) syncOpenIssue(
	ctx context.Context,
	client Client,
	repo RepoRef,
	repoID int64,
	ghIssue *gh.Issue,
	forceRefresh bool,
) error {
	normalized, err := NormalizeIssue(repoID, ghIssue)
	if err != nil {
		return fmt.Errorf("normalize issue #%d: %w", ghIssue.GetNumber(), err)
	}

	existing, err := s.db.GetIssueByRepoIDAndNumber(
		ctx, repoID, ghIssue.GetNumber(),
	)
	if err != nil {
		return fmt.Errorf(
			"get existing issue #%d: %w", ghIssue.GetNumber(), err,
		)
	}

	needsTimeline := forceRefresh || existing == nil ||
		!existing.UpdatedAt.Equal(normalized.UpdatedAt)

	issueID, revision, accepted, err := s.commitIssueParentSnapshot(ctx, normalized)
	if err != nil {
		return fmt.Errorf(
			"upsert issue #%d: %w", ghIssue.GetNumber(), err,
		)
	}
	if !accepted {
		return nil
	}

	if !needsTimeline {
		if existing != nil && existing.DetailFetchedAt != nil {
			s.queueIssueCommentSync(repo, existing.RepoID, existing.Number, existing.DetailFetchedAt)
		}
		return nil
	}

	if err := s.refreshIssueTimeline(ctx, repo, issueID, revision, ghIssue, nil); err != nil {
		if errors.Is(err, errParentSnapshotAdvanced) {
			return nil
		}
		return err
	}
	return nil
}

func (s *Syncer) refreshIssueTimeline(
	ctx context.Context,
	repo RepoRef,
	issueID int64,
	expectedRevision int64,
	ghIssue *gh.Issue,
	visibility map[int64]platformgithub.CommentVisibility,
) error {
	if ghIssue == nil {
		return errors.New("nil issue")
	}
	number := ghIssue.GetNumber()
	client, err := s.clientFor(repo)
	if err != nil {
		return fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}

	comments, err := client.ListIssueComments(
		ctx, repo.Owner, repo.Name, number,
	)
	if err != nil {
		return fmt.Errorf("list comments for issue #%d: %w", number, err)
	}
	if visibility == nil {
		visibility, err = s.storedIssueCommentVisibility(ctx, issueID)
		if err != nil {
			return fmt.Errorf("load stored comment visibility for issue #%d: %w", number, err)
		}
		if observed, ok := s.currentIssueCommentVisibility(ctx, repo, number); ok {
			visibility = observed
		}
	} else {
		storedVisibility, err := s.storedIssueCommentVisibility(ctx, issueID)
		if err != nil {
			return fmt.Errorf("load stored comment visibility for issue #%d: %w", number, err)
		}
		maps.Copy(storedVisibility, visibility)
		visibility = storedVisibility
	}

	derived := db.IssueDerivedFields{
		CommentCount:   len(comments),
		LastActivityAt: computeIssueCommentLastActivity(ghIssue, comments),
	}
	var otherEvents []db.IssueEvent
	var timelineErr error
	if timelineClient, ok := client.(issueTimelineLister); ok {
		timelineEvents, err := timelineClient.ListIssueTimelineEvents(
			ctx, repo.Owner, repo.Name, number,
		)
		if err != nil {
			if disabledErr := repositoryFeatureDisabledError(
				repo, platform.RepositoryFeatureIssues, err,
			); disabledErr != nil {
				return disabledErr
			}
			timelineErr = fmt.Errorf("list timeline events for issue #%d: %w", number, err)
			if IsArchiveSyncBudgetContext(ctx) {
				return timelineErr
			}
			logSyncFailure(ctx, slog.LevelWarn, "issue timeline event fetch failed during timeline refresh", err,
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
			)
		} else {
			otherEvents = normalizeIssueTimelineEvents(issueID, timelineEvents)
		}
	}
	applied, err := s.replaceIssueCommentEvents(
		ctx, repo, number, issueID, expectedRevision, comments, otherEvents, &derived, visibility,
	)
	if err != nil {
		return fmt.Errorf("replace issue events for #%d: %w", number, err)
	}
	if !applied {
		return errParentSnapshotAdvanced
	}
	if syncBudgetWait(timelineErr) {
		return timelineErr // keep the comments but leave the issue pending until the timeline fits the budget
	}
	return nil
}

func normalizeIssueTimelineEvents(
	issueID int64,
	timelineEvents []platformgithub.PullRequestTimelineEvent,
) []db.IssueEvent {
	events := make([]db.IssueEvent, 0, len(timelineEvents))
	for _, timelineEvent := range timelineEvents {
		event := NormalizeIssueTimelineEvent(issueID, timelineEvent)
		if event == nil {
			continue
		}
		events = append(events, *event)
	}
	return events
}

func (s *Syncer) fetchAndUpdateClosedIssue(
	ctx context.Context, repo RepoRef, repoID int64, number int,
) error {
	removed, err := s.db.IsArchiveItemRemovedUpstream(
		ctx, repoID, db.ArchiveItemTypeIssue, number,
	)
	if err != nil {
		return fmt.Errorf("check closed issue #%d visibility: %w", number, err)
	}
	if removed {
		return nil
	}
	client, err := s.clientFor(repo)
	if err != nil {
		return fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	ghIssue, err := client.GetIssue(
		ctx, repo.Owner, repo.Name, number,
	)
	// Route fetch failures and detected transfers through the canonical
	// lookup classification so removed, inaccessible, and moved items
	// surface typed outcomes instead of generic upstream failures.
	if outcomeErr := s.issueOnlyFetchOutcomeError(ctx, repo, number, ghIssue, err); outcomeErr != nil {
		// A not_found without a destination is a true removal and gets a
		// terminal local state. A transfer carries the destination and
		// keeps failing the cycle so the maintainer sees the moved item
		// in repo sync health instead of it silently closing here.
		if errors.Is(outcomeErr, platform.ErrNotFound) &&
			lookupDestination(outcomeErr) == nil {
			return s.tombstoneRemovedIssue(ctx, repo, repoID, number)
		}
		return fmt.Errorf("get closed issue #%d: %w", number, outcomeErr)
	}
	if err != nil {
		return fmt.Errorf("get closed issue #%d: %w", number, err)
	}
	if ghIssue == nil {
		return fmt.Errorf("get closed issue #%d: client returned nil issue", number)
	}

	normalized, err := NormalizeIssue(repoID, ghIssue)
	if err != nil {
		return fmt.Errorf("normalize closed issue #%d: %w", number, err)
	}
	if existing, getErr := s.db.GetIssueByRepoIDAndNumber(ctx, repoID, number); getErr != nil {
		return fmt.Errorf("get closed issue #%d: %w", number, getErr)
	} else if existing != nil {
		normalized.DetailFetchedAt = existing.DetailFetchedAt
		normalized.Starred = existing.Starred
	}
	if _, _, accepted, commitErr := s.commitIssueParentSnapshot(ctx, normalized); commitErr != nil {
		return fmt.Errorf("commit closed issue #%d: %w", number, commitErr)
	} else if !accepted {
		return nil
	}

	return s.markClosedLinkedIssueNotificationsDone(ctx, repoID, number)
}

// lookupDestination extracts the transfer destination from a typed lookup
// error, or nil when the error carries none (a true removal).
func lookupDestination(err error) *platform.RepoRef {
	if pErr, ok := errors.AsType[*platform.Error](err); ok && pErr != nil {
		return pErr.Destination
	}
	return nil
}

// tombstoneRemovedIssue closes the local copy of a previously-open issue
// whose provider lookup is explicitly classified as not present (deleted or
// transferred upstream). Without a terminal state the number stays
// "previously open" forever: every cycle re-fetches it, fails the repo's
// issue sync, and spends a lookup plus repository probe on an item that can
// never resolve.
func (s *Syncer) tombstoneRemovedIssue(
	ctx context.Context, repo RepoRef, repoID int64, number int,
) error {
	existing, err := s.db.GetIssueByRepoIDAndNumber(ctx, repoID, number)
	if err != nil {
		return fmt.Errorf("get removed issue #%d: %w", number, err)
	}
	if existing == nil || existing.State != "open" {
		return nil
	}
	now := time.Now().UTC()
	tombstone := *existing
	tombstone.State = "closed"
	if tombstone.ClosedAt == nil {
		tombstone.ClosedAt = &now
	}
	// Upstream is gone; there is no detail left to fetch, so keep the
	// detail drain from retrying the lookup.
	if tombstone.DetailFetchedAt == nil {
		tombstone.DetailFetchedAt = &now
	}
	if _, _, _, err := s.commitIssueParentSnapshot(ctx, &tombstone); err != nil {
		return fmt.Errorf("tombstone removed issue #%d: %w", number, err)
	}
	slog.Info("issue removed upstream; closed local copy",
		"repo", repo.Owner+"/"+repo.Name,
		"number", number,
	)
	return nil
}

func (s *Syncer) fetchAndUpdateClosedPlatformIssue(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
) error {
	removed, err := s.db.IsArchiveItemRemovedUpstream(
		ctx, repoID, db.ArchiveItemTypeIssue, number,
	)
	if err != nil {
		return fmt.Errorf("check closed issue #%d visibility: %w", number, err)
	}
	if removed {
		return nil
	}
	issueReader, err := s.issueReaderFor(repo)
	if err != nil {
		return fmt.Errorf("resolve issue reader for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	issue, err := issueReader.GetIssue(ctx, platformRepoRef(repo), number)
	if err != nil {
		// No tombstone here: a bare provider 404 is ambiguous on the
		// neutral path (GitLab hides inaccessible items behind 404), so
		// the row is retained and the failure surfaces as partial. Only
		// GitHub's classified lookup can distinguish a true removal.
		return fmt.Errorf("get closed issue #%d: %w", number, err)
	}
	normalized := platformdb.DBIssue(repoID, issue)
	_, _, accepted, err := s.commitIssueParentSnapshot(ctx, normalized)
	if err != nil {
		return fmt.Errorf("upsert closed issue #%d: %w", number, err)
	}
	if !accepted {
		return nil
	}
	return nil
}

// SyncIssue fetches fresh data for a single issue from GitHub and updates the DB.
// Returns an error if the repo is not in the configured repo list.
func (s *Syncer) SyncIssue(ctx context.Context, owner, name string, number int) error {
	return s.syncIssueWithHost(ctx, owner, name, number, "")
}

// SyncIssueOnHost fetches fresh issue data for a specific tracked host.
func (s *Syncer) SyncIssueOnHost(
	ctx context.Context,
	host, owner, name string,
	number int,
) error {
	return s.syncIssueWithHost(ctx, owner, name, number, host)
}

// IssueSyncOptions selects the lightweight issue-page refresh.
type IssueSyncOptions struct {
	PagePoll bool
}

// SyncIssueOnProvider fetches fresh issue data for a specific configured
// provider host.
func (s *Syncer) SyncIssueOnProvider(
	ctx context.Context,
	kind platform.Kind,
	host, owner, name string,
	number int,
	options ...IssueSyncOptions,
) error {
	repo, ok := s.trackedRepoByIdentity(kind, owner, name, host)
	if !ok {
		host = repoHost(RepoRef{Platform: kind, PlatformHost: host})
		return fmt.Errorf(
			"repo %s/%s on %s/%s is %w",
			owner, name, kind, host, ErrRepoNotTracked,
		)
	}
	repo.Owner = owner
	repo.Name = name
	repo.PlatformHost = repoHost(repo)
	return s.syncIssueForRepo(ctx, repo, number, nil, options...)
}

func (s *Syncer) syncIssueWithHost(
	ctx context.Context,
	owner, name string,
	number int,
	hostHint string,
) error {
	var (
		repo RepoRef
		ok   bool
		err  error
	)
	if hostHint == "" {
		repo, ok, err = s.trackedRepo(owner, name)
	} else {
		repo, ok, err = s.trackedRepoOnHostUnique(owner, name, hostHint)
	}
	if err != nil {
		return err
	}
	if !ok {
		host := hostHint
		if host == "" {
			host = s.hostFor(owner, name)
		}
		return fmt.Errorf(
			"repo %s/%s on %s is %w", owner, name, host, ErrRepoNotTracked,
		)
	}
	repo.Owner = owner
	repo.Name = name
	repo.PlatformHost = repoHost(repo)
	return s.syncIssueForRepo(ctx, repo, number, nil)
}

func (s *Syncer) syncIssueForRepo(
	ctx context.Context,
	repo RepoRef,
	number int,
	providerAttempted *bool,
	options ...IssueSyncOptions,
) error {
	if !IsArchiveSyncBudgetContext(ctx) {
		bucket, err := s.bucketKeyForRepo(repo, false)
		if err != nil {
			return fmt.Errorf("resolve issue credential route for %s/%s: %w", repo.Owner, repo.Name, err)
		}
		releaseProviderWork := s.beginProviderWork(ctx, bucket, archive.PriorityActiveDetail)
		defer releaseProviderWork()
	}

	var repoID int64
	var probe repositoryFeatureProbe
	if len(options) > 0 && options[0].PagePoll {
		// Background sync reconciles repository renames; page polls use
		// the stored identity so only the issue and comments cost reads.
		stored, err := s.db.ResolveActiveRepositoryRoute(ctx, platformdb.DBRepoIdentity(platformRepoRef(repo)))
		if err != nil {
			return err
		}
		if stored == nil {
			return ErrRepoNotTracked
		}
		repoID = stored.Repository.ID
		repo = repoRefFromCatalog(repo, stored.Repository, nil)
		var due bool
		probe, due = s.beginRepositoryFeatureProbe(ctx, repo, platform.RepositoryFeatureIssues)
		if !due {
			return nil
		}
		defer probe.abandon()
	} else {
		resolvedRef, resolvedID, err := s.reconcileRepoForDirectSync(ctx, repo)
		if err != nil {
			return err
		}
		repo, repoID = resolvedRef, resolvedID
	}
	if repo.Archived && !IsArchiveSyncBudgetContext(ctx) {
		// Live detail syncing stops on an archived repo; only archive
		// hydration, which runs under the archive budget, proceeds.
		slog.Debug("skipping issue detail sync for archived repo",
			"repo", repo.Owner+"/"+repo.Name, "number", number,
		)
		return nil
	}

	providerCalls, err := s.fetchIssueDetail(ctx, repo, repoID, number, options...)
	if len(options) > 0 && options[0].PagePoll {
		s.recordGitHubRepositoryFeatureDisabled(repo, platform.RepositoryFeatureIssues, err)
		if providerCalls > 0 {
			probe.release()
		}
	}
	if providerAttempted != nil && providerCalls > 0 {
		*providerAttempted = true
	}
	if err != nil {
		return err
	}
	return s.markClosedLinkedIssueNotificationsDone(ctx, repoID, number)
}
