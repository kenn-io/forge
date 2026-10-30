package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/telemetry"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// RefreshMRCIStatusOnProvider fetches only CI checks for a PR's head SHA and
// persists the derived CI fields. It intentionally skips the heavier PR detail
// sync path (timeline, diff, review, and body refreshes).
func (s *Syncer) RefreshMRCIStatusOnProvider(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	headSHA string,
) ([]string, error) {
	if headSHA == "" {
		return nil, nil
	}
	if repoPlatform(repo) == platform.KindGitHub {
		result, err := s.fetchGitHubCIStatus(ctx, repo, number, headSHA)
		if err != nil {
			return nil, err
		}
		if result.Warning != "" {
			return []string{result.Warning}, nil
		}
		if !result.Updated {
			return nil, nil
		}
		return nil, s.db.UpdateMRCIStatusForHead(
			ctx, repoID, number, headSHA,
			result.Status, result.ChecksJSON, ciHasPending(result.ChecksJSON),
			&result.RequestedAt,
		)
	}

	ciReader, err := s.ciReaderFor(repo)
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve CI reader for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	requestedAt := s.nowUTC()
	checks, err := ciReader.ListCIChecks(ctx, platformRepoRef(repo), headSHA)
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) {
			return nil, nil
		}
		logSyncFailure(ctx, slog.LevelWarn, "list CI checks failed", err,
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
		)
		return []string{ciRefreshWarning}, nil
	}
	dbChecks := platformdb.DBCIChecks(checks)
	if dbChecks == nil {
		dbChecks = []db.CICheck{}
	}
	ciJSON, _ := json.Marshal(dbChecks)
	ciStatus := deriveCIStatusFromChecks(dbChecks)
	if err := s.db.UpdateMRCIStatusForHead(
		ctx, repoID, number, headSHA,
		ciStatus, string(ciJSON), ciHasPending(string(ciJSON)),
		&requestedAt,
	); err != nil {
		return nil, fmt.Errorf("update CI status for MR #%d: %w", number, err)
	}
	return nil, nil
}

// RefreshMRCIStatusForRepository refreshes CI only while repo still owns its
// captured route. Delayed background work uses this entry point so route reuse
// cannot write a replacement repository's provider response under repoID.
func (s *Syncer) RefreshMRCIStatusForRepository(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	headSHA string,
) ([]string, error) {
	return s.RefreshMRCIStatusOnProvider(ctx, repo, repoID, number, headSHA)
}

// refreshCIStatus fetches combined status and check runs for a PR's head SHA.
// Called on every sync cycle for open PRs, since check runs change independently
// of the PR's updated_at field. Takes headSHA and number directly so it can be
// invoked from the 304 code path, where the caller holds DB rows rather than
// a *gh.PullRequest.
func (s *Syncer) refreshCIStatus(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	headSHA string,
) error {
	result, err := s.fetchGitHubCIStatus(ctx, repo, number, headSHA)
	if err != nil {
		return err
	}
	if !result.Updated {
		return nil
	}
	return s.db.UpdateMRCIStatusForHead(
		ctx, repoID, number, headSHA,
		result.Status, result.ChecksJSON, ciHasPending(result.ChecksJSON),
		&result.RequestedAt,
	)
}

// refreshCIStatusSnapshot is the detail-sync variant of refreshCIStatus. It
// rejects the write when another parent snapshot won while CI was in flight.
func (s *Syncer) refreshCIStatusSnapshot(
	ctx context.Context,
	repo RepoRef,
	mrID int64,
	expectedRevision int64,
	number int,
	headSHA string,
) (bool, error) {
	result, err := s.fetchGitHubCIStatus(ctx, repo, number, headSHA)
	if err != nil {
		return false, err
	}
	if !result.Updated {
		return true, nil
	}
	applied, err := s.db.UpdateMergeRequestCISnapshot(
		ctx, mrID, expectedRevision, result.Status, result.ChecksJSON,
		&result.RequestedAt,
	)
	if errors.Is(err, db.ErrRepositoryIdentityChanged) {
		return false, nil
	}
	return applied, err
}

const ciRefreshWarning = "Could not refresh CI checks; showing last known status."

type ciStatusFetchResult struct {
	Status     string
	ChecksJSON string
	Updated    bool
	Warning    string
	// RequestedAt is when the check-runs request was sent.
	RequestedAt time.Time
}

func (s *Syncer) fetchGitHubCIStatus(
	ctx context.Context,
	repo RepoRef,
	number int,
	headSHA string,
) (ciStatusFetchResult, error) {
	if headSHA == "" {
		return ciStatusFetchResult{}, nil
	}

	// Fetch both sources. On failure, skip the DB write to preserve
	// existing data rather than wiping it with empty values.
	client, err := s.clientFor(repo)
	if err != nil {
		return ciStatusFetchResult{}, fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	requestedAt := s.nowUTC()
	checkRuns, err := client.ListCheckRunsForRef(ctx, repo.Owner, repo.Name, headSHA)
	if err != nil {
		s.observeSync(ctx, repo, telemetry.SyncSourceChecks, err)
		logSyncFailure(ctx, slog.LevelWarn, "list check runs failed", err,
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
		)
		return ciStatusFetchResult{Warning: ciRefreshWarning}, nil
	}

	combined, err := client.GetCombinedStatus(ctx, repo.Owner, repo.Name, headSHA)
	if err != nil {
		s.observeSync(ctx, repo, telemetry.SyncSourceChecks, err)
		logSyncFailure(ctx, slog.LevelWarn, "get combined status failed", err,
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
		)
		return ciStatusFetchResult{Warning: ciRefreshWarning}, nil
	}

	s.observeSync(ctx, repo, telemetry.SyncSourceChecks, nil)
	return ciStatusFetchResult{
		Status:      DeriveOverallCIStatus(checkRuns, combined),
		ChecksJSON:  NormalizeCIChecks(checkRuns, combined),
		Updated:     true,
		RequestedAt: requestedAt,
	}, nil
}

// refreshWorkflowApproval fetches action_required workflow runs at the
// given head SHA and persists the result on the merge request row.
//
// The persisted snapshot is keyed by head SHA for reads and guarded by the
// local parent revision so same-head workflow changes cannot race a newer
// equal-timestamp parent snapshot.
//
// Failures (no client, network errors, closed PR) are intentionally
// silent: this is a refresh, not a precondition. The previous
// persisted state stays in place rather than being clobbered.
func (s *Syncer) refreshWorkflowApproval(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	headSHA string,
	ghPR *gh.PullRequest,
	normalized *db.MergeRequest,
) (bool, int) {
	if headSHA == "" {
		return true, 0
	}
	state := ""
	switch {
	case ghPR != nil:
		state = ghPR.GetState()
	case normalized != nil:
		state = string(normalized.State)
	}
	if state != "open" {
		return true, 0
	}

	client, err := s.clientFor(repo)
	if err != nil {
		return true, 0
	}
	headRepoFullName := ""
	headRef := ""
	if ghPR != nil && ghPR.GetHead() != nil {
		headRepoFullName = ghPR.GetHead().GetRepo().GetFullName()
		headRef = ghPR.GetHead().GetRef()
	}
	// GraphQL bulk fetch populates clone URL but not full name on the
	// head repo struct, so fall back to parsing the persisted clone
	// URL. Without this, fork PRs synced via bulk would lose the head
	// repo identity needed to match fork-triggered workflow runs whose
	// pull_requests array is empty.
	if headRepoFullName == "" && normalized != nil {
		headRepoFullName = platformgithub.ParseHeadRepoFullName(normalized.HeadRepoCloneURL)
	}
	if headRef == "" && normalized != nil {
		headRef = normalized.HeadBranch
	}

	runs, err := client.ListWorkflowRunsForHeadSHA(ctx, repo.Owner, repo.Name, headSHA)
	if err != nil {
		logSyncFailure(ctx, slog.LevelWarn, "list workflow runs for approval refresh failed", err,
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
		)
		return true, 1
	}
	approval := WorkflowApprovalStateFromRuns(
		FilterWorkflowRunsAwaitingApproval(runs, PRSource{
			Number:           number,
			HeadSHA:          headSHA,
			HeadRepoFullName: headRepoFullName,
			HeadRef:          headRef,
		}),
	)
	if err := s.db.UpdateMRWorkflowApproval(
		ctx, repoID, number, time.Now().UTC(), headSHA,
		approval.Required, approval.Count,
	); err != nil {
		slog.Warn("persist workflow approval state failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
			"err", err,
		)
	}
	return true, 1
}

// ciHasPending parses the CI checks JSON and returns true if any
// check has a status other than "completed".
func ciHasPending(ciChecksJSON string) bool {
	if ciChecksJSON == "" {
		return false
	}
	var checks []db.CICheck
	if err := json.Unmarshal([]byte(ciChecksJSON), &checks); err != nil {
		return false
	}
	for _, c := range checks {
		if c.Status != "completed" {
			return true
		}
	}
	return false
}

func (s *Syncer) canSpendWorkflowApprovalRefresh(repo RepoRef) bool {
	budget, ok := s.BudgetForRepo(repo)
	return !ok || budget == nil || budget.CanSpend(1)
}
