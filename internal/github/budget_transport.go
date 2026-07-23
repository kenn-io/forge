package github

import (
	"context"
	"net/http"
	"sync/atomic"

	"go.kenn.io/middleman/internal/platform"
)

type syncBudgetKey struct{}
type archiveSyncBudgetKey struct{}
type wireAttemptAllowanceKey struct{}

// wireAttemptAllowance is a shared, mutable counter of the wire attempts an
// admitted sync operation is still allowed to make. Every budget-counting
// transport decrements it once per attempt and refuses the attempt when it
// falls below zero, so pagination, provider-SDK retries, and authentication
// retries together cannot exceed the admitted cost.
type wireAttemptAllowance struct {
	remaining atomic.Int64
}

// WithWireAttemptAllowance attaches a per-operation wire-attempt allowance to
// ctx. Admission sets attempts to the operation's admitted cost so the ceiling
// enforced before work begins is also enforced atomically at every wire attempt.
func WithWireAttemptAllowance(ctx context.Context, attempts int) context.Context {
	allowance := &wireAttemptAllowance{}
	allowance.remaining.Store(int64(attempts))
	return context.WithValue(ctx, wireAttemptAllowanceKey{}, allowance)
}

// ConsumeWireAttemptAllowance reserves one wire attempt from the allowance in
// ctx. It returns true when the attempt is permitted and false once the
// allowance is exhausted, in which case the caller must refuse the attempt
// without any upstream I/O. Contexts without an allowance always permit the
// attempt.
func ConsumeWireAttemptAllowance(ctx context.Context) bool {
	allowance, ok := ctx.Value(wireAttemptAllowanceKey{}).(*wireAttemptAllowance)
	if !ok {
		return true
	}
	return allowance.remaining.Add(-1) >= 0
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
	if !ConsumeWireAttemptAllowance(req.Context()) {
		return nil, platform.ErrWireAttemptBudget
	}
	resp, err := t.base.RoundTrip(req)
	if IsSyncBudgetContext(req.Context()) &&
		(resp == nil || resp.StatusCode != http.StatusNotModified) {
		if IsArchiveSyncBudgetContext(req.Context()) {
			t.budget.SpendArchive(1)
		} else {
			t.budget.Spend(1)
		}
	}
	return resp, err
}
