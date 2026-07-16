package github

import "sync"

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
	mu    sync.Mutex
	limit int
	spent int
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
