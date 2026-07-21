package github

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/platform"
)

const repositoryFeatureProbeInterval = 24 * time.Hour

type repositoryFeatureCooldownKey struct {
	platform platform.Kind
	host     string
	repoPath string
	feature  string
}

type repositoryFeatureCooldowns struct {
	mu        sync.Mutex
	nextProbe map[repositoryFeatureCooldownKey]time.Time
}

type repositoryFeatureCooldownBypassKey struct{}

func withRepositoryFeatureCooldownBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, repositoryFeatureCooldownBypassKey{}, true)
}

func repositoryFeatureCooldownBypassed(ctx context.Context) bool {
	bypass, _ := ctx.Value(repositoryFeatureCooldownBypassKey{}).(bool)
	return bypass
}

func repositoryFeatureKey(repo RepoRef, feature string) repositoryFeatureCooldownKey {
	ref := platformRepoRef(repo)
	return repositoryFeatureCooldownKey{
		platform: ref.Platform,
		host:     ref.Host,
		repoPath: ref.RepoPath,
		feature:  feature,
	}
}

func (c *repositoryFeatureCooldowns) due(repo RepoRef, feature string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	nextProbe, ok := c.nextProbe[repositoryFeatureKey(repo, feature)]
	return !ok || !nextProbe.After(now)
}

func (c *repositoryFeatureCooldowns) deferUntil(repo RepoRef, feature string, nextProbe time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nextProbe == nil {
		c.nextProbe = make(map[repositoryFeatureCooldownKey]time.Time)
	}
	c.nextProbe[repositoryFeatureKey(repo, feature)] = nextProbe
}

func (c *repositoryFeatureCooldowns) clear(repo RepoRef, feature string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.nextProbe, repositoryFeatureKey(repo, feature))
}

func (s *Syncer) repositoryFeatureDue(ctx context.Context, repo RepoRef, feature string) bool {
	if repositoryFeatureCooldownBypassed(ctx) {
		return true
	}
	return s.featureCooldowns.due(repo, feature, s.now().UTC())
}

func (s *Syncer) recordRepositoryFeatureDisabled(repo RepoRef, feature string, err error) bool {
	var platformErr *platform.Error
	if !errors.As(err, &platformErr) ||
		platformErr.Code != platform.ErrCodeRepositoryFeatureDisabled ||
		platformErr.Capability != feature {
		return false
	}
	nextProbe := s.now().UTC().Add(repositoryFeatureProbeInterval)
	s.featureCooldowns.deferUntil(repo, feature, nextProbe)
	slog.Info("repository feature disabled; deferring background sync",
		"platform", repoPlatform(repo),
		"host", repoHost(repo),
		"repo", platformRepoRef(repo).RepoPath,
		"feature", feature,
		"next_probe_at", nextProbe,
	)
	return true
}
