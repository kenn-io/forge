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
	gate        *repositoryFeatureProbeGate
}

type repositoryFeatureProbeGate struct {
	once sync.Once
	gate *sync.RWMutex
}

type repoIncarnationGateHeldKey struct{}

type repositoryFeatureCooldownBypassKey struct{}

type repositoryFeatureCooldownBypass struct {
	throughGeneration uint64
}

func withRepositoryFeatureCooldownBypass(
	ctx context.Context,
	throughGeneration uint64,
) context.Context {
	return context.WithValue(ctx, repositoryFeatureCooldownBypassKey{}, repositoryFeatureCooldownBypass{
		throughGeneration: throughGeneration,
	})
}

func repositoryFeatureCooldownBypassFromContext(
	ctx context.Context,
) (repositoryFeatureCooldownBypass, bool) {
	bypass, ok := ctx.Value(repositoryFeatureCooldownBypassKey{}).(repositoryFeatureCooldownBypass)
	return bypass, ok
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

func (c *repositoryFeatureCooldowns) beginProbeWithRetry(
	repo RepoRef,
	feature string,
	now time.Time,
	bypass repositoryFeatureCooldownBypass,
	bypassEnabled bool,
) (repositoryFeatureProbe, bool, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := repositoryFeatureKey(repo, feature)
	state, ok := c.states[key]
	if bypassEnabled && (!ok || state.generation <= bypass.throughGeneration) {
		return repositoryFeatureProbe{
			cooldowns:  c,
			key:        key,
			generation: state.generation,
			bypass:     true,
		}, true, time.Time{}
	}
	if !ok {
		return repositoryFeatureProbe{cooldowns: c, key: key}, true, time.Time{}
	}
	if state.reservation != 0 {
		return repositoryFeatureProbe{}, false, now.Add(time.Second)
	}
	if state.nextProbe.After(now) {
		return repositoryFeatureProbe{}, false, state.nextProbe
	}
	c.nextReservation++
	state.reservation = c.nextReservation
	c.states[key] = state
	return repositoryFeatureProbe{
		cooldowns:   c,
		key:         key,
		generation:  state.generation,
		reservation: state.reservation,
	}, true, time.Time{}
}

func (c *repositoryFeatureCooldowns) beginProbe(
	repo RepoRef,
	feature string,
	now time.Time,
	bypass repositoryFeatureCooldownBypass,
	bypassEnabled bool,
) (repositoryFeatureProbe, bool) {
	probe, due, _ := c.beginProbeWithRetry(repo, feature, now, bypass, bypassEnabled)
	return probe, due
}

func (c *repositoryFeatureCooldowns) currentGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nextGeneration
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

func (c *repositoryFeatureCooldowns) clearRepository(repo RepoRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	target := repositoryFeatureKey(repo, "")
	for key := range c.states {
		if key.platform == target.platform &&
			key.host == target.host &&
			key.repoPath == target.repoPath {
			delete(c.states, key)
		}
	}
}

func (probe repositoryFeatureProbe) release() {
	defer probe.releaseIncarnationGate()
	if probe.bypass {
		probe.clear()
		return
	}
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

func (probe repositoryFeatureProbe) abandon() {
	defer probe.releaseIncarnationGate()
	if probe.cooldowns == nil || probe.reservation == 0 {
		return
	}
	probe.cooldowns.mu.Lock()
	defer probe.cooldowns.mu.Unlock()
	state, ok := probe.cooldowns.states[probe.key]
	if !ok || state.generation != probe.generation || state.reservation != probe.reservation {
		return
	}
	state.reservation = 0
	probe.cooldowns.states[probe.key] = state
}

func (probe repositoryFeatureProbe) clear() {
	defer probe.releaseIncarnationGate()
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

func (probe repositoryFeatureProbe) releaseIncarnationGate() {
	if probe.gate == nil || probe.gate.gate == nil {
		return
	}
	probe.gate.once.Do(probe.gate.gate.RUnlock)
}

func withRepoIncarnationGateHeld(ctx context.Context, repo RepoRef) context.Context {
	return context.WithValue(ctx, repoIncarnationGateHeldKey{}, repoPriorityKey(repo))
}

func repoIncarnationGateHeld(ctx context.Context, repo RepoRef) bool {
	key, _ := ctx.Value(repoIncarnationGateHeldKey{}).(string)
	return key == repoPriorityKey(repo)
}

func (s *Syncer) beginRepositoryFeatureProbe(
	ctx context.Context,
	repo RepoRef,
	feature string,
) (repositoryFeatureProbe, bool) {
	var gate *sync.RWMutex
	if !repoIncarnationGateHeld(ctx, repo) {
		gate = s.repoIncarnationGate(repo)
		gate.RLock()
	}
	bypass, bypassEnabled := repositoryFeatureCooldownBypassFromContext(ctx)
	probe, due := s.featureCooldowns.beginProbe(
		repo, feature, s.now().UTC(), bypass, bypassEnabled,
	)
	return attachFeatureProbeIncarnationGate(probe, due, gate)
}

func (s *Syncer) beginRepositoryFeatureProbeWithRetry(
	ctx context.Context,
	repo RepoRef,
	feature string,
) (repositoryFeatureProbe, bool, time.Time) {
	var gate *sync.RWMutex
	if !repoIncarnationGateHeld(ctx, repo) {
		gate = s.repoIncarnationGate(repo)
		gate.RLock()
	}
	bypass, bypassEnabled := repositoryFeatureCooldownBypassFromContext(ctx)
	probe, due, retryAt := s.featureCooldowns.beginProbeWithRetry(
		repo, feature, s.now().UTC(), bypass, bypassEnabled,
	)
	probe, due = attachFeatureProbeIncarnationGate(probe, due, gate)
	return probe, due, retryAt
}

func attachFeatureProbeIncarnationGate(
	probe repositoryFeatureProbe,
	due bool,
	gate *sync.RWMutex,
) (repositoryFeatureProbe, bool) {
	if gate == nil {
		return probe, due
	}
	if !due {
		gate.RUnlock()
		return probe, false
	}
	probe.gate = &repositoryFeatureProbeGate{gate: gate}
	return probe, true
}

func (s *Syncer) recordRepositoryFeatureDisabled(repo RepoRef, feature string, err error) bool {
	_, recorded := s.recordRepositoryFeatureDisabledUntil(repo, feature, err)
	return recorded
}

func (s *Syncer) recordRepositoryFeatureDisabledUntil(
	repo RepoRef,
	feature string,
	err error,
) (time.Time, bool) {
	var platformErr *platform.Error
	if !errors.As(err, &platformErr) ||
		platformErr.Code != platform.ErrCodeRepositoryFeatureDisabled ||
		platformErr.Capability != feature {
		return time.Time{}, false
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
	return nextProbe, true
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
