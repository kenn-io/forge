package github

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type QuotaResource string

const (
	QuotaResourceREST    QuotaResource = "rest"
	QuotaResourceGraphQL QuotaResource = "graphql"
)

// QuotaPool is one GitHub principal's live capacity for a single resource.
// GitHub meters REST and GraphQL separately per principal, so a route's App
// installation and the user's PAT hold independent pools on the same host.
type QuotaPool struct {
	Identity  IdentityKey
	Resource  QuotaResource
	Limit     int
	Remaining int
	ResetAt   time.Time
	UpdatedAt time.Time
	Requests  int
	Known     bool
}

type quotaKey struct {
	identity IdentityKey
	resource QuotaResource
}

type QuotaRegistry struct {
	mu    sync.RWMutex
	pools map[quotaKey]QuotaPool
	now   func() time.Time
}

func NewQuotaRegistry() *QuotaRegistry {
	return &QuotaRegistry{
		pools: make(map[quotaKey]QuotaPool),
		now:   time.Now,
	}
}

func (r *QuotaRegistry) ObserveHeaders(
	identity IdentityKey,
	resource QuotaResource,
	header http.Header,
) {
	if r == nil || identity.Principal == "" {
		return
	}
	key := newQuotaKey(identity, resource)
	r.mu.Lock()
	pool := r.pools[key]
	pool.Identity = key.identity
	pool.Resource = key.resource
	pool.Requests++
	if rate, ok := rateFromQuotaHeaders(header); ok {
		reset := rate.Reset.UTC()
		// Responses complete out of order, so a header issued earlier can
		// arrive after a later one. Within one reset window the provider's
		// remaining only falls, so a stale header must not hand back quota
		// that has already been spent. A new window replaces the pool
		// outright, and /rate_limit snapshots still reconcile it either way.
		if pool.Known && reset.Equal(pool.ResetAt) &&
			rate.Remaining > pool.Remaining {
			rate.Remaining = pool.Remaining
		}
		pool.Limit = rate.Limit
		pool.Remaining = rate.Remaining
		pool.ResetAt = reset
		pool.UpdatedAt = r.now().UTC()
		pool.Known = true
	}
	r.pools[key] = pool
	r.mu.Unlock()
}

func (r *QuotaRegistry) UpdateSnapshot(
	identity IdentityKey,
	resource QuotaResource,
	rate Rate,
) {
	if r == nil || identity.Principal == "" {
		return
	}
	key := newQuotaKey(identity, resource)
	r.mu.Lock()
	pool := r.pools[key]
	pool.Identity = key.identity
	pool.Resource = key.resource
	pool.Limit = rate.Limit
	pool.Remaining = rate.Remaining
	pool.ResetAt = rate.Reset.UTC()
	pool.UpdatedAt = r.now().UTC()
	pool.Known = rate.Limit >= 0 && rate.Remaining >= 0 && !rate.Reset.IsZero()
	r.pools[key] = pool
	r.mu.Unlock()
}

func (r *QuotaRegistry) Get(
	identity IdentityKey,
	resource QuotaResource,
) (QuotaPool, bool) {
	if r == nil {
		return QuotaPool{}, false
	}
	r.mu.RLock()
	pool, ok := r.pools[newQuotaKey(identity, resource)]
	r.mu.RUnlock()
	return pool, ok
}

func (r *QuotaRegistry) Snapshot() []QuotaPool {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	pools := make([]QuotaPool, 0, len(r.pools))
	for _, pool := range r.pools {
		pools = append(pools, pool)
	}
	r.mu.RUnlock()
	slices.SortFunc(pools, func(a, b QuotaPool) int {
		if cmp := strings.Compare(a.Identity.Host, b.Identity.Host); cmp != 0 {
			return cmp
		}
		if cmp := strings.Compare(a.Identity.Principal, b.Identity.Principal); cmp != 0 {
			return cmp
		}
		return strings.Compare(string(a.Resource), string(b.Resource))
	})
	return pools
}

// QuotaAvailability reports one credential's headroom across the requested
// resources. Known and Exhausted are tracked independently: a resource whose
// pool has never been observed makes the answer unknown, but it must not hide
// a different resource that is known to be at its reserve. Callers that treat
// unknown as permission to proceed must therefore also check Exhausted.
type QuotaAvailability struct {
	Allowed   bool
	Known     bool
	Exhausted bool
	ResetAt   *time.Time
}

// AllowedOrUnobserved reports whether background work may proceed. Unknown
// quota is permission to proceed — ordinary response headers are what populate
// the registry — but only while no observed resource sits at its reserve.
func (a QuotaAvailability) AllowedOrUnobserved() bool {
	return a.Allowed || (!a.Known && !a.Exhausted)
}

func (r *QuotaRegistry) CheckReserve(
	identity IdentityKey,
	resources []QuotaResource,
	cost int,
	reserve int,
) QuotaAvailability {
	availability := QuotaAvailability{Allowed: true, Known: true}
	now := r.now().UTC()
	for _, resource := range resources {
		pool, ok := r.Get(identity, resource)
		if !ok || !pool.Known || pool.ResetAt.IsZero() || !pool.ResetAt.After(now) {
			availability.Allowed = false
			availability.Known = false
			continue
		}
		if pool.Remaining-cost < reserve {
			availability.Allowed = false
			availability.Exhausted = true
			if !pool.ResetAt.IsZero() &&
				(availability.ResetAt == nil || pool.ResetAt.After(*availability.ResetAt)) {
				reset := pool.ResetAt
				availability.ResetAt = &reset
			}
		}
	}
	return availability
}

func (r *QuotaRegistry) EarliestReset(
	identity IdentityKey,
	resources []QuotaResource,
) *time.Time {
	var earliest *time.Time
	for _, resource := range resources {
		pool, ok := r.Get(identity, resource)
		if !ok || !pool.Known || pool.ResetAt.IsZero() {
			continue
		}
		if earliest == nil || pool.ResetAt.Before(*earliest) {
			reset := pool.ResetAt
			earliest = &reset
		}
	}
	return earliest
}

func newQuotaKey(identity IdentityKey, resource QuotaResource) quotaKey {
	return quotaKey{
		identity: IdentityKey{
			Host:      canonicalRepoHost(identity.Host),
			Principal: identity.Principal,
		},
		resource: resource,
	}
}

func rateFromQuotaHeaders(header http.Header) (Rate, bool) {
	if header == nil {
		return Rate{}, false
	}
	limit, err := strconv.Atoi(header.Get("X-RateLimit-Limit"))
	if err != nil {
		return Rate{}, false
	}
	remaining, err := strconv.Atoi(header.Get("X-RateLimit-Remaining"))
	if err != nil {
		return Rate{}, false
	}
	resetUnix, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return Rate{}, false
	}
	return Rate{
		Limit: limit, Remaining: remaining, Reset: time.Unix(resetUnix, 0).UTC(),
	}, true
}

type quotaResourceContextKey struct{}

func withQuotaResource(ctx context.Context, resource QuotaResource) context.Context {
	return context.WithValue(ctx, quotaResourceContextKey{}, resource)
}

func quotaResourceFromContext(ctx context.Context, fallback QuotaResource) QuotaResource {
	resource, ok := ctx.Value(quotaResourceContextKey{}).(QuotaResource)
	if !ok || resource == "" {
		return fallback
	}
	return resource
}

// quotaTransport attributes response rate-limit headers to the principal that
// authenticated the request. Each of a route's transport chains (read,
// mutation, notification) is built with a fixed identity, so attribution comes
// from the chain rather than from inspecting the resolved token.
type quotaTransport struct {
	base     http.RoundTripper
	registry *QuotaRegistry
	identity IdentityKey
	resource QuotaResource
}

func (t *quotaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if resp == nil || t.registry == nil || t.identity.Principal == "" {
		return resp, err
	}
	t.registry.ObserveHeaders(
		t.identity,
		quotaResourceFromContext(req.Context(), t.resource),
		resp.Header,
	)
	return resp, err
}
