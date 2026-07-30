package github

import (
	"math"
	"sync"
	"time"

	"go.kenn.io/forge/internal/platform"
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
//
// The hourly window rolls on the budget's own clock, independent of provider
// quota. Provider window resets still call Reset, but the local ceiling must
// never depend on them: once the ceiling is exhausted its transport refuses
// counted requests before any wire attempt, so no provider response — and
// therefore no provider-driven reset — can arrive to release it.
type SyncBudget struct {
	mu                   sync.Mutex
	limit                int
	spent                int
	archiveSpent         int
	providerArchiveSpent map[providerArchiveWindow]int
	windowStart          time.Time
	window               BudgetWindow
	now                  func() time.Time
}

type providerArchiveWindow struct {
	resource QuotaResource
	resetAt  time.Time
}

// BudgetWindow identifies the hourly window a reservation was made in. Refunds
// carry it so a response that arrives after a rollover cannot credit spend back
// into the new window: that spend was already cleared by the roll, and
// returning it again would let the new window exceed its ceiling.
type BudgetWindow uint64

func NewSyncBudget(limit int) *SyncBudget {
	return &SyncBudget{
		limit:       limit,
		windowStart: time.Now().UTC(),
		now:         time.Now,
	}
}

// rollLocked clears spend once the local hourly window has elapsed.
// Must be called with mu held.
func (b *SyncBudget) rollLocked() {
	now := b.now().UTC()
	if now.Sub(b.windowStart) < time.Hour {
		return
	}
	b.spent = 0
	b.archiveSpent = 0
	b.windowStart = now
	b.window++
}

func (b *SyncBudget) CanSpend(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	return b.spent+n <= b.limit
}

// TrySpend atomically checks and increments the budget. It reports whether the
// spend succeeded and, when it did, the window the reservation belongs to. Pass
// that window to Refund so a late refund cannot cross a rollover.
func (b *SyncBudget) TrySpend(n int) (BudgetWindow, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	if n < 0 || b.spent+n > b.limit {
		return 0, false
	}
	b.spent += n
	return b.window, true
}

func (b *SyncBudget) Spend(n int) {
	b.TrySpend(n)
}

// Refund returns n calls back to the budget. A refund for an elapsed window is
// dropped: the roll already cleared that spend.
func (b *SyncBudget) Refund(window BudgetWindow, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if window != b.window {
		return
	}
	b.spent = max(b.spent-n, 0)
}

func (b *SyncBudget) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent = 0
	b.archiveSpent = 0
	b.windowStart = b.now().UTC()
	b.window++
}

func (b *SyncBudget) Remaining() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	return max(b.limit-b.spent, 0)
}

func (b *SyncBudget) Spent() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	return b.spent
}

func (b *SyncBudget) Limit() int {
	return b.limit
}

func (b *SyncBudget) ArchiveSpendCeiling(now time.Time, resetAt *time.Time, liveFloor int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	return b.archiveSpendCeiling(now, resetAt, liveFloor)
}

func (b *SyncBudget) ProviderArchiveSpendCeiling(
	now time.Time,
	resetAt *time.Time,
	localLiveFloor int,
	providerLimit int,
	providerReserve int,
) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	return b.providerArchiveSpendCeiling(
		now, resetAt, localLiveFloor, providerLimit, providerReserve,
	)
}

func (b *SyncBudget) ProviderArchiveSpendAvailable(
	now time.Time,
	window QuotaPacingWindow,
	localLiveFloor int,
	providerReserve int,
) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	providerArchiveSpent := b.providerArchiveSpendLocked(window.ResourceResets)
	ceilingRemaining := b.providerArchiveSpendCeiling(
		now, &window.ResetAt, localLiveFloor, window.Limit, providerReserve,
	) - providerArchiveSpent
	localRemaining := b.limit - max(localLiveFloor, 0) - b.spent
	providerHeadroom := window.Remaining - max(providerReserve, 0)
	return max(min(ceilingRemaining, localRemaining, providerHeadroom), 0)
}

// providerArchiveSpendLocked returns provider quota-unit spend belonging to
// each resource's current window. Staggered resets expire only the spend for
// the resource that advanced, preserving charges already made in another
// resource's newer window.
func (b *SyncBudget) providerArchiveSpendLocked(
	resourceResets map[QuotaResource]time.Time,
) int {
	spent := 0
	for window, amount := range b.providerArchiveSpent {
		currentReset, current := resourceResets[window.resource]
		switch {
		case current && currentReset.Equal(window.resetAt):
			spent += amount
		case current && currentReset.After(window.resetAt):
			delete(b.providerArchiveSpent, window)
		}
	}
	return spent
}

func (b *SyncBudget) providerArchiveSpendCeiling(
	now time.Time,
	resetAt *time.Time,
	localLiveFloor int,
	providerLimit int,
	providerReserve int,
) int {
	if resetAt == nil || providerLimit <= providerReserve {
		return 0
	}
	remaining := resetAt.Sub(now)
	if remaining < 0 || remaining > time.Hour {
		return 0
	}
	elapsedFraction := 1 - float64(remaining)/float64(time.Hour)
	localSurplus := b.limit - max(localLiveFloor, 0)
	providerSurplus := providerLimit - max(providerReserve, 0)
	surplus := min(localSurplus, providerSurplus)
	if surplus <= 0 {
		return 0
	}
	return int(math.Floor(float64(surplus) * elapsedFraction * elapsedFraction))
}

func (b *SyncBudget) CanSpendArchive(n int, now time.Time, resetAt *time.Time, liveFloor int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	return n > 0 && n <= b.archiveSpendAvailable(now, resetAt, liveFloor)
}

func (b *SyncBudget) ArchiveSpendAvailable(now time.Time, resetAt *time.Time, liveFloor int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
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

func (b *SyncBudget) TrySpendArchive(n int) (BudgetWindow, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	if n < 0 || b.spent+n > b.limit {
		return 0, false
	}
	b.spent += n
	b.archiveSpent += n
	return b.window, true
}

// addProviderArchiveSpend records provider quota units independently from the
// local wire-call ceiling and keys them to the resource window that incurred
// them so staggered resets cannot clear newer-window spend.
func (b *SyncBudget) addProviderArchiveSpend(
	resource QuotaResource,
	resetAt time.Time,
	n int,
) {
	if resource == "" || resetAt.IsZero() || n <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.providerArchiveSpent == nil {
		b.providerArchiveSpent = make(map[providerArchiveWindow]int)
	}
	window := providerArchiveWindow{resource: resource, resetAt: resetAt.UTC()}
	b.providerArchiveSpent[window] += n
}

func (b *SyncBudget) RefundArchive(window BudgetWindow, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if window != b.window {
		return
	}
	b.spent = max(b.spent-n, 0)
	b.archiveSpent = max(b.archiveSpent-n, 0)
	// Provider pacing counts wire attempts, including conditional requests.
	// Do not return this spend: a late response may belong to an older provider
	// window even when it still belongs to the current local budget window.
}

func (b *SyncBudget) ArchiveSpent() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
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
