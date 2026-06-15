package server

import (
	"context"
	"slices"
	"sync/atomic"
	"time"

	"go.kenn.io/middleman/internal/config"
)

// fleetPlatformAuthInterval is how often the monitor re-resolves the host's
// platform-authentication signal. Auth state changes rarely (a `gh auth login`
// or a token rotation), so the cadence is slow; the cached value keeps the
// snapshot read path free of the token-resolution subprocess.
const fleetPlatformAuthInterval = 60 * time.Second

// fleetPlatformAuthMonitor keeps the local host's platform-authentication
// signal fresh for the fleet snapshot. On a fixed interval it resolves whether
// a GitHub token is available, caching the result so buildLocalRaw can fill
// RawSnapshot.PlatformAuthenticated without resolving a token (which may shell
// out to `gh auth token`) on the read path. The cached pointer is nil until the
// first resolve completes; the snapshot reports that unknown state as omitted,
// which the diagnostics layer treats as "do not warn".
type fleetPlatformAuthMonitor struct {
	resolve  func() bool
	interval time.Duration
	result   atomic.Pointer[bool]
}

func newFleetPlatformAuthMonitor(
	snapshot func() *config.Config,
) *fleetPlatformAuthMonitor {
	return &fleetPlatformAuthMonitor{
		resolve:  platformAuthResolver(snapshot),
		interval: fleetPlatformAuthInterval,
	}
}

// platformAuthResolver reports whether any configured platform host can
// resolve a usable token, matching the snapshot's notion of platform
// authentication: a resolvable token (repo-level token_env, [[platforms]]
// token_env, default env var, else `gh auth token` for GitHub hosts) means
// the platform backend can act. It walks the same per-repo and per-platform
// token path provider startup uses, so token_env overrides and self-hosted
// or non-default hosts are not misreported as unauthenticated. A nil config
// is never authenticated.
//
// snapshot must return a config detached from concurrent reloads
// (the resolver runs on its own goroutine and token resolution can
// shell out, so it must not read live reload-mutated fields).
func platformAuthResolver(snapshot func() *config.Config) func() bool {
	return func() bool {
		cfg := snapshot()
		if cfg == nil {
			return false
		}
		type platformHost struct{ platform, host, tokenEnv string }
		seen := map[platformHost]bool{}
		hasToken := func(platform, host, tokenEnv string) bool {
			key := platformHost{platform, host, tokenEnv}
			if seen[key] {
				return false
			}
			seen[key] = true
			return cfg.TokenForPlatformHost(platform, host, tokenEnv) != ""
		}
		for _, repo := range cfg.Repos {
			if hasToken(
				repo.PlatformOrDefault(), repo.PlatformHostOrDefault(),
				repo.TokenEnv,
			) {
				return true
			}
		}
		for _, platform := range cfg.Platforms {
			if hasToken(platform.Type, platform.Host, "") {
				return true
			}
		}
		return hasToken("github", cfg.DefaultPlatformHost, "")
	}
}

// run resolves once immediately so a freshly started daemon reports auth
// without waiting a full interval, then re-resolves on the interval until ctx
// is cancelled.
func (m *fleetPlatformAuthMonitor) run(ctx context.Context) {
	if m == nil || m.resolve == nil {
		return
	}
	m.runOnce()
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runOnce()
		}
	}
}

func (m *fleetPlatformAuthMonitor) runOnce() {
	authed := m.resolve()
	m.result.Store(&authed)
}

// authenticated returns the cached signal, or nil when no resolve has completed
// yet (unknown auth state).
func (m *fleetPlatformAuthMonitor) authenticated() *bool {
	if m == nil {
		return nil
	}
	return m.result.Load()
}

// snapshotPlatformAuthConfig copies, under cfgMu, exactly the fields
// the platform-auth resolver reads (config reload rewrites them in
// place), so resolution — which can shell out to `gh auth token` —
// runs against a detached view and never holds the lock.
func (s *Server) snapshotPlatformAuthConfig() *config.Config {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.cfg == nil {
		return nil
	}
	return &config.Config{
		GitHubTokenEnv:      s.cfg.GitHubTokenEnv,
		DefaultPlatformHost: s.cfg.DefaultPlatformHost,
		Repos:               slices.Clone(s.cfg.Repos),
		Platforms:           slices.Clone(s.cfg.Platforms),
	}
}
