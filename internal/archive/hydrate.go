package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

const (
	maxArchiveDatasetPages   = 10_000
	maxArchiveDatasetRecords = 50_000
	maxArchiveDatasetBytes   = 16 << 20
)

func (s *Service) hydrateItem(ctx context.Context, repo resolvedRepository, item db.ArchiveItemState) (bool, error) {
	var err error
	switch item.ItemType {
	case db.ArchiveItemTypeIssue:
		err = s.hydrateIssue(ctx, repo, &item)
	case db.ArchiveItemTypeMergeRequest:
		err = s.hydrateMergeRequest(ctx, repo, &item)
	default:
		err = fmt.Errorf("hydrate archive item: invalid item type %q", item.ItemType)
	}
	if err != nil {
		if errors.Is(err, errAdmissionDeferred) {
			return false, err
		}
		var terminal *archiveTerminalLookup
		if errors.As(err, &terminal) {
			return s.commitTerminalLookup(ctx, item, terminal)
		}
		return true, s.recordHydrationFailure(ctx, item, err)
	}
	return true, nil
}

type archiveTerminalLookup struct {
	outcome     platform.ArchiveLookupOutcome
	destination *platform.RepoRef
}

func (e *archiveTerminalLookup) Error() string {
	return fmt.Sprintf("archive item lookup returned %s", e.outcome)
}

// commitTerminalLookup records a removed, moved, or inaccessible provider
// outcome on the archive work row. Archived domain content is retained: a
// deletion upstream stops future refreshes but never erases local history.
func (s *Service) commitTerminalLookup(
	ctx context.Context,
	item db.ArchiveItemState,
	terminal *archiveTerminalLookup,
) (bool, error) {
	lifecycle := db.ArchiveLifecycleStateInaccessible
	if terminal.outcome == platform.ArchiveLookupRemoved || terminal.outcome == platform.ArchiveLookupMoved {
		lifecycle = db.ArchiveLifecycleStateRemovedUpstream
	}
	if err := s.db.MarkArchiveItemTerminal(ctx, db.ArchiveItemTerminal{
		RepoID: item.RepoID, ItemType: item.ItemType, ItemNumber: item.ItemNumber,
		Lifecycle: lifecycle, At: s.now(),
	}); err != nil {
		return true, s.recordHydrationFailure(ctx, item, err)
	}
	if terminal.outcome == platform.ArchiveLookupMoved && terminal.destination != nil {
		destination := platform.DBRepoIdentity(*terminal.destination)
		if err := s.db.QueueArchivePromptByIdentity(ctx, destination, s.now()); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (s *Service) hydrateIssue(ctx context.Context, repo resolvedRepository, item *db.ArchiveItemState) error {
	requestCtx, release, err := s.admit(ctx, repo, 1)
	if err != nil {
		return err
	}
	lookup, err := repo.Reader.GetArchiveIssue(requestCtx, repo.Ref, item.ItemNumber)
	release()
	if err != nil {
		return err
	}
	if lookup.Outcome != platform.ArchiveLookupPresent {
		return &archiveTerminalLookup{
			outcome: lookup.Outcome, destination: lookup.Destination,
		}
	}
	parent := platform.DBIssue(repo.ID, lookup.Item)
	status := db.ArchiveDatasetStatusUnsupported
	issueID, revision, accepted, err := s.db.UpsertArchiveIssueSnapshot(ctx, parent)
	if err != nil {
		return err
	}
	if !accepted {
		return nil
	}
	if archiveSnapshotAdvanced(*item, parent.UpdatedAt) {
		item.CommentsStatus = db.ArchiveDatasetStatusPending
	}
	item.ProviderUpdatedAt = parent.UpdatedAt
	if repo.Capabilities.OrdinaryComments {
		if archiveDatasetMutable(item.CommentsStatus) {
			if err := s.fetchIssueComments(ctx, repo, *item, issueID, revision); err != nil {
				return err
			}
		}
		status = db.ArchiveDatasetStatusComplete
	}
	return s.db.MarkArchiveItemHydrated(ctx, db.ArchiveItemHydration{
		RepoID: repo.ID, ItemType: item.ItemType, ItemNumber: item.ItemNumber,
		DomainRevision:    revision,
		CommentsStatus:    status,
		ReviewsStatus:     db.ArchiveDatasetStatusNotApplicable,
		InlineStatus:      db.ArchiveDatasetStatusNotApplicable,
		MirroredUpdatedAt: parent.UpdatedAt, HydratedAt: s.now(),
	})
}

func (s *Service) hydrateMergeRequest(ctx context.Context, repo resolvedRepository, item *db.ArchiveItemState) error {
	requestCtx, release, err := s.admit(ctx, repo, 1)
	if err != nil {
		return err
	}
	lookup, err := repo.Reader.GetArchiveMergeRequest(requestCtx, repo.Ref, item.ItemNumber)
	release()
	if err != nil {
		return err
	}
	if lookup.Outcome != platform.ArchiveLookupPresent {
		return &archiveTerminalLookup{
			outcome: lookup.Outcome, destination: lookup.Destination,
		}
	}
	parent := platform.DBMergeRequest(repo.ID, lookup.Item)
	statuses := db.ArchiveDatasetStatuses{
		Comments:       datasetTerminalStatus(repo.Capabilities.OrdinaryComments),
		Reviews:        datasetTerminalStatus(repo.Capabilities.SubmittedReviews),
		InlineComments: datasetTerminalStatus(repo.Capabilities.InlineReviewComments),
	}
	mrID, revision, accepted, err := s.db.UpsertArchiveMergeRequestSnapshot(ctx, parent)
	if err != nil {
		return err
	}
	if !accepted {
		return nil
	}
	if archiveSnapshotAdvanced(*item, parent.UpdatedAt) {
		if repo.Capabilities.OrdinaryComments {
			item.CommentsStatus = db.ArchiveDatasetStatusPending
		}
		if repo.Capabilities.SubmittedReviews {
			item.ReviewsStatus = db.ArchiveDatasetStatusPending
		}
		if repo.Capabilities.InlineReviewComments {
			item.InlineCommentsStatus = db.ArchiveDatasetStatusPending
		}
	}
	item.ProviderUpdatedAt = parent.UpdatedAt
	if repo.Capabilities.OrdinaryComments {
		if archiveDatasetMutable(item.CommentsStatus) {
			if err := s.fetchMergeRequestEvents(ctx, repo, *item, mrID, revision, db.ArchiveDatasetComments, "issue_comment", repo.Reader.ListArchiveMergeRequestComments); err != nil {
				return err
			}
		}
	}
	if repo.Capabilities.SubmittedReviews {
		if archiveDatasetMutable(item.ReviewsStatus) {
			if err := s.fetchMergeRequestEvents(ctx, repo, *item, mrID, revision, db.ArchiveDatasetReviews, "review", repo.Reader.ListArchiveSubmittedReviews); err != nil {
				return err
			}
		}
	}
	if repo.Capabilities.InlineReviewComments {
		if archiveDatasetMutable(item.InlineCommentsStatus) {
			if err := s.fetchReviewThreads(ctx, repo, *item, mrID, revision); err != nil {
				return err
			}
		}
	}
	return s.db.MarkArchiveItemHydrated(ctx, db.ArchiveItemHydration{
		RepoID: repo.ID, ItemType: item.ItemType, ItemNumber: item.ItemNumber,
		DomainRevision: revision,
		CommentsStatus: statuses.Comments, ReviewsStatus: statuses.Reviews,
		InlineStatus:      statuses.InlineComments,
		MirroredUpdatedAt: parent.UpdatedAt, HydratedAt: s.now(),
	})
}

func archiveSnapshotAdvanced(item db.ArchiveItemState, updatedAt time.Time) bool {
	return item.HydrationSnapshotUpdatedAt == nil ||
		updatedAt.After(*item.HydrationSnapshotUpdatedAt)
}

func datasetTerminalStatus(supported bool) db.ArchiveDatasetStatus {
	if supported {
		return db.ArchiveDatasetStatusComplete
	}
	return db.ArchiveDatasetStatusUnsupported
}

func dbMergeRequestEvents(mrID int64, events []platform.MergeRequestEvent) []db.MREvent {
	out := make([]db.MREvent, 0, len(events))
	for _, event := range events {
		out = append(out, platform.DBMREvent(mrID, event))
	}
	return out
}

func archiveDatasetMutable(status db.ArchiveDatasetStatus) bool {
	return status == db.ArchiveDatasetStatusPending || status == db.ArchiveDatasetStatusFailed
}

func archiveDatasetKey(item db.ArchiveItemState, dataset db.ArchiveDataset, domainRevision int64) db.ArchiveDatasetKey {
	return db.ArchiveDatasetKey{
		RepoID: item.RepoID, ItemType: item.ItemType, ItemNumber: item.ItemNumber,
		Dataset: dataset, SnapshotUpdatedAt: item.ProviderUpdatedAt,
		DomainRevision: domainRevision,
	}
}

func (s *Service) fetchIssueComments(ctx context.Context, repo resolvedRepository, item db.ArchiveItemState, issueID, domainRevision int64) error {
	key := archiveDatasetKey(item, db.ArchiveDatasetComments, domainRevision)
	stage, err := s.db.GetArchiveDatasetStage(ctx, key)
	if err != nil {
		return err
	}
	for !stage.Exhausted {
		requestCtx, release, err := s.admit(ctx, repo, 1)
		if err != nil {
			return err
		}
		page, err := repo.Reader.ListArchiveIssueComments(requestCtx, repo.Ref, item.ItemNumber, stage.NextCursor)
		release()
		if err != nil {
			return err
		}
		payload, err := json.Marshal(page.Items)
		if err != nil {
			return err
		}
		stage, err = s.db.CommitArchiveDatasetPage(ctx, db.ArchiveDatasetPage{
			ArchiveDatasetKey: key, InputCursor: stage.NextCursor, NextCursor: page.NextCursor,
			Exhausted: page.Exhausted, RecordCount: len(page.Items), Payload: payload,
			MaxPages: maxArchiveDatasetPages, MaxRecords: maxArchiveDatasetRecords,
			MaxBytes: maxArchiveDatasetBytes, Now: s.now(),
		})
		if err != nil {
			return err
		}
	}
	payloads, err := s.db.LoadArchiveDatasetPages(ctx, key)
	if err != nil {
		return err
	}
	var comments []platform.IssueEvent
	for _, payload := range payloads {
		var page []platform.IssueEvent
		if err := json.Unmarshal(payload, &page); err != nil {
			return err
		}
		comments = append(comments, page...)
	}
	events := make([]db.IssueEvent, 0, len(comments))
	for _, event := range comments {
		events = append(events, platform.DBIssueEvent(issueID, event))
	}
	return s.db.PublishArchiveIssueComments(ctx, key, issueID, events)
}

type archiveMergeRequestEventPage func(context.Context, platform.RepoRef, int, string) (platform.ArchivePage[platform.MergeRequestEvent], error)

func (s *Service) fetchMergeRequestEvents(ctx context.Context, repo resolvedRepository, item db.ArchiveItemState, mrID, domainRevision int64, dataset db.ArchiveDataset, eventType string, read archiveMergeRequestEventPage) error {
	key := archiveDatasetKey(item, dataset, domainRevision)
	stage, err := s.db.GetArchiveDatasetStage(ctx, key)
	if err != nil {
		return err
	}
	for !stage.Exhausted {
		requestCtx, release, err := s.admit(ctx, repo, 1)
		if err != nil {
			return err
		}
		page, err := read(requestCtx, repo.Ref, item.ItemNumber, stage.NextCursor)
		release()
		if err != nil {
			return err
		}
		payload, err := json.Marshal(page.Items)
		if err != nil {
			return err
		}
		stage, err = s.db.CommitArchiveDatasetPage(ctx, db.ArchiveDatasetPage{
			ArchiveDatasetKey: key, InputCursor: stage.NextCursor, NextCursor: page.NextCursor,
			Exhausted: page.Exhausted, RecordCount: len(page.Items), Payload: payload,
			MaxPages: maxArchiveDatasetPages, MaxRecords: maxArchiveDatasetRecords, MaxBytes: maxArchiveDatasetBytes, Now: s.now(),
		})
		if err != nil {
			return err
		}
	}
	payloads, err := s.db.LoadArchiveDatasetPages(ctx, key)
	if err != nil {
		return err
	}
	var values []platform.MergeRequestEvent
	for _, payload := range payloads {
		var page []platform.MergeRequestEvent
		if err := json.Unmarshal(payload, &page); err != nil {
			return err
		}
		values = append(values, page...)
	}
	return s.db.PublishArchiveMREvents(ctx, key, mrID, eventType, dbMergeRequestEvents(mrID, values))
}

func (s *Service) fetchReviewThreads(ctx context.Context, repo resolvedRepository, item db.ArchiveItemState, mrID, domainRevision int64) error {
	key := archiveDatasetKey(item, db.ArchiveDatasetInlineComments, domainRevision)
	stage, err := s.db.GetArchiveDatasetStage(ctx, key)
	if err != nil {
		return err
	}
	for !stage.Exhausted {
		requestCtx, release, err := s.admit(ctx, repo, 1)
		if err != nil {
			return err
		}
		page, err := repo.Reader.ListArchiveReviewThreads(requestCtx, repo.Ref, item.ItemNumber, stage.NextCursor)
		release()
		if err != nil {
			return err
		}
		payload, err := json.Marshal(page.Items)
		if err != nil {
			return err
		}
		stage, err = s.db.CommitArchiveDatasetPage(ctx, db.ArchiveDatasetPage{
			ArchiveDatasetKey: key, InputCursor: stage.NextCursor, NextCursor: page.NextCursor,
			Exhausted: page.Exhausted, RecordCount: len(page.Items), Payload: payload,
			MaxPages: maxArchiveDatasetPages, MaxRecords: maxArchiveDatasetRecords, MaxBytes: maxArchiveDatasetBytes, Now: s.now(),
		})
		if err != nil {
			return err
		}
	}
	payloads, err := s.db.LoadArchiveDatasetPages(ctx, key)
	if err != nil {
		return err
	}
	var values []platform.MergeRequestReviewThread
	for _, payload := range payloads {
		var page []platform.MergeRequestReviewThread
		if err := json.Unmarshal(payload, &page); err != nil {
			return err
		}
		values = append(values, page...)
	}
	events, threads := platform.DBReviewThreads(values)
	for i := range events {
		events[i].MergeRequestID = mrID
	}
	return s.db.PublishArchiveReviewThreads(ctx, key, mrID, threads, events)
}

func (s *Service) recordHydrationFailure(ctx context.Context, item db.ArchiveItemState, cause error) error {
	decision := s.retries.Classify(cause, item.AttemptCount, s.now())
	var limit *db.ArchiveDatasetLimitError
	if errors.As(cause, &limit) {
		decision = RetryDecision{Code: db.ArchiveErrorCodeRepoBlocked}
	}
	if decision.Code == "" {
		decision.Code = db.ArchiveErrorCodeTransient
	}
	if decision.Code == db.ArchiveErrorCodeAuthentication || decision.Code == db.ArchiveErrorCodeRepoBlocked {
		if err := s.db.RecordArchiveRepositoryFailure(
			ctx, item.RepoID, decision.Code, cause.Error(), decision.RetryAt, s.now(),
		); err != nil {
			cause = errors.Join(cause, err)
		}
	}
	datasets := mutableArchiveDatasets(item)
	if len(datasets) == 0 {
		return cause
	}
	if err := s.db.FailArchiveItem(ctx, db.ArchiveItemFailure{
		RepoID: item.RepoID, ItemType: item.ItemType, ItemNumber: item.ItemNumber,
		Datasets: datasets, NextRetryAt: decision.RetryAt, Code: decision.Code,
		Detail: cause.Error(),
	}); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func mutableArchiveDatasets(item db.ArchiveItemState) []db.ArchiveDataset {
	var datasets []db.ArchiveDataset
	if item.CommentsStatus == db.ArchiveDatasetStatusPending || item.CommentsStatus == db.ArchiveDatasetStatusFailed {
		datasets = append(datasets, db.ArchiveDatasetComments)
	}
	if item.ReviewsStatus == db.ArchiveDatasetStatusPending || item.ReviewsStatus == db.ArchiveDatasetStatusFailed {
		datasets = append(datasets, db.ArchiveDatasetReviews)
	}
	if item.InlineCommentsStatus == db.ArchiveDatasetStatusPending || item.InlineCommentsStatus == db.ArchiveDatasetStatusFailed {
		datasets = append(datasets, db.ArchiveDatasetInlineComments)
	}
	return datasets
}
