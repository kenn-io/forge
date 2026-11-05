package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	gh "github.com/google/go-github/v93/github"

	"go.kenn.io/forge/githubapp"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitealike"
)

const (
	// providerStartRetryInitial is the first wait after a transient start
	// failure that names no retry time; each later failure doubles it.
	providerStartRetryInitial = 30 * time.Second
	providerStartRetryMax     = 15 * time.Minute
	// providerStartResetMax caps a provider-directed wait, matching the cap
	// on GitHub App token mint retries.
	providerStartResetMax = time.Hour
)

// providerStartFailure is the verdict on why a provider host failed to start.
type providerStartFailure struct {
	// transient reports a failure that can clear without a configuration
	// change: a rate limit, a network failure, or a provider server error.
	transient bool
	// reason is shown to users, so it never quotes the provider's response.
	reason string
	// resetAt is the provider's own retry time, when it gave one.
	resetAt time.Time
}

func classifyProviderStartFailure(err error, now time.Time) providerStartFailure {
	if errors.Is(err, tokenauth.ErrMissingToken) {
		return providerStartFailure{reason: "no credential is available"}
	}
	if rateErr, ok := errors.AsType[*gh.RateLimitError](err); ok {
		return providerStartFailure{
			transient: true, reason: "rate limit exceeded",
			resetAt: rateErr.Rate.Reset.UTC(),
		}
	}
	if abuseErr, ok := errors.AsType[*gh.AbuseRateLimitError](err); ok {
		failure := providerStartFailure{transient: true, reason: "rate limit exceeded"}
		if abuseErr.RetryAfter != nil {
			failure.resetAt = now.Add(*abuseErr.RetryAfter)
		}
		return failure
	}
	if statusErr, ok := errors.AsType[*githubapp.StatusError](err); ok {
		if resetAt := statusErr.RetryDeadline(now); !resetAt.IsZero() {
			return providerStartFailure{transient: true, reason: "rate limit exceeded", resetAt: resetAt}
		}
		return classifyProviderStatus(statusErr.StatusCode)
	}
	if responseErr, ok := errors.AsType[*gh.ErrorResponse](err); ok && responseErr.Response != nil {
		return classifyProviderStatus(responseErr.Response.StatusCode)
	}
	if httpErr, ok := errors.AsType[*gitealike.HTTPError](err); ok {
		return classifyProviderStatus(httpErr.StatusCode)
	}
	if platformErr, ok := errors.AsType[*platform.Error](err); ok &&
		platformErr.Code == platform.ErrCodeRateLimited {
		failure := providerStartFailure{transient: true, reason: "rate limit exceeded"}
		if platformErr.ResetAt != nil {
			failure.resetAt = *platformErr.ResetAt
		}
		return failure
	}
	if providerUnreachable(err) {
		return providerStartFailure{transient: true, reason: "the provider could not be reached"}
	}
	return providerStartFailure{reason: "it failed to start"}
}

func classifyProviderStatus(status int) providerStartFailure {
	switch {
	case status == http.StatusTooManyRequests:
		return providerStartFailure{transient: true, reason: "rate limit exceeded"}
	case status >= http.StatusInternalServerError:
		return providerStartFailure{transient: true, reason: "the provider returned a server error"}
	case status == http.StatusUnauthorized:
		return providerStartFailure{reason: "the provider rejected the credential"}
	case status == http.StatusForbidden:
		return providerStartFailure{reason: "the provider denied access"}
	default:
		return providerStartFailure{reason: "it failed to start"}
	}
}

// providerUnreachable reports network failures that can clear on their own,
// including an upstream closing the connection before it responds (io.EOF).
// TLS verification failures are left out: they need a configuration change.
func providerUnreachable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return true
	}
	urlErr, ok := errors.AsType[*url.Error](err)
	return ok && urlErr.Timeout()
}

// nextProviderStartAttempt is when to try a transient failure again: the
// provider's reset time when it gave one, otherwise a doubling wait.
func nextProviderStartAttempt(now time.Time, failure providerStartFailure, failures int) time.Time {
	if failure.resetAt.After(now) {
		if limit := now.Add(providerStartResetMax); failure.resetAt.After(limit) {
			return limit
		}
		return failure.resetAt
	}
	wait := providerStartRetryInitial
	for range failures {
		wait *= 2
		if wait >= providerStartRetryMax {
			return now.Add(providerStartRetryMax)
		}
	}
	return now.Add(wait)
}

// providerHostRecovery starts provider hosts that failed to start with the
// others. Each failed host gets one loop, so attempts for a host never
// overlap; a host whose failure cannot clear on its own stays unavailable.
type providerHostRecovery struct {
	database *db.DB
	// withConfig runs fn with the current configuration while settings saves
	// and reloads wait, so a host is built and published from one consistent
	// configuration and a budget ceiling saved meanwhile reaches its budgets.
	withConfig func(ctx context.Context, fn func(*config.Config) error) error
	set        *tokenauth.SourceSet
	factories  map[string]providerFactory
	resolver   github.IdentityResolver
	syncer     *github.Syncer
	quota      *github.QuotaRegistry
	git        *gitStartup
	// onStarted runs after a host joins the syncer, to resolve and sync the
	// repositories it serves.
	onStarted func(ctx context.Context, kind platform.Kind, host string)
	now       func() time.Time
	// wait blocks until at or ctx ends and reports whether at was reached.
	wait func(ctx context.Context, at time.Time) bool

	mu sync.Mutex
	// rateTrackers are the trackers of started hosts, flushed at shutdown.
	rateTrackers []*github.RateTracker
}

// begin records host's start failure on the syncer. When the failure can
// clear on its own it returns a loop that retries until the host starts.
func (r *providerHostRecovery) begin(key string, err error) func(context.Context) {
	failure := classifyProviderStartFailure(err, r.now())
	if !failure.transient {
		r.markUnavailable(key, failure, time.Time{}, err)
		return nil
	}
	next := nextProviderStartAttempt(r.now(), failure, 0)
	r.markUnavailable(key, failure, next, err)
	return func(ctx context.Context) { r.retry(ctx, key, next) }
}

func (r *providerHostRecovery) retry(ctx context.Context, key string, next time.Time) {
	for failures := 1; ; failures++ {
		if !r.wait(ctx, next) {
			return
		}
		err := r.start(ctx, key)
		if err == nil {
			kind, host := splitProviderHostKey(key)
			slog.Info("provider host started", "platform", kind, "host", host)
			return
		}
		if ctx.Err() != nil {
			return
		}
		failure := classifyProviderStartFailure(err, r.now())
		if !failure.transient {
			r.markUnavailable(key, failure, time.Time{}, err)
			return
		}
		next = nextProviderStartAttempt(r.now(), failure, failures)
		r.markUnavailable(key, failure, next, err)
	}
}

func (r *providerHostRecovery) markUnavailable(
	key string, failure providerStartFailure, retryAt time.Time, err error,
) {
	kind, host := splitProviderHostKey(key)
	r.syncer.SetProviderHostOutage(platform.Kind(kind), host, github.ProviderHostOutage{
		Reason: failure.reason, RetryAt: retryAt,
	})
	if retryAt.IsZero() {
		slog.Warn(
			"provider host unavailable until restart",
			"platform", kind, "host", host, "err", err,
		)
		return
	}
	slog.Warn(
		"provider host unavailable; retrying",
		"platform", kind, "host", host, "retry_at", retryAt, "err", err,
	)
}

// start builds the host's clients as startup does and adds them to the
// running syncer.
func (r *providerHostRecovery) start(ctx context.Context, key string) error {
	kind, host := splitProviderHostKey(key)
	if err := r.withConfig(ctx, func(cfg *config.Config) error {
		return r.publish(ctx, cfg, key)
	}); err != nil {
		return err
	}
	// Started outside withConfig: resolving repositories takes the reload
	// lock itself.
	r.onStarted(ctx, platform.Kind(kind), host)
	return nil
}

func (r *providerHostRecovery) publish(ctx context.Context, cfg *config.Config, key string) error {
	// Degraded resolution matches startup: broken archive credentials leave
	// the ordinary host usable.
	sources, failures, err := providerTokenSources(ctx, cfg, r.set, true, true, key)
	if err != nil {
		return err
	}
	if err := failures[key]; err != nil {
		return err
	}
	if sources[key] == nil {
		return fmt.Errorf("resolve provider host %s: %w", key, tokenauth.ErrMissingToken)
	}
	control, err := buildProviderControlPlaneWithQuota(
		ctx, r.database, cfg, r.set, sources, r.factories, r.resolver, r.quota,
	)
	if err != nil {
		return err
	}
	kind, host := splitProviderHostKey(key)
	if err := r.syncer.AddProviderHost(platform.Kind(kind), host, github.ProviderHostWiring{
		Providers:            control.registry.Providers(),
		RateTrackers:         control.rateTrackers,
		WriteRateTrackers:    control.writeRateTrackers,
		WriteGQLRateTrackers: control.writeGQLRateTrackers,
		Budgets:              control.budgets,
		Fetchers:             control.fetchers,
		Routers:              control.githubRouters,
		RatePrincipalLabels:  control.ratePrincipalLabels,
	}); err != nil {
		return err
	}
	r.mu.Lock()
	r.rateTrackers = append(r.rateTrackers, control.allRateTrackers...)
	r.mu.Unlock()
	r.git.ApplyProviderControlPlane(&control)
	return nil
}

// flushRateTrackers writes rate state of started hosts that has not reached
// the database yet.
func (r *providerHostRecovery) flushRateTrackers() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rt := range r.rateTrackers {
		rt.Flush()
	}
}

func waitUntil(ctx context.Context, at time.Time) bool {
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
