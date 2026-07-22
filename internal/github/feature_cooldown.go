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
	mu              sync.Mutex
	states          map[repositoryFeatureCooldownKey]repositoryFeatureCooldownState
	nextReservation uint64
}

type repositoryFeatureCooldownState struct {
	nextProbe   time.Time
	reservation uint64
}

type repositoryFeatureProbe struct {
	cooldowns   *repositoryFeatureCooldowns
	key         repositoryFeatureCooldownKey
	reservation uint64
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

func (c *repositoryFeatureCooldowns) beginProbe(
	repo RepoRef,
	feature string,
	now time.Time,
) (repositoryFeatureProbe, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := repositoryFeatureKey(repo, feature)
	state, ok := c.states[key]
	if !ok {
		return repositoryFeatureProbe{}, true
	}
	if state.reservation != 0 || state.nextProbe.After(now) {
		return repositoryFeatureProbe{}, false
	}
	c.nextReservation++
	state.reservation = c.nextReservation
	c.states[key] = state
	return repositoryFeatureProbe{
		cooldowns:   c,
		key:         key,
		reservation: state.reservation,
	}, true
}

func (c *repositoryFeatureCooldowns) deferUntil(repo RepoRef, feature string, nextProbe time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.states == nil {
		c.states = make(map[repositoryFeatureCooldownKey]repositoryFeatureCooldownState)
	}
	c.states[repositoryFeatureKey(repo, feature)] = repositoryFeatureCooldownState{
		nextProbe: nextProbe,
	}
}

func (c *repositoryFeatureCooldowns) clear(repo RepoRef, feature string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.states, repositoryFeatureKey(repo, feature))
}

func (probe repositoryFeatureProbe) release() {
	if probe.cooldowns == nil {
		return
	}
	probe.cooldowns.mu.Lock()
	defer probe.cooldowns.mu.Unlock()
	state, ok := probe.cooldowns.states[probe.key]
	if ok && state.reservation == probe.reservation {
		delete(probe.cooldowns.states, probe.key)
	}
}

func (s *Syncer) beginRepositoryFeatureProbe(
	ctx context.Context,
	repo RepoRef,
	feature string,
) (repositoryFeatureProbe, bool) {
	if repositoryFeatureCooldownBypassed(ctx) {
		return repositoryFeatureProbe{}, true
	}
	return s.featureCooldowns.beginProbe(repo, feature, s.now().UTC())
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

func (s *Syncer) recordGitHubRepositoryFeatureDisabled(
	repo RepoRef,
	feature string,
	err error,
) bool {
	if classified := githubRepositoryFeatureDisabled(repoHost(repo), feature, err); classified != nil {
		err = classified
	}
	return s.recordRepositoryFeatureDisabled(repo, feature, err)
}
