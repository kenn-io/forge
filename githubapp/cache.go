package githubapp

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.kenn.io/forge/platform"
)

var ErrStaleGeneration = errors.New("installation token credential generation invalidated")

// CacheKey is the complete identity of a requested installation token.
type CacheKey struct {
	Host           string
	AppID          int64
	InstallationID int64
	Generation     uint64
	Scope          TokenScope
}

type generationKey struct {
	host              string
	app, installation int64
	generation        uint64
}

type tokenKey struct {
	generationKey
	scope string
}

type tokenFlight struct {
	done  chan struct{}
	token InstallationToken
	err   error
	retry bool
}

// TokenCache shares bounded in-flight mints without coupling waiter cancellation.
// The application owns this cache's lifetime and any failure cooldown policy.
type TokenCache struct {
	mu          sync.Mutex
	now         func() time.Time
	refreshSkew time.Duration
	tokens      map[tokenKey]InstallationToken
	flights     map[tokenKey]*tokenFlight
	invalid     map[generationKey]bool
}

type CacheOption func(*TokenCache)

// WithRefreshSkew lets the caller choose its proactive refresh policy.
func WithRefreshSkew(skew time.Duration) CacheOption {
	return func(c *TokenCache) { c.refreshSkew = skew }
}

func NewTokenCache(now func() time.Time, options ...CacheOption) *TokenCache {
	c := &TokenCache{now: now, tokens: make(map[tokenKey]InstallationToken), flights: make(map[tokenKey]*tokenFlight), invalid: make(map[generationKey]bool)}
	for _, option := range options {
		option(c)
	}
	return c
}

func (key CacheKey) generationKey() (generationKey, error) {
	host, err := platform.NormalizeHost(platform.KindGitHub, key.Host)
	if err != nil || key.Host != host || key.AppID <= 0 || key.InstallationID <= 0 || key.Generation == 0 {
		return generationKey{}, errors.New("token cache requires canonical instance and positive App, installation and generation IDs")
	}
	return generationKey{host: host, app: key.AppID, installation: key.InstallationID, generation: key.Generation}, nil
}

// Token mints at most once for a scope/generation. Each mint is bounded by the
// initiating caller's deadline, but that caller's cancellation only removes its
// own wait. An invalidated generation can never publish or return a token.
func (c *TokenCache) Token(ctx context.Context, key CacheKey, mint func(context.Context) (InstallationToken, error)) (InstallationToken, error) {
	if err := ctx.Err(); err != nil {
		return InstallationToken{}, err
	}
	deadline, ok := ctx.Deadline()
	if !ok || c.now == nil || mint == nil {
		return InstallationToken{}, errors.New("token cache requires a caller deadline, clock and minter")
	}
	generation, err := key.generationKey()
	if err != nil {
		return InstallationToken{}, err
	}
	scope, err := key.Scope.cacheKey()
	if err != nil {
		return InstallationToken{}, err
	}
	k := tokenKey{generationKey: generation, scope: scope}
	c.mu.Lock()
	if c.invalid[generation] {
		c.mu.Unlock()
		return InstallationToken{}, ErrStaleGeneration
	}
	if token, ok := c.tokens[k]; ok && token.ExpiresAt.After(c.now().Add(c.refreshSkew)) {
		c.mu.Unlock()
		return token, nil
	}
	flight := c.flights[k]
	if flight == nil {
		flight = &tokenFlight{done: make(chan struct{})}
		c.flights[k] = flight
		runner, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		go func() {
			defer cancel()
			token, err := mint(runner)
			c.mu.Lock()
			defer c.mu.Unlock()
			flight.retry = err != nil && runner.Err() != nil && errors.Is(err, runner.Err())
			if c.invalid[generation] {
				err = ErrStaleGeneration
				flight.retry = false
			}
			if err == nil {
				if token.Token == "" || !token.ExpiresAt.After(c.now()) {
					err = errors.New("mint returned an empty or expired installation token")
				} else {
					c.tokens[k] = token
				}
			}
			if err != nil {
				token = InstallationToken{}
			}
			flight.token, flight.err = token, err
			delete(c.flights, k)
			close(flight.done)
		}()
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return InstallationToken{}, ctx.Err()
	case <-flight.done:
		c.mu.Lock()
		if c.invalid[generation] {
			c.mu.Unlock()
			return InstallationToken{}, ErrStaleGeneration
		}
		c.mu.Unlock()
		if flight.retry {
			return c.Token(ctx, key, mint)
		}
		return flight.token, flight.err
	}
}

// EvictCompleted removes a cached result without disturbing an ongoing mint.
// Use generation invalidation when the credential itself is no longer valid.
func (c *TokenCache) EvictCompleted(key CacheKey) {
	generation, err := key.generationKey()
	if err != nil {
		return
	}
	scope, err := key.Scope.cacheKey()
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tokens, tokenKey{generationKey: generation, scope: scope})
}

// InvalidateGeneration fences every token scope under this credential generation.
func (c *TokenCache) InvalidateGeneration(key CacheKey) {
	generation, err := key.generationKey()
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalid[generation] = true
	for k := range c.tokens {
		if k.generationKey == generation {
			delete(c.tokens, k)
		}
	}
}

// RejectToken evicts only the exact rejected token, never another completed
// token or a mint in progress under the same scope.
func (c *TokenCache) RejectToken(key CacheKey, rejected string) {
	generation, err := key.generationKey()
	if err != nil {
		return
	}
	scope, err := key.Scope.cacheKey()
	if err != nil {
		return
	}
	k := tokenKey{generationKey: generation, scope: scope}
	c.mu.Lock()
	defer c.mu.Unlock()
	if token, ok := c.tokens[k]; ok && token.Token == rejected {
		delete(c.tokens, k)
	}
}
