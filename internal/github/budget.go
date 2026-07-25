package github

import (
	"math"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/platform"
)

// PRDetailWorstCase is the maximum API calls a PR detail
// fetch can make (detail + GetUser + comments + reviews +
// commits + force-push events + review threads + combined status +
// check runs + one workflow-run read).
const PRDetailWorstCase = 10

// IssueDetailWorstCase is the maximum API calls an issue
// detail fetch can make (detail + comments).
const IssueDetailWorstCase = 2

const wireAttemptsPerRequest = 2

func detailWorstCaseAttemptCost(kind platform.Kind, itemType QueueItemType) int {
	logicalRequests := IssueDetailWorstCase
	if itemType == QueueItemPR {
		logicalRequests = PRDetailWorstCase
	}
	if kind != platform.KindGitHub {
		logicalRequests++ // repository metadata confirmation after a candidate feature error
	}
	return logicalRequests * wireAttemptsPerRequest
}

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
	if n < 0 || b.spent+n > b.limit {
		return false
	}
	b.spent += n
	return true
}

func (b *SyncBudget) Spend(n int) {
	b.TrySpend(n)
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
	return max(b.limit-b.spent, 0)
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
	return n > 0 && n <= b.archiveSpendAvailable(now, resetAt, liveFloor)
}

func (b *SyncBudget) ArchiveSpendAvailable(now time.Time, resetAt *time.Time, liveFloor int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.archiveSpendAvailable(now, resetAt, liveFloor)
}

// LocalArchiveSpendAvailable returns the unspent configured hourly budget
// above the live-work floor. It is used only by providers whose responses do
// not expose a usable quota window; it does not create provider quota state.
func (b *SyncBudget) LocalArchiveSpendAvailable(liveFloor int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return max(b.limit-max(liveFloor, 0)-b.spent, 0)
}

func (b *SyncBudget) SpendArchive(n int) {
	b.TrySpendArchive(n)
}

func (b *SyncBudget) TrySpendArchive(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n < 0 || b.spent+n > b.limit {
		return false
	}
	b.spent += n
	b.archiveSpent += n
	return true
}

func (b *SyncBudget) RefundArchive(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent = max(b.spent-n, 0)
	b.archiveSpent = max(b.archiveSpent-n, 0)
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

func (b *SyncBudget) archiveSpendAvailable(now time.Time, resetAt *time.Time, liveFloor int) int {
	ceilingRemaining := b.archiveSpendCeiling(now, resetAt, liveFloor) - b.archiveSpent
	liveRemaining := b.limit - max(liveFloor, 0) - b.spent
	return max(min(ceilingRemaining, liveRemaining), 0)
}
