package github

import (
	"math"
	"sync"
	"time"
)

// PRDetailWorstCase is the maximum API calls a PR detail
// fetch can make (detail + GetUser + comments + reviews +
// commits + force-push events + review threads + combined status +
// check runs + up to two workflow-run reads when approval state loses
// a parent race).
const PRDetailWorstCase = 11

// IssueDetailWorstCase is the maximum API calls an issue
// detail fetch can make (detail + comments).
const IssueDetailWorstCase = 2

// SyncBudget tracks hourly API call spend for background
// detail fetches on a single host.
type SyncBudget struct {
	mu           sync.Mutex
	limit        int
	spent        int
	archiveSpent int
}

func NewSyncBudget(limit int) *SyncBudget {
	return &SyncBudget{limit: limit}
}

func (b *SyncBudget) CanSpend(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent+n <= b.limit
}

// TrySpend atomically checks and increments the budget.
// Returns true if the spend was successful.
func (b *SyncBudget) TrySpend(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spent+n > b.limit {
		return false
	}
	b.spent += n
	return true
}

func (b *SyncBudget) Spend(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent += n
}

// Refund returns n calls back to the budget.
func (b *SyncBudget) Refund(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent -= n
	if b.spent < 0 {
		b.spent = 0
	}
}

func (b *SyncBudget) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent = 0
	b.archiveSpent = 0
}

func (b *SyncBudget) Remaining() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit - b.spent
}

func (b *SyncBudget) Spent() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

func (b *SyncBudget) Limit() int {
	return b.limit
}

func (b *SyncBudget) ArchiveSpendCeiling(now time.Time, resetAt *time.Time, liveFloor int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.archiveSpendCeiling(now, resetAt, liveFloor)
}

func (b *SyncBudget) CanSpendArchive(n int, now time.Time, resetAt *time.Time, liveFloor int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 {
		return false
	}
	ceiling := b.archiveSpendCeiling(now, resetAt, liveFloor)
	return b.archiveSpent+n <= ceiling && b.spent+n <= b.limit-liveFloor
}

func (b *SyncBudget) SpendArchive(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent += n
	b.archiveSpent += n
}

func (b *SyncBudget) ArchiveSpent() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.archiveSpent
}

func (b *SyncBudget) archiveSpendCeiling(now time.Time, resetAt *time.Time, liveFloor int) int {
	if resetAt == nil || liveFloor >= b.limit {
		return 0
	}
	remaining := resetAt.Sub(now)
	if remaining < 0 || remaining > time.Hour {
		return 0
	}
	elapsedFraction := 1 - float64(remaining)/float64(time.Hour)
	surplus := b.limit - max(liveFloor, 0)
	return int(math.Floor(float64(surplus) * elapsedFraction * elapsedFraction))
}
