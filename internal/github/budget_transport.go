package github

import (
	"context"
	"net/http"
	"sync/atomic"

	"go.kenn.io/middleman/internal/platform"
)

type syncBudgetKey struct{}
type archiveSyncBudgetKey struct{}
type archiveAttemptAllowanceKey struct{}

// archiveAttemptAllowance is a shared, mutable counter of the wire attempts an
// admitted archive request is still allowed to make. Every budget-counting
// transport decrements it once per attempt and refuses the attempt when it
// falls below zero, so provider-SDK retries and authentication retries together
// cannot exceed the admitted cost.
type archiveAttemptAllowance struct {
	remaining atomic.Int64
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
