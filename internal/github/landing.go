package github

import (
	"context"

	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/platform"
)

// CollectLandingEvidence uses foreground routing and admission, even when
// periodic sync is disabled. No database lock is held during provider work.
func (s *Syncer) CollectLandingEvidence(ctx context.Context, repo platform.RepoRef, budget platform.Budget) (platform.LandingSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return platform.LandingSnapshot{}, err
	}
	provider, err := s.directClients.Provider(repo.Platform, repo.Host)
	if err != nil {
		return platform.LandingSnapshot{}, err
	}
	reader, ok := provider.(platform.LandingReader)
	if !ok {
		return platform.LandingSnapshot{}, platform.UnsupportedCapability(repo.Platform, repo.Host, "landing_evidence")
	}
	bucket, err := s.bucketKeyForRepoContext(ctx, RepoRef{Platform: repo.Platform, PlatformHost: repo.Host, Owner: repo.Owner, Name: repo.Name}, false)
	if err != nil {
		return platform.LandingSnapshot{}, err
	}
	release := s.beginProviderWork(ctx, bucket, archive.PriorityActiveDetail)
	defer release()
	if err := ctx.Err(); err != nil {
		return platform.LandingSnapshot{}, err
	}
	return reader.CollectLandingEvidence(WithEssentialSyncBudget(ctx), repo, budget)
}
