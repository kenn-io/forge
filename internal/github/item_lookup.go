package github

import (
	"context"
	"errors"
	"fmt"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// issueFetchOutcomeError routes a raw single-issue fetch result through the
// canonical lookup classification when the repo's registered reader is the
// GitHub provider. A nil return means no outcome mapping applies (present
// item, non-GitHub reader, or a nil conditional result).
func (s *Syncer) issueFetchOutcomeError(
	ctx context.Context,
	repo RepoRef,
	number int,
	issue *gh.Issue,
	err error,
) error {
	reader, readerErr := s.issueReaderFor(repo)
	if readerErr == nil {
		if provider, ok := reader.(*platformgithub.Provider); ok {
			return provider.IssueLookupOutcomeError(ctx, platformRepoRef(repo), number, issue, err)
		}
	}
	return nil
}

// issueOnlyFetchOutcomeError adds the GitHub Issues API's PR-shape
// classification for callers that require an issue. Kind-dispatching callers
// such as SyncItemByNumber use issueFetchOutcomeError directly so they can
// route a PR-shaped response to merge-request sync instead.
func (s *Syncer) issueOnlyFetchOutcomeError(
	ctx context.Context,
	repo RepoRef,
	number int,
	issue *gh.Issue,
	err error,
) error {
	if outcomeErr := s.issueFetchOutcomeError(ctx, repo, number, issue, err); outcomeErr != nil {
		return outcomeErr
	}
	reader, readerErr := s.issueReaderFor(repo)
	if readerErr == nil {
		if provider, ok := reader.(*platformgithub.Provider); ok {
			return provider.IssuePullRequestOutcomeError(platformRepoRef(repo), number, issue)
		}
	}
	return nil
}

// mergeRequestFetchOutcomeError is the merge-request counterpart to
// issueFetchOutcomeError.
func (s *Syncer) mergeRequestFetchOutcomeError(
	ctx context.Context,
	repo RepoRef,
	number int,
	pr *gh.PullRequest,
	err error,
) error {
	reader, readerErr := s.mergeRequestReaderFor(repo)
	if readerErr == nil {
		if provider, ok := reader.(*platformgithub.Provider); ok {
			return provider.MergeRequestLookupOutcomeError(ctx, platformRepoRef(repo), number, pr, err)
		}
	}
	return nil
}

func (s *Syncer) classifyProviderItemLookupError(
	ctx context.Context,
	repo RepoRef,
	err error,
) error {
	if errors.Is(err, platform.ErrLookupNotPresent) {
		return err
	}
	if !errors.Is(err, platform.ErrNotFound) {
		return err
	}
	repositoryReader, readerErr := s.clients.RepositoryReader(repoPlatform(repo), repoHost(repo))
	if readerErr != nil {
		return err
	}
	if _, probeErr := repositoryReader.GetRepository(ctx, platformRepoRef(repo)); probeErr != nil {
		return errors.Join(err, fmt.Errorf("probe repository after item not found: %w", probeErr))
	}
	return errors.Join(platform.ErrLookupNotPresent, err)
}

// SyncItemByNumber fetches an item by number from GitHub, determines
// whether it is a PR or issue, syncs it into the DB, and returns the
// item type ("pr" or "issue").
// Returns an error if the repo is not in the configured repo list.
func (s *Syncer) SyncItemByNumber(
	ctx context.Context, owner, name string, number int,
) (string, error) {
	repo, ok, err := s.trackedRepo(owner, name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("repo %s/%s is %w", owner, name, ErrRepoNotTracked)
	}
	repo.Owner = owner
	repo.Name = name
	repo.PlatformHost = repoHost(repo)
	bucket, err := s.bucketKeyForRepo(repo, false)
	if err != nil {
		return "", fmt.Errorf("resolve item credential route for %s/%s: %w", owner, name, err)
	}
	releaseProviderWork := s.beginProviderWork(ctx, bucket, archive.PriorityActiveDetail)
	defer releaseProviderWork()

	if repoPlatform(repo) != platform.KindGitHub {
		return "", fmt.Errorf(
			"sync item by number for %s/%s on %s/%s requires an item type",
			owner, name, repoPlatform(repo), repo.PlatformHost,
		)
	}

	// GitHub's Issues API returns both issues and PRs. If the
	// response has PullRequestLinks, it's a PR.
	client, err := s.clientFor(repo)
	if err != nil {
		return "", fmt.Errorf("resolve client for %s/%s: %w", owner, name, err)
	}
	ghIssue, err := client.GetIssue(ctx, owner, name, number)
	// Route fetch failures and detected transfers through the canonical
	// lookup classification so removed, inaccessible, and moved items
	// surface typed outcomes instead of generic upstream failures.
	if outcomeErr := s.issueFetchOutcomeError(ctx, repo, number, ghIssue, err); outcomeErr != nil {
		return "", fmt.Errorf(
			"get item %s/%s#%d: %w", owner, name, number, outcomeErr,
		)
	}
	if err != nil {
		return "", fmt.Errorf(
			"get item %s/%s#%d: %w", owner, name, number, err,
		)
	}
	if ghIssue == nil {
		return "", fmt.Errorf(
			"get item %s/%s#%d: client returned nil issue", owner, name, number,
		)
	}

	if ghIssue.PullRequestLinks != nil {
		if err := s.SyncMR(ctx, owner, name, number); err != nil {
			// A DiffSyncError means the PR row, timeline, and CI status
			// were upserted successfully and only the diff computation
			// failed. The item type is known, so resolution can still
			// succeed; surface the error so callers that care about diff
			// freshness can react, but report itemType so callers that
			// just need to route the user (e.g. /items/{n}/resolve) can
			// proceed.
			if _, ok := errors.AsType[*DiffSyncError](err); ok {
				return "pr", err
			}
			return "", fmt.Errorf(
				"sync MR %s/%s#%d: %w", owner, name, number, err,
			)
		}
		return "pr", nil
	}

	if err := s.SyncIssue(ctx, owner, name, number); err != nil {
		return "", fmt.Errorf(
			"sync issue %s/%s#%d: %w", owner, name, number, err,
		)
	}
	return "issue", nil
}
