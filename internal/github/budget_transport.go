package github

import (
	"context"
	"net/http"
	"slices"
	"sync/atomic"

	"go.kenn.io/middleman/internal/platform"
)

type syncBudgetKey struct{}
type archiveSyncBudgetKey struct{}
type archiveAttemptAllowanceKey struct{}
type archiveProviderAttemptReservationKey struct{}

// archiveAttemptAllowance is a shared, mutable counter of the provider capacity
// an admitted archive request is still allowed to consume. Headerless providers
// debit one unit per wire attempt. GitHub transports reserve the resource's
// largest observed same-window attempt cost and reconcile larger response
// deltas, so SDK and authentication retries cannot outrun live quota headroom.
type archiveAttemptAllowance struct {
	remaining atomic.Int64
	provider  *archiveProviderAttemptConfig
}

type archiveProviderAttemptConfig struct {
	identity  IdentityKey
	resources []QuotaResource
	reserve   int
	budget    *SyncBudget
}

type archiveProviderAttemptReservation struct {
	allowance *archiveAttemptAllowance
	before    QuotaPool
	cost      int
}

// WithArchiveAttemptAllowance attaches a per-request wire-attempt allowance to
// ctx alongside the archive-budget marker. Admission sets attempts to the
// admitted archive cost so the ceiling enforced at admission is also enforced
// atomically at every wire attempt.
func WithArchiveAttemptAllowance(ctx context.Context, attempts int) context.Context {
	allowance := &archiveAttemptAllowance{}
	allowance.remaining.Store(int64(attempts))
	return context.WithValue(ctx, archiveAttemptAllowanceKey{}, allowance)
}

// WithArchiveProviderAttemptAllowance adds live provider-pool enforcement to
// the admitted allowance. quotaTransport reserves capacity before every wire
// attempt and updates the effective cost from response headers.
func WithArchiveProviderAttemptAllowance(
	ctx context.Context,
	attempts int,
	identity IdentityKey,
	resources []QuotaResource,
	reserve int,
	budget *SyncBudget,
) context.Context {
	allowance := &archiveAttemptAllowance{
		provider: &archiveProviderAttemptConfig{
			identity: identity, resources: slices.Clone(resources), reserve: max(reserve, 0),
			budget: budget,
		},
	}
	allowance.remaining.Store(int64(attempts))
	return context.WithValue(ctx, archiveAttemptAllowanceKey{}, allowance)
}

// ConsumeArchiveAttemptAllowance reserves one wire attempt from the archive
// allowance in ctx. It returns true when the attempt is permitted and false
// once the allowance is exhausted, in which case the caller must refuse the
// attempt without any upstream I/O. Contexts without an allowance — every live
// (non-archive) request — always permit the attempt.
func ConsumeArchiveAttemptAllowance(ctx context.Context) bool {
	allowance, ok := ctx.Value(archiveAttemptAllowanceKey{}).(*archiveAttemptAllowance)
	if !ok {
		return true
	}
	if reserved, _ := ctx.Value(archiveProviderAttemptReservationKey{}).(*archiveAttemptAllowance); reserved == allowance {
		return true
	}
	return allowance.consume(1)
}

func (a *archiveAttemptAllowance) consume(cost int) bool {
	for {
		remaining := a.remaining.Load()
		if cost <= 0 || remaining < int64(cost) {
			return false
		}
		if a.remaining.CompareAndSwap(remaining, remaining-int64(cost)) {
			return true
		}
	}
}

func (a *archiveAttemptAllowance) debit(cost int) {
	for cost > 0 {
		remaining := a.remaining.Load()
		next := max(remaining-int64(cost), 0)
		if a.remaining.CompareAndSwap(remaining, next) {
			return
		}
	}
}

func reserveArchiveProviderAttempt(
	req *http.Request,
	registry *QuotaRegistry,
	identity IdentityKey,
	resource QuotaResource,
) (*http.Request, archiveProviderAttemptReservation, bool) {
	if registry == nil || identity.Principal == "" {
		return req, archiveProviderAttemptReservation{}, true
	}
	allowance, ok := req.Context().Value(archiveAttemptAllowanceKey{}).(*archiveAttemptAllowance)
	if !ok || allowance.provider == nil {
		return req, archiveProviderAttemptReservation{}, true
	}
	config := allowance.provider
	resources := []QuotaResource{resource}
	if identity == config.identity {
		resources = config.resources
	}
	now := registry.now().UTC()
	var current QuotaPool
	for _, required := range resources {
		pool, known := registry.Get(identity, required)
		if !known || !pool.Known || pool.ResetAt.IsZero() || !pool.ResetAt.After(now) {
			return req, archiveProviderAttemptReservation{}, false
		}
		if required == resource {
			current = pool
			continue
		}
		if pool.Remaining <= config.reserve {
			return req, archiveProviderAttemptReservation{}, false
		}
	}
	cost := max(current.AttemptCost, 1)
	if current.Remaining-cost < config.reserve || !allowance.consume(cost) {
		return req, archiveProviderAttemptReservation{}, false
	}
	reservation := archiveProviderAttemptReservation{
		allowance: allowance, before: current, cost: cost,
	}
	ctx := context.WithValue(
		req.Context(), archiveProviderAttemptReservationKey{}, allowance,
	)
	return req.Clone(ctx), reservation, true
}

func reconcileArchiveProviderAttempt(
	reservation archiveProviderAttemptReservation,
	registry *QuotaRegistry,
	identity IdentityKey,
	resource QuotaResource,
) {
	if reservation.allowance == nil {
		return
	}
	after, ok := registry.Get(identity, resource)
	if !ok || !reservation.before.Known || !after.Known ||
		!after.ResetAt.Equal(reservation.before.ResetAt) ||
		after.Remaining >= reservation.before.Remaining {
		return
	}
	actualCost := reservation.before.Remaining - after.Remaining
	if budget := reservation.allowance.provider.budget; budget != nil &&
		actualCost > reservation.cost {
		budget.addProviderArchiveSpend(actualCost - reservation.cost)
	}
	if actualCost > reservation.cost {
		reservation.allowance.debit(actualCost - reservation.cost)
	}
}

func persistArchiveProviderReservation(reservation archiveProviderAttemptReservation) {
	if reservation.allowance == nil || reservation.cost <= 1 {
		return
	}
	if budget := reservation.allowance.provider.budget; budget != nil {
		budget.addProviderArchiveSpend(reservation.cost - 1)
	}
}

// WithSyncBudget marks a context so that HTTP calls made with
// it count against the sync budget. Background sync entry points
// (RunOnce, syncWatchedMRs) inject this; user-initiated server
// handler paths do not.
func WithSyncBudget(ctx context.Context) context.Context {
	return context.WithValue(ctx, syncBudgetKey{}, true)
}

func IsSyncBudgetContext(ctx context.Context) bool {
	_, ok := ctx.Value(syncBudgetKey{}).(bool)
	return ok
}

func WithArchiveSyncBudget(ctx context.Context) context.Context {
	return context.WithValue(WithSyncBudget(ctx), archiveSyncBudgetKey{}, true)
}

func IsArchiveSyncBudgetContext(ctx context.Context) bool {
	_, ok := ctx.Value(archiveSyncBudgetKey{}).(bool)
	return ok
}

// budgetTransport wraps an http.RoundTripper and increments a
// SyncBudget for every RoundTrip invocation whose request
// context carries the sync-budget key. This captures paginated
// pages and GraphQL calls made during background sync without
// counting user-initiated server actions or GitHub REST 304
// responses that do not spend primary provider quota.
//
// Transparent retries inside net/http.Transport are not visible
// to RoundTripper wrappers and are not counted.
type budgetTransport struct {
	base   http.RoundTripper
	budget *SyncBudget
}

func WrapSyncBudgetTransport(base http.RoundTripper, budget *SyncBudget) http.RoundTripper {
	if budget == nil {
		return base
	}
	return &budgetTransport{base: base, budget: budget}
}

func (t *budgetTransport) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	if !ConsumeArchiveAttemptAllowance(req.Context()) {
		return nil, platform.ErrArchiveAttemptBudget
	}
	counted := IsSyncBudgetContext(req.Context())
	archive := IsArchiveSyncBudgetContext(req.Context())
	var window BudgetWindow
	if counted {
		var reserved bool
		if archive {
			window, reserved = t.budget.TrySpendArchive(1)
		} else {
			window, reserved = t.budget.TrySpend(1)
		}
		if !reserved {
			return nil, platform.ErrSyncBudgetExhausted
		}
	}
	resp, err := t.base.RoundTrip(req)
	if counted && resp != nil && resp.StatusCode == http.StatusNotModified {
		if archive {
			t.budget.RefundArchive(window, 1)
		} else {
			t.budget.Refund(window, 1)
		}
	}
	return resp, err
}
