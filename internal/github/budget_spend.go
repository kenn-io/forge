package github

import (
	"context"
	"time"
)

// SyncSpendKind identifies the background work responsible for a local charge.
type SyncSpendKind uint8

const (
	SyncSpendDiscovery SyncSpendKind = iota
	SyncSpendWatchedPRRefresh
	SyncSpendDetail
	SyncSpendArchive
)

// SyncSpendByKind reports charges in one local hourly window.
type SyncSpendByKind struct {
	Discovery        int
	WatchedPRRefresh int
	Detail           int
	Archive          int
}

// SyncBudgetSnapshot captures all local ceiling fields under one lock.
type SyncBudgetSnapshot struct {
	Limit           int
	BackgroundLimit int
	Spent           int
	Remaining       int
	ResetAt         time.Time
	SpentByKind     SyncSpendByKind
}

func (b *SyncBudget) Snapshot() SyncBudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	return SyncBudgetSnapshot{
		Limit: b.limit, BackgroundLimit: b.optionalLimitLocked(),
		Spent: b.spent, Remaining: max(b.limit-b.spent, 0),
		ResetAt: b.windowStart.Add(time.Hour),
		SpentByKind: SyncSpendByKind{
			Discovery:        b.spentByKind[SyncSpendDiscovery],
			WatchedPRRefresh: b.spentByKind[SyncSpendWatchedPRRefresh],
			Detail:           b.spentByKind[SyncSpendDetail],
			Archive:          b.spentByKind[SyncSpendArchive],
		},
	}
}

type syncSpendKindKey struct{}

// WithSyncSpendKind labels counted work without changing budget admission.
func WithSyncSpendKind(ctx context.Context, kind SyncSpendKind) context.Context {
	return context.WithValue(ctx, syncSpendKindKey{}, kind)
}

func syncSpendKind(ctx context.Context) SyncSpendKind {
	if kind, ok := ctx.Value(syncSpendKindKey{}).(SyncSpendKind); ok {
		return kind
	}
	if IsArchiveSyncBudgetContext(ctx) {
		return SyncSpendArchive
	}
	if IsEssentialSyncBudgetContext(ctx) {
		return SyncSpendDiscovery
	}
	return SyncSpendDetail
}

func (b *SyncBudget) refundKind(window BudgetWindow, n int, kind SyncSpendKind, archive bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if window != b.window {
		return
	}
	n = min(n, b.spentByKind[kind])
	b.spent -= n
	b.spentByKind[kind] -= n
	if archive {
		b.archiveSpent = max(b.archiveSpent-n, 0)
	}
}
