package github

import (
	"context"
	"errors"
	"fmt"

	"go.kenn.io/forge/internal/db"
)

func (s *Syncer) commitIssueParentSnapshot(
	ctx context.Context,
	issue *db.Issue,
) (int64, int64, bool, error) {
	return s.db.UpsertIssueSnapshotWithLabels(ctx, issue)
}

// CommitMergeRequestParentSnapshot is the single choke point every accepted
// provider MR snapshot funnels through. Reclassifying workspace head-repo trust
// here guarantees every path that can change an MR's head_repo_clone_url fans
// that change out to a tracking workspace's mr_head_repo — see
// reclassifyWorkspaceHeadRepoTrust.
// CommitMergeRequestParentSnapshot applies a provider parent snapshot. When
// the accepted snapshot takes the merge request out of the open state, the
// db layer detects that transition inside the snapshot transaction and runs
// the terminal commit-liveness computation there, against the transaction's
// own view of the stored events — finalization is intrinsic to the choke
// point, so no caller can commit a terminal transition without it and no
// concurrent round can shift the data between compute and commit. It also
// drops the observation time of an empty or unknown mergeable state, which
// is never an observation.
func (s *Syncer) CommitMergeRequestParentSnapshot(
	ctx context.Context,
	repo RepoRef,
	mr *db.MergeRequest,
) (int64, int64, bool, error) {
	ctx = withCloneRepositoryIdentity(ctx, repo)
	clearUnknownMergeableStateTime(mr)
	mrID, revision, accepted, err := s.db.UpsertMergeRequestSnapshotWithLabelsAndEventMetadata(
		ctx, mr, s.terminalLivenessComputer(ctx, repo, mr),
	)
	if err != nil || !accepted {
		return mrID, revision, accepted, err
	}
	if s.afterMergeRequestParentSnapshotCommit != nil {
		s.afterMergeRequestParentSnapshotCommit()
	}
	s.reclassifyWorkspaceHeadRepoTrust(
		ctx, repo, mr.RepoID, mr.Number,
	)
	if mr.State != db.MergeRequestStateOpen && s.onMergeRequestTerminal != nil {
		s.onMergeRequestTerminal(ctx, mr.RepoID, mr.Number)
	}
	return mrID, revision, accepted, err
}

// commitIssueCommentsSnapshot binds the child snapshot to the parent issue ID
// the caller fetched for. Resolving the parent by route here would let a
// concurrent route replacement redirect stale comments onto the replacement
// repository's issue when snapshot revisions coincide.
func (s *Syncer) commitIssueCommentsSnapshot(
	ctx context.Context,
	_ RepoRef,
	issueID int64,
	number int,
	expectedRevision int64,
	events []db.IssueEvent,
	otherEvents []db.IssueEvent,
	derived *db.IssueDerivedFields,
) (bool, error) {
	for i := range events {
		events[i].IssueID = issueID
	}
	for i := range otherEvents {
		otherEvents[i].IssueID = issueID
	}
	applied, err := s.db.CommitIssueChildSnapshot(ctx, db.IssueChildSnapshot{
		IssueID: issueID, ExpectedRevision: expectedRevision,
		Comments: events, OtherEvents: otherEvents, DerivedFields: derived,
	})
	if err != nil {
		return false, fmt.Errorf("commit child snapshot for issue #%d: %w", number, err)
	}
	return applied, nil
}

// commitMergeRequestDatasets binds the child snapshot to the parent MR ID the
// caller fetched for — see commitIssueCommentsSnapshot for why the route must
// not resolve the parent.
//
// livenessHeadSHA, when non-empty, is the open MR's current head for this
// round: commit-obsolescence flags are computed against it and ride this same
// revision-guarded snapshot (incoming commit events carry flags in their
// metadata; stored events the round does not re-list are updated in the same
// transaction). Rounds without a verifiable head commit their events without
// liveness changes. Callers pass "" for closed MRs and for batches that carry
// no commit events.
func (s *Syncer) commitMergeRequestDatasets(
	ctx context.Context,
	repo RepoRef,
	mrID int64,
	number int,
	expectedRevision int64,
	comments []db.MREvent,
	commentsComplete bool,
	reviews []db.MREvent,
	inline []db.MREvent,
	threads []db.MRReviewThread,
	inlineComplete bool,
	otherEvents []db.MREvent,
	derived *db.MRDerivedFields,
	livenessHeadSHA string,
) (bool, error) {
	for _, events := range [][]db.MREvent{comments, reviews, inline, otherEvents} {
		for i := range events {
			events[i].MergeRequestID = mrID
		}
	}
	metadataUpdates := s.computeCommitLiveness(
		ctx, repo, mrID, livenessHeadSHA, otherEvents,
	)
	applied, err := s.db.CommitMergeRequestChildSnapshot(ctx, db.MergeRequestChildSnapshot{
		MergeRequestID: mrID, ExpectedRevision: expectedRevision,
		Comments: comments, CommentsComplete: commentsComplete, Reviews: reviews,
		InlineComments: inline, ReviewThreads: threads, InlineCommentsComplete: inlineComplete,
		OtherEvents: otherEvents, DerivedFields: derived,
		EventMetadataUpdates: metadataUpdates,
	})
	if err != nil {
		return false, fmt.Errorf("commit child snapshot for MR #%d: %w", number, err)
	}
	return applied, nil
}

// terminalLivenessComputer returns the in-transaction finalizer for a parent
// snapshot that may take the merge request out of the open state. The db
// layer invokes it only when the accepted upsert is that transition, with the
// transaction's own view of the stored events: the transition round is the
// last round that will ever compute this MR's flags, so they are computed
// from data no concurrent round can shift and land atomically with the
// terminal state. Snapshots that cannot be such a transition return nil —
// open rounds' flags travel with the dataset commit, and any lost race there
// is repaired by the next round, which terminal MRs never get.
func (s *Syncer) terminalLivenessComputer(
	ctx context.Context,
	repo RepoRef,
	mr *db.MergeRequest,
) db.MREventMetadataComputer {
	if mr == nil || mr.State == db.MergeRequestStateOpen ||
		mr.PlatformHeadSHA == "" || s.clones == nil {
		return nil
	}
	return func(mrID int64, events []db.MREvent) map[string]string {
		return s.computeCommitLivenessForEvents(
			ctx, repo, mrID, mr.PlatformHeadSHA, nil, events,
		)
	}
}

// livenessHeadForRound picks the head against which a round computes commit
// liveness. Open MRs always compute against their current head. A round that
// takes an MR out of the open state computes once against the final head, so
// the flags it persists become the terminal record; rounds for
// already-terminal MRs return "" — merged and closed history is never
// refetched or recomputed.
func livenessHeadForRound(normalized, existing *db.MergeRequest) string {
	if normalized == nil {
		return ""
	}
	if normalized.State == db.MergeRequestStateOpen {
		return normalized.PlatformHeadSHA
	}
	if existing != nil && existing.State == db.MergeRequestStateOpen {
		return normalized.PlatformHeadSHA
	}
	return ""
}

var errParentSnapshotAdvanced = errors.New("provider parent snapshot advanced during child refresh")
