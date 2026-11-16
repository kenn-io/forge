package github

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/platform"
)

type QuotaResource string

const (
	QuotaResourceREST    QuotaResource = "rest"
	QuotaResourceGraphQL QuotaResource = "graphql"
)

// QuotaPool is one GitHub principal's live capacity for a single resource.
// GitHub meters REST and GraphQL separately per principal, so a route's App
// installation and the user's PAT hold independent pools on the same host.
//
// GitHub can report more than one live rate-limit window for a pool, each with
// its own reset time and count, and answer requests from each in turn. The
// pool follows the window with the least remaining: a request that window
// serves is refused when it runs out, whatever the others have left.
type QuotaPool struct {
	Identity    IdentityKey
	Resource    QuotaResource
	Limit       int
	Remaining   int
	ResetAt     time.Time
	UpdatedAt   time.Time
	Requests    int
	AttemptCost int // largest observed Remaining decrease per response in this window
	// Reserved is the archive spend reserved in this window that no response
	// has confirmed yet. Reads fill it in; the registry keeps it separately.
	Reserved int
	Known    bool
}

type quotaKey struct {
	identity IdentityKey
	resource QuotaResource
}

type quotaReservationKey struct {
	quotaKey
	resetAt time.Time
}

type quotaReservation struct {
	key  quotaReservationKey
	cost int
	pool QuotaPool
}

type QuotaRegistry struct {
	mu           sync.RWMutex
	pools        map[quotaKey]QuotaPool
	reservations map[quotaReservationKey]int
	now          func() time.Time
}

// quotaWindowLength is how long a GitHub primary rate-limit window lasts.
const quotaWindowLength = time.Hour

// archiveQuotaFloor is the remaining quota a pool must hold at now for an
// archive item to start. It falls in a straight line from the pool's whole
// limit when its window opens to the archive reserve when the window resets.
//
// Archive work therefore starts only on what live sync has left unused so far
// in the window. A fixed reserve alone lets a fast archive take everything
// above it in the first minutes, and live sync then has the reserve to last
// the rest of the hour, which a busy instance outruns.
//
// The floor gates the start of an item and never an attempt inside one. An
// item that has started may spend down to the reserve (reserveArchiveAttempt).
// In steady state the room above the floor is about one item's declared cost,
// so an item larger than that would be cut off, lose the pages it had read,
// and be cut off again on every retry. A large item instead takes the pool
// below the floor, and the next item waits until the floor has fallen past it.
func archiveQuotaFloor(pool QuotaPool, now time.Time) int {
	reserve := ArchiveProviderReserve(pool.Limit)
	left := min(pool.ResetAt.Sub(now), quotaWindowLength)
	if left <= 0 || pool.Limit <= reserve {
		return reserve
	}
	paced := float64(pool.Limit-reserve) * float64(left) / float64(quotaWindowLength)
	return reserve + int(math.Floor(paced))
}

// quotaResetJitter is how far apart two reset times may be and still name one
// quota window. GitHub's reset header can differ by a second between responses
// in the same window, while the next window resets about an hour later.
const quotaResetJitter = 5 * time.Second

// compareQuotaWindow orders the window an observed reset names against the
// current one: negative for an earlier window, zero for the same window, and
// positive for a later one.
func compareQuotaWindow(observed, current time.Time) int {
	switch difference := observed.Sub(current); {
	case difference < -quotaResetJitter:
		return -1
	case difference > quotaResetJitter:
		return 1
	default:
		return 0
	}
}

func NewQuotaRegistry() *QuotaRegistry {
	return &QuotaRegistry{
		pools:        make(map[quotaKey]QuotaPool),
		reservations: make(map[quotaReservationKey]int),
		now:          time.Now,
	}
}

// SetClock replaces the registry's time source. Windows and the archive floor
// span an hour, so callers that need to observe them inject a clock instead of
// waiting. It must not be called while requests are in flight.
func (r *QuotaRegistry) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
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
		now := r.now().UTC()
		reset := rate.Reset.UTC()
		// Responses complete out of order, so a header issued earlier can
		// arrive after a later one. Inside the pool's window the count may
		// only fall, because the provider's remaining only falls there. The
		// pool keeps its first reset time for the whole window, because
		// archive reservations are keyed by it. /rate_limit snapshots still
		// reconcile the count in both directions.
		switch placeQuotaReading(pool, rate, now) {
		case quotaReadingSameWindow:
			if rate.Remaining < pool.Remaining {
				pool.AttemptCost = max(pool.AttemptCost, pool.Remaining-rate.Remaining)
				pool.Remaining = rate.Remaining
			}
			pool.Limit = rate.Limit
			pool.UpdatedAt = now
		case quotaReadingFreshWindow:
			r.clearEndedReservationsLocked(key, now)
			pool.AttemptCost = 1
			fallthrough
		case quotaReadingTighterWindow:
			pool.Limit = rate.Limit
			pool.Remaining = rate.Remaining
			pool.ResetAt = reset
			pool.UpdatedAt = now
			pool.Known = true
		case quotaReadingIgnored:
			// Drop the observation, but the request itself still happened.
		}
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
	reset := rate.Reset.UTC()
	known := rate.Limit >= 0 && rate.Remaining >= 0 && !rate.Reset.IsZero()
	r.mu.Lock()
	pool := r.pools[key]
	pool.Identity = key.identity
	pool.Resource = key.resource
	// Within the pool's window a snapshot is authoritative in both directions,
	// unlike a response header. It is the endpoint whose whole job is to state
	// the truth, refreshes are claimed so only one is in flight per credential,
	// and it is how a pool that headers clamped too low recovers before the
	// window rolls.
	//
	// A snapshot of any other window is one more reading of it, placed like a
	// header. That covers a delayed /rate_limit response for a window that has
	// closed: rewinding to it would make the pool read as expired, and an
	// expired pool reads as unobserved, which admits background work the
	// current window may have already ruled out.
	now := r.now().UTC()
	placement := quotaReadingFreshWindow
	if known {
		placement = placeQuotaReading(pool, rate, now)
	}
	switch placement {
	case quotaReadingSameWindow:
	case quotaReadingFreshWindow:
		r.clearEndedReservationsLocked(key, now)
		pool.AttemptCost = 1
		pool.ResetAt = reset
	case quotaReadingTighterWindow:
		pool.ResetAt = reset
	case quotaReadingIgnored:
		r.mu.Unlock()
		return
	}
	pool.Limit = rate.Limit
	pool.Remaining = rate.Remaining
	pool.UpdatedAt = now
	pool.Known = known
	r.pools[key] = pool
	r.mu.Unlock()
}

// quotaReading says where a provider reading belongs relative to the pool.
type quotaReading int

const (
	// quotaReadingIgnored is a reading that says nothing about the pool.
	quotaReadingIgnored quotaReading = iota
	// quotaReadingSameWindow is another reading of the window the pool follows.
	quotaReadingSameWindow
	// quotaReadingFreshWindow starts the pool over: it had no live window.
	quotaReadingFreshWindow
	// quotaReadingTighterWindow is a second live window with less remaining.
	quotaReadingTighterWindow
)

// placeQuotaReading decides what a reading of the window ending at rate.Reset
// means for the pool.
//
// A pool whose window has ended, or that has none, takes the next window the
// provider reports. While the pool's window is open, a reading of any other
// window comes from a second live one. The pool moves to it only when it is
// still open and has less remaining. That leaves alone a second window with
// more room, a late response from a window that has closed, and a window with
// nothing spent yet, whose reset is an hour after each request and so always
// looks like the newest.
func placeQuotaReading(pool QuotaPool, rate Rate, now time.Time) quotaReading {
	switch {
	case pool.Known && compareQuotaWindow(rate.Reset, pool.ResetAt) == 0:
		return quotaReadingSameWindow
	case !pool.Known || !pool.ResetAt.After(now):
		return quotaReadingFreshWindow
	case rate.Reset.After(now) && rate.Remaining < pool.Remaining:
		return quotaReadingTighterWindow
	default:
		return quotaReadingIgnored
	}
}

func (r *QuotaRegistry) Get(
	identity IdentityKey,
	resource QuotaResource,
) (QuotaPool, bool) {
	if r == nil {
		return QuotaPool{}, false
	}
	key := newQuotaKey(identity, resource)
	r.mu.RLock()
	pool, ok := r.pools[key]
	pool.Reserved = r.reservedLocked(key)
	r.mu.RUnlock()
	return pool, ok
}

func (r *QuotaRegistry) Snapshot() []QuotaPool {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	pools := make([]QuotaPool, 0, len(r.pools))
	for key, pool := range r.pools {
		pool.Reserved = r.reservedLocked(key)
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

// QuotaPacingResource is one resource pool's view inside a pacing window.
type QuotaPacingResource struct {
	Limit     int
	Remaining int
	// Headroom is remaining minus this pool's own archive floor
	// (`archiveQuotaFloor`), which falls through the window to the pool's
	// archive reserve. Negative when the pool sits below the floor.
	Headroom int
	ResetAt  time.Time
}

type QuotaPacingWindow struct {
	Limit     int
	Remaining int
	// ArchiveHeadroom is the archive spend this credential may admit: the
	// minimum across required resources of remaining minus that pool's own
	// archive floor. Floors are per pool — applying the smallest pool's
	// floor to a larger pool would admit spend below the larger pool's
	// own. Negative when the binding pool sits below its floor.
	ArchiveHeadroom int
	ResetAt         time.Time
	Resources       map[QuotaResource]QuotaPacingResource
}

// ArchiveAllowance is what an archive item that has started may spend: the
// room the tightest pool has above its own archive reserve.
func (w QuotaPacingWindow) ArchiveAllowance() int {
	allowance, found := 0, false
	for _, resource := range w.Resources {
		room := resource.Remaining - ArchiveProviderReserve(resource.Limit)
		if !found || room < allowance {
			allowance, found = room, true
		}
	}
	return max(allowance, 0)
}

// ArchiveRetryAt returns the earliest time every pool whose headroom, read at
// now, is below cost can have enough: when its falling floor has made room, or
// its reset if that comes first, which is also where a pool below its reserve
// recovers. Waiting for the window-wide latest reset would leave archives
// paused after the short pool has already recovered. Zero when no pool is
// deficient.
func (w QuotaPacingWindow) ArchiveRetryAt(cost int, now time.Time) time.Time {
	var retry time.Time
	for _, resource := range w.Resources {
		if resource.Headroom >= cost {
			continue
		}
		ready := resource.ResetAt
		if paced := resource.Limit - ArchiveProviderReserve(resource.Limit); paced > 0 {
			wait := time.Duration(float64(cost-resource.Headroom) /
				float64(paced) * float64(quotaWindowLength))
			if at := now.Add(wait); at.Before(ready) {
				ready = at
			}
		}
		if ready.After(retry) {
			retry = ready
		}
	}
	return retry
}

// ArchiveBindingResource returns the pool with the least archive headroom —
// the constraint that currently limits archive admission — with a
// lexicographic tie-break for determinism.
func (w QuotaPacingWindow) ArchiveBindingResource() (QuotaResource, QuotaPacingResource) {
	var bindingName QuotaResource
	var binding QuotaPacingResource
	first := true
	for name, resource := range w.Resources {
		if first || resource.Headroom < binding.Headroom ||
			(resource.Headroom == binding.Headroom && name < bindingName) {
			bindingName = name
			binding = resource
			first = false
		}
	}
	return bindingName, binding
}

func (r *QuotaRegistry) PacingWindow(
	identity IdentityKey,
	resources []QuotaResource,
) (QuotaPacingWindow, bool) {
	if r == nil || len(resources) == 0 {
		return QuotaPacingWindow{}, false
	}
	now := r.now().UTC()
	r.mu.RLock()
	defer r.mu.RUnlock()
	window := QuotaPacingWindow{
		Resources: make(map[QuotaResource]QuotaPacingResource, len(resources)),
	}
	for index, resource := range resources {
		key := newQuotaKey(identity, resource)
		pool, ok := r.pools[key]
		if !ok || !pool.Known || pool.Limit <= 0 || pool.Remaining < 0 ||
			pool.ResetAt.IsZero() || !pool.ResetAt.After(now) {
			return QuotaPacingWindow{}, false
		}
		if window.Limit == 0 || pool.Limit < window.Limit {
			window.Limit = pool.Limit
		}
		remaining := r.effectiveRemainingLocked(key, pool)
		if index == 0 || remaining < window.Remaining {
			window.Remaining = remaining
		}
		headroom := remaining - archiveQuotaFloor(pool, now)
		if index == 0 || headroom < window.ArchiveHeadroom {
			window.ArchiveHeadroom = headroom
		}
		if pool.ResetAt.After(window.ResetAt) {
			window.ResetAt = pool.ResetAt
		}
		window.Resources[resource] = QuotaPacingResource{
			Limit:     pool.Limit,
			Remaining: remaining,
			Headroom:  headroom,
			ResetAt:   pool.ResetAt,
		}
	}
	return window, true
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
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, resource := range resources {
		key := newQuotaKey(identity, resource)
		pool, ok := r.pools[key]
		if !ok || !pool.Known || pool.ResetAt.IsZero() || !pool.ResetAt.After(now) {
			availability.Allowed = false
			availability.Known = false
			continue
		}
		if r.effectiveRemainingLocked(key, pool)-cost < reserve {
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

// reserveArchiveAttempt reserves one wire attempt's cost against the target
// resource pool. Every required pool must stay above its own archive reserve
// (`ArchiveProviderReserve` of that pool's limit) — reserves are per pool, so
// a larger pool keeps its proportionally larger one. The falling floor is not
// checked here: it decides when an item starts (`archiveQuotaFloor`).
func (r *QuotaRegistry) reserveArchiveAttempt(
	identity IdentityKey,
	resources []QuotaResource,
	resource QuotaResource,
) (quotaReservation, bool) {
	if r == nil || identity.Principal == "" {
		return quotaReservation{}, false
	}
	now := r.now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()

	var targetKey quotaKey
	var target QuotaPool
	for _, required := range resources {
		key := newQuotaKey(identity, required)
		pool, ok := r.pools[key]
		if !ok || !pool.Known || pool.ResetAt.IsZero() || !pool.ResetAt.After(now) {
			return quotaReservation{}, false
		}
		remaining := r.effectiveRemainingLocked(key, pool)
		if required == resource {
			targetKey = key
			target = pool
			continue
		}
		if remaining <= ArchiveProviderReserve(pool.Limit) {
			return quotaReservation{}, false
		}
	}
	if target.Resource == "" {
		return quotaReservation{}, false
	}
	cost := max(target.AttemptCost, 1)
	if r.effectiveRemainingLocked(targetKey, target)-cost < ArchiveProviderReserve(target.Limit) {
		return quotaReservation{}, false
	}
	key := quotaReservationKey{quotaKey: targetKey, resetAt: target.ResetAt.UTC()}
	r.reservations[key] += cost
	return quotaReservation{key: key, cost: cost, pool: target}, true
}

func (r *QuotaRegistry) releaseArchiveReservation(reservation quotaReservation) {
	if r == nil || reservation.cost <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseArchiveReservationLocked(reservation)
}

func (r *QuotaRegistry) releaseArchiveReservationLocked(reservation quotaReservation) {
	remaining := r.reservations[reservation.key] - reservation.cost
	if remaining > 0 {
		r.reservations[reservation.key] = remaining
	} else {
		delete(r.reservations, reservation.key)
	}
}

func (r *QuotaRegistry) effectiveRemainingLocked(key quotaKey, pool QuotaPool) int {
	return max(pool.Remaining-r.reservedLocked(key), 0)
}

// reservedLocked is the archive spend no response has accounted for yet. A
// reservation is recorded under the window the pool followed when it was
// made and lasts until that window ends, so it still counts after the pool
// moves to another live window.
func (r *QuotaRegistry) reservedLocked(key quotaKey) int {
	now := r.now().UTC()
	reserved := 0
	for reservation, cost := range r.reservations {
		if reservation.quotaKey == key && reservation.resetAt.After(now) {
			reserved += cost
		}
	}
	return reserved
}

func (r *QuotaRegistry) clearEndedReservationsLocked(key quotaKey, now time.Time) {
	for reservation := range r.reservations {
		if reservation.quotaKey == key && !reservation.resetAt.After(now) {
			delete(r.reservations, reservation)
		}
	}
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
	resource := quotaResourceFromContext(req.Context(), t.resource)
	guardedReq, reservation, allowed := reserveArchiveProviderAttempt(
		req, t.registry, t.identity, resource,
	)
	if !allowed {
		return nil, platform.ErrArchiveAttemptBudget
	}
	resp, err := t.base.RoundTrip(guardedReq)
	if errors.Is(err, platform.ErrSyncBudgetExhausted) ||
		errors.Is(err, platform.ErrArchiveAttemptBudget) {
		t.registry.releaseArchiveReservation(reservation.quota)
		return resp, err
	}
	var header http.Header
	if resp == nil || t.registry == nil || t.identity.Principal == "" {
		reconcileArchiveProviderAttempt(reservation, t.registry, header)
		return resp, err
	}
	header = resp.Header
	t.registry.ObserveHeaders(
		t.identity,
		resource,
		header,
	)
	reconcileArchiveProviderAttempt(reservation, t.registry, header)
	return resp, err
}
