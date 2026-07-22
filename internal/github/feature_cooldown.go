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
	nextGeneration  uint64
	nextReservation uint64
}

type repositoryFeatureCooldownState struct {
	nextProbe   time.Time
	generation  uint64
	reservation uint64
}

type repositoryFeatureProbe struct {
	cooldowns   *repositoryFeatureCooldowns
	key         repositoryFeatureCooldownKey
	generation  uint64
	reservation uint64
	bypass      bool
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
	if repoPlatform(repo) == platform.KindGitHub {
		repo = canonicalRepoRef(repo)
		if repo.Owner != "" && repo.Name != "" {
			repo.RepoPath = repo.Owner + "/" + repo.Name
		}
	}
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
	bypass bool,
) (repositoryFeatureProbe, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := repositoryFeatureKey(repo, feature)
	state, ok := c.states[key]
	if bypass {
		return repositoryFeatureProbe{
			cooldowns:  c,
			key:        key,
			generation: state.generation,
			bypass:     true,
		}, true
	}
	if !ok {
		return repositoryFeatureProbe{cooldowns: c, key: key}, true
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
		generation:  state.generation,
		reservation: state.reservation,
	}, true
}

func (c *repositoryFeatureCooldowns) deferUntil(repo RepoRef, feature string, nextProbe time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.states == nil {
		c.states = make(map[repositoryFeatureCooldownKey]repositoryFeatureCooldownState)
	}
	c.nextGeneration++
	c.states[repositoryFeatureKey(repo, feature)] = repositoryFeatureCooldownState{
		nextProbe:  nextProbe,
		generation: c.nextGeneration,
	}
}

func (probe repositoryFeatureProbe) release() {
	if probe.cooldowns == nil || probe.reservation == 0 {
		return
	}
	probe.cooldowns.mu.Lock()
	defer probe.cooldowns.mu.Unlock()
	state, ok := probe.cooldowns.states[probe.key]
	if ok && state.generation == probe.generation && state.reservation == probe.reservation {
		delete(probe.cooldowns.states, probe.key)
	}
}

func (probe repositoryFeatureProbe) clear() {
	if probe.cooldowns == nil {
		return
	}
	probe.cooldowns.mu.Lock()
	defer probe.cooldowns.mu.Unlock()
	state, ok := probe.cooldowns.states[probe.key]
	if !ok || state.generation != probe.generation {
		return
	}
	if !probe.bypass && probe.reservation != 0 && state.reservation != probe.reservation {
		return
	}
	delete(probe.cooldowns.states, probe.key)
}

func (s *Syncer) beginRepositoryFeatureProbe(
	ctx context.Context,
	repo RepoRef,
	feature string,
) (repositoryFeatureProbe, bool) {
	return s.featureCooldowns.beginProbe(
		repo, feature, s.now().UTC(), repositoryFeatureCooldownBypassed(ctx),
	)
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
	classified := repositoryFeatureDisabledError(repo, feature, err)
	if classified == nil {
		return false
	}
	return s.recordRepositoryFeatureDisabled(repo, feature, classified)
}

func repositoryFeatureDisabledError(repo RepoRef, feature string, err error) error {
	if errors.Is(err, platform.ErrRepositoryFeatureDisabled) {
		return err
	}
	if repoPlatform(repo) != platform.KindGitHub {
		return nil
	}
	return githubRepositoryFeatureDisabled(repoHost(repo), feature, err)
}
