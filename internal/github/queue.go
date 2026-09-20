package github

import (
	"cmp"
	"slices"
	"time"

	"go.kenn.io/forge/platform"
)

// QueueItemType distinguishes PRs from issues for cost
// estimation.
type QueueItemType int

const (
	QueueItemPR QueueItemType = iota
	QueueItemIssue
)

// QueueItem holds scoring inputs and result for a single
// item that may need a detail fetch.
type QueueItem struct {
	Type         QueueItemType
	Platform     platform.Kind
	RepoOwner    string
	RepoName     string
	Number       int
	PlatformHost string
	Score        float64

	// Scoring inputs
	UpdatedAt       time.Time
	DetailFetchedAt *time.Time
	CIHadPending    bool
	Starred         bool
	Watched         bool
	IsOpen          bool
	LargeRepo       bool
	dailyDue        bool
}

// WorstCaseCost returns the maximum wire attempts this item's
// detail fetch could require, including authentication retry and
// provider metadata confirmation allowances.
func (qi *QueueItem) WorstCaseCost() int {
	return detailWorstCaseAttemptCost(qi.Platform, qi.Type)
}

func (qi QueueItem) Compare(other QueueItem) int {
	// Once an item reaches the daily deadline, oldest checks go first.
	// The separate watched/webhook paths still keep foreground work fast.
	if qi.dailyDue != other.dailyDue {
		if qi.dailyDue {
			return -1
		}
		return 1
	}
	if qi.dailyDue {
		left, right := qi.UpdatedAt, other.UpdatedAt
		if qi.DetailFetchedAt != nil {
			left = *qi.DetailFetchedAt
		}
		if other.DetailFetchedAt != nil {
			right = *other.DetailFetchedAt
		}
		if order := left.Compare(right); order != 0 {
			return order
		}
	}
	return cmp.Compare(other.Score, qi.Score)
}

// Staleness thresholds.
const (
	defaultRefetchInterval = 30 * time.Minute
	starWatchInterval      = 15 * time.Minute
	dailyRefetchInterval   = 24 * time.Hour
)

// BuildQueue orders overdue items oldest-first, then other eligible items by score.
func BuildQueue(
	items []QueueItem, now time.Time,
) []QueueItem {
	var eligible []QueueItem
	for i := range items {
		if !isEligible(&items[i], now) {
			continue
		}
		items[i].Score = score(&items[i], now)
		items[i].dailyDue = items[i].DetailFetchedAt == nil ||
			now.Sub(*items[i].DetailFetchedAt) >= dailyRefetchInterval
		eligible = append(eligible, items[i])
	}
	slices.SortFunc(eligible, QueueItem.Compare)
	return eligible
}

func updatedSinceLastFetch(qi *QueueItem) bool {
	return qi.DetailFetchedAt != nil &&
		qi.UpdatedAt.After(*qi.DetailFetchedAt)
}

func commentRefreshDue(state string, updated, fetched time.Time, starred bool, now time.Time) bool {
	// Fast comment checks remain useful for active conversations. Dormant
	// items share the daily detail deadline instead of polling every pass.
	return state == "open" && (starred || now.Sub(updated) < dailyRefetchInterval ||
		now.Sub(fetched) >= dailyRefetchInterval)
}

func isEligible(qi *QueueItem, now time.Time) bool {
	// Routine coverage is open-only. Detected changes may refresh a closed
	// item; explicit user refreshes use the direct sync path.
	if !qi.IsOpen && !updatedSinceLastFetch(qi) {
		return false
	}
	// Never fetched — always eligible.
	if qi.DetailFetchedAt == nil {
		return true
	}

	sinceLastFetch := now.Sub(*qi.DetailFetchedAt)

	// updated_at changed since last fetch — always eligible.
	if updatedSinceLastFetch(qi) {
		return true
	}

	// CI had pending checks — always eligible regardless of
	// updated_at.
	if qi.CIHadPending {
		return true
	}

	// Even large repositories get a budgeted daily check. Previously their
	// unchanged items could stay stale indefinitely.
	if sinceLastFetch >= dailyRefetchInterval {
		return true
	}

	// Starred or watched: eligible if >15min since fetch.
	if qi.Starred || qi.Watched {
		return sinceLastFetch > starWatchInterval
	}

	if qi.LargeRepo || now.Sub(qi.UpdatedAt) >= dailyRefetchInterval {
		return false
	}

	// Default: eligible if >30min since last fetch.
	return sinceLastFetch > defaultRefetchInterval
}

func score(qi *QueueItem, now time.Time) float64 {
	var s float64

	if updatedSinceLastFetch(qi) {
		s += 1000
	}
	if qi.DetailFetchedAt == nil {
		s += 500
	}
	if qi.Starred {
		s += 200
	}
	if qi.Watched {
		s += 200
	}
	if qi.CIHadPending {
		s += 100
	}
	if qi.IsOpen {
		s += 50
	}

	// Recency bonus: decays with hours since last update.
	hours := now.Sub(qi.UpdatedAt).Hours()
	s += 1000 / (1 + hours)

	return s
}
