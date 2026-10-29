package github

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// syncMRDiff fetches the bare clone and computes diff SHAs for a single PR.
// Returns nil when there is no clone manager (the caller has already opted
// out of diff support); otherwise returns an error wrapping a
// *DiffSyncError that describes the first failure encountered along the
// clone or diff path. Callers can recover the structured categorization via
// errors.As.
func (s *Syncer) syncMRDiff(
	ctx context.Context, repo RepoRef, repoID, mrID, expectedRevision int64, number int,
	ghPR *gh.PullRequest, normalized *db.MergeRequest,
) error {
	ctx = withCloneRepositoryIdentity(ctx, repo)
	if s.clones == nil {
		return nil
	}
	host := repoHost(repo)
	if err := s.ensureClone(ctx, repo); err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeCloneUnavailable,
			Err:  fmt.Errorf("ensure bare clone for #%d: %w", number, err),
		}
	}

	if ghPR.GetMerged() {
		// Merged MRs need special merge-base logic via the pull ref.
		// Force recomputation to repair any previously incorrect SHAs.
		return s.computeMergedMRDiffSHAs(
			ctx, repo, repoID, mrID, expectedRevision, number,
			normalized.PlatformHeadSHA, normalized.PlatformBaseSHA,
			ghPR.GetMergeCommitSHA(), true,
		)
	}

	if normalized.PlatformHeadSHA == "" || normalized.PlatformBaseSHA == "" {
		return nil
	}
	mb, err := s.clones.MergeBase(ctx, string(repoPlatform(repo)), host, repo.Owner, repo.Name, normalized.PlatformBaseSHA, normalized.PlatformHeadSHA)
	if err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeMergeBaseFailed,
			Err:  fmt.Errorf("merge-base for #%d: %w", number, err),
		}
	}
	applied, err := s.db.UpdateDiffSHAsSnapshot(
		ctx, mrID, expectedRevision,
		normalized.PlatformHeadSHA, normalized.PlatformBaseSHA,
		normalized.PlatformHeadSHA, normalized.PlatformBaseSHA, mb,
	)
	if err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeInternal,
			Err:  fmt.Errorf("update diff SHAs for #%d: %w", number, err),
		}
	}
	if !applied {
		return errParentSnapshotAdvanced
	}
	return nil
}

// syncProviderMRDiff records the reviewed diff snapshot for a
// non-GitHub MR: the locally verified head/base SHAs plus merge-base.
// Head-binding providers gate merge/approve on this snapshot matching
// the platform head, so it must be written by the same sync that
// refreshed the MR row. Only open MRs are snapshotted — head pins are
// meaningless once merged/closed, and the merged-MR merge-base repair
// logic is GitHub-specific.
func (s *Syncer) syncProviderMRDiff(
	ctx context.Context, repo RepoRef, repoID, mrID, expectedRevision int64, number int,
	normalized *db.MergeRequest, ensureClone bool,
) error {
	ctx = withCloneRepositoryIdentity(ctx, repo)
	if s.clones == nil {
		return nil
	}
	if normalized.State != db.MergeRequestStateOpen {
		return nil
	}
	if normalized.PlatformHeadSHA == "" || normalized.PlatformBaseSHA == "" {
		return nil
	}
	host := repoHost(repo)
	// List sync already fetched the repo clone once for this cycle;
	// refetching per MR would turn one repo sync into N network
	// round-trips. Per-MR detail syncs have no prior fetch and pass
	// ensureClone.
	if ensureClone {
		if err := s.ensureClone(ctx, repo); err != nil {
			return &DiffSyncError{
				Code: DiffSyncCodeCloneUnavailable,
				Err:  fmt.Errorf("ensure bare clone for #%d: %w", number, err),
			}
		}
	}
	mb, err := s.clones.MergeBase(ctx, string(repoPlatform(repo)), host, repo.Owner, repo.Name, normalized.PlatformBaseSHA, normalized.PlatformHeadSHA)
	if err != nil && repoPlatform(repo) == platform.KindBitbucket {
		sourceBranch := normalized.HeadBranch
		if normalized.HeadRepoCloneURLUnknown || normalized.HeadRepoIdentityStale {
			sourceBranch = ""
		}
		fetchErr := s.clones.FetchBitbucketMergeRequestHead(ctx, host, repo.Owner, repo.Name, number, normalized.HeadRepoCloneURL, sourceBranch, normalized.PlatformHeadSHA)
		if fetchErr != nil {
			err = fmt.Errorf("fetch source head: %w", fetchErr)
		} else {
			mb, err = s.clones.MergeBase(ctx, string(repoPlatform(repo)), host, repo.Owner, repo.Name, normalized.PlatformBaseSHA, normalized.PlatformHeadSHA)
		}
	}
	if err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeMergeBaseFailed,
			Err:  fmt.Errorf("merge-base for #%d: %w", number, err),
		}
	}
	applied, err := s.db.UpdateDiffSHAsSnapshot(
		ctx, mrID, expectedRevision,
		normalized.PlatformHeadSHA, normalized.PlatformBaseSHA,
		normalized.PlatformHeadSHA, normalized.PlatformBaseSHA, mb,
	)
	if err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeInternal,
			Err:  fmt.Errorf("update diff SHAs for #%d: %w", number, err),
		}
	}
	if !applied {
		return errParentSnapshotAdvanced
	}
	return nil
}
