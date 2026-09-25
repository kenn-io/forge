package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

// Fixed reference time for deterministic tests.
var testNow = time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)

func TestQueueItemWorstCaseCost(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	tests := []struct {
		name string
		item QueueItem
		want int
	}{
		{name: "GitHub pull request", item: QueueItem{Type: QueueItemPR, Platform: platform.KindGitHub}, want: 20},
		{name: "GitLab pull request", item: QueueItem{Type: QueueItemPR, Platform: platform.KindGitLab}, want: 22},
		{name: "GitHub issue", item: QueueItem{Type: QueueItemIssue, Platform: platform.KindGitHub}, want: 4},
		{name: "Gitea issue", item: QueueItem{Type: QueueItemIssue, Platform: platform.KindGitea}, want: 8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(test.want, test.item.WorstCaseCost())
		})
	}
}

func TestBuildQueueNeverFetchedOpenPR(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)

	items := []QueueItem{{
		Type:      QueueItemPR,
		Number:    1,
		IsOpen:    true,
		UpdatedAt: testNow.Add(-1 * time.Hour),
		// DetailFetchedAt nil => never fetched
	}}

	q := BuildQueue(items, testNow)
	assert.Len(q, 1)
	// +500 (never fetched) +50 (open) +1000/(1+1)=500
	// = 1050. Spec says ~1500+, recency with 1h = 500.
	assert.Greater(q[0].Score, 1000.0)
}

func TestBuildQueueStarredUpdatedPR(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)

	fetched := testNow.Add(-20 * time.Minute)
	items := []QueueItem{{
		Type:            QueueItemPR,
		Number:          2,
		IsOpen:          true,
		Starred:         true,
		UpdatedAt:       testNow.Add(-5 * time.Minute),
		DetailFetchedAt: &fetched,
	}}

	q := BuildQueue(items, testNow)
	assert.Len(q, 1)
	// +1000 (updated) +200 (starred) +50 (open)
	// +1000/(1+0.083)=~923 => ~2173
	assert.Greater(q[0].Score, 2100.0)
}

func TestBuildQueueRecentlyFetchedUnchangedExcluded(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)

	// Fetched 10min ago, updated_at before fetch, not
	// starred/watched, no pending CI, open.
	fetched := testNow.Add(-10 * time.Minute)
	items := []QueueItem{{
		Type:            QueueItemPR,
		Number:          3,
		IsOpen:          true,
		UpdatedAt:       testNow.Add(-1 * time.Hour),
		DetailFetchedAt: &fetched,
	}}

	q := BuildQueue(items, testNow)
	assert.Empty(q)
}

func TestBuildQueueStarredStalenessEligible(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)

	// Starred, fetched 20min ago (>15min threshold),
	// updated_at before fetch.
	fetched := testNow.Add(-20 * time.Minute)
	items := []QueueItem{{
		Type:            QueueItemPR,
		Number:          4,
		IsOpen:          true,
		Starred:         true,
		UpdatedAt:       testNow.Add(-2 * time.Hour),
		DetailFetchedAt: &fetched,
	}}

	q := BuildQueue(items, testNow)
	assert.Len(q, 1)
}

func TestBuildQueueClosedOldItemExcluded(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)

	sixMonthsAgo := testNow.Add(-180 * 24 * time.Hour)
	fetched := testNow.Add(-25 * time.Hour)
	items := []QueueItem{{
		Type:            QueueItemIssue,
		Number:          5,
		IsOpen:          false,
		UpdatedAt:       sixMonthsAgo,
		DetailFetchedAt: &fetched,
	}}

	q := BuildQueue(items, testNow)
	assert.Empty(q)
}

func TestBuildQueueSortedByScoreDescending(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)

	fetched := testNow.Add(-1 * time.Hour)
	items := []QueueItem{
		{
			// Low score: closed, old, no bonuses.
			Type:            QueueItemIssue,
			Number:          10,
			IsOpen:          false,
			UpdatedAt:       testNow.Add(-90 * 24 * time.Hour),
			DetailFetchedAt: new(testNow.Add(-25 * time.Hour)),
		},
		{
			// High score: open, starred, updated.
			Type:            QueueItemPR,
			Number:          20,
			IsOpen:          true,
			Starred:         true,
			UpdatedAt:       testNow.Add(-10 * time.Minute),
			DetailFetchedAt: &fetched,
		},
		{
			// Mid score: previously fetched, open, unchanged.
			Type:            QueueItemPR,
			Number:          30,
			IsOpen:          true,
			UpdatedAt:       testNow.Add(-3 * time.Hour),
			DetailFetchedAt: &fetched,
		},
	}

	q := BuildQueue(items, testNow)
	require.Len(t, q, 2)
	assert.Equal(20, q[0].Number) // highest
	assert.Equal(30, q[1].Number) // middle

	for i := range len(q) - 1 {
		assert.Greater(q[i].Score, q[i+1].Score)
	}
}

func TestBuildQueueCIHadPendingMakesEligible(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)

	// Fetched 10min ago, unchanged — normally ineligible.
	// But CIHadPending overrides.
	fetched := testNow.Add(-10 * time.Minute)
	items := []QueueItem{{
		Type:            QueueItemPR,
		Number:          6,
		IsOpen:          true,
		CIHadPending:    true,
		UpdatedAt:       testNow.Add(-1 * time.Hour),
		DetailFetchedAt: &fetched,
	}}

	q := BuildQueue(items, testNow)
	assert.Len(q, 1)
	// Verify CI bonus in score.
	assert.Greater(q[0].Score, 100.0)
}

func TestBuildQueueCIHadPendingBypassesLargeRepoGate(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)

	fetched := testNow.Add(-10 * time.Minute)
	items := []QueueItem{{
		Type:            QueueItemPR,
		Number:          9,
		IsOpen:          true,
		CIHadPending:    true,
		LargeRepo:       true,
		UpdatedAt:       testNow.Add(-1 * time.Hour),
		DetailFetchedAt: &fetched,
	}}

	q := BuildQueue(items, testNow)
	assert.Len(q, 1)
	assert.Equal(9, q[0].Number)
}

func TestBuildQueueClosedRecentlyFetchedExcluded(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)

	// Closed item fetched 12h ago (<24h) — should be
	// excluded.
	fetched := testNow.Add(-12 * time.Hour)
	items := []QueueItem{{
		Type:            QueueItemIssue,
		Number:          7,
		IsOpen:          false,
		UpdatedAt:       testNow.Add(-48 * time.Hour),
		DetailFetchedAt: &fetched,
	}}

	q := BuildQueue(items, testNow)
	assert.Empty(q)
}

func TestBuildQueueWatchedStalenessEligible(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)

	fetched := testNow.Add(-20 * time.Minute)
	items := []QueueItem{{
		Type:            QueueItemPR,
		Number:          8,
		IsOpen:          true,
		Watched:         true,
		UpdatedAt:       testNow.Add(-2 * time.Hour),
		DetailFetchedAt: &fetched,
	}}

	q := BuildQueue(items, testNow)
	assert.Len(q, 1)
}

func TestBuildQueueEmptyInput(t *testing.T) {
	t.Parallel()

	q := BuildQueue(nil, testNow)
	assert.Empty(t, q)
}

func TestBuildQueueDormantOpenItemsRefreshDaily(t *testing.T) {
	t.Parallel()

	for _, large := range []bool{false, true} {
		for _, kind := range []QueueItemType{QueueItemPR, QueueItemIssue} {
			item := QueueItem{
				Type: kind, IsOpen: true, LargeRepo: large,
				UpdatedAt:       testNow.Add(-14 * 24 * time.Hour),
				DetailFetchedAt: new(testNow.Add(-time.Hour)),
			}
			assert.Empty(t, BuildQueue([]QueueItem{item}, testNow))
			item.DetailFetchedAt = new(testNow.Add(-24 * time.Hour))
			assert.Len(t, BuildQueue([]QueueItem{item}, testNow), 1)
		}
	}
}

func TestBuildQueueDailyCoverageCannotStarveBehindActiveWork(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	items := []QueueItem{
		{
			Number: 1, IsOpen: true, Starred: true, UpdatedAt: testNow,
			DetailFetchedAt: new(testNow.Add(-time.Hour)),
		},
		{
			Number: 2, IsOpen: true, UpdatedAt: testNow.Add(-14 * 24 * time.Hour),
			DetailFetchedAt: new(testNow.Add(-25 * time.Hour)),
		},
		{
			Number: 3, IsOpen: true, UpdatedAt: testNow.Add(-30 * 24 * time.Hour),
			DetailFetchedAt: new(testNow.Add(-26 * time.Hour)),
		},
	}
	queue := BuildQueue(items, testNow)
	require.Len(t, queue, 3)
	assert.Equal(3, queue[0].Number)
	assert.Equal(2, queue[1].Number)
	assert.Equal(1, queue[2].Number)
}

func TestDailyCoverageIncludesNeverFetchedItemsWithLimitedCapacity(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	items := []QueueItem{
		{
			Number: 1, IsOpen: true, UpdatedAt: testNow.Add(-96 * time.Hour),
			DetailFetchedAt: new(testNow.Add(-72 * time.Hour)),
		},
		{
			Number: 2, IsOpen: true, UpdatedAt: testNow.Add(-96 * time.Hour),
			DetailFetchedAt: new(testNow.Add(-48 * time.Hour)),
		},
		{Number: 3, IsOpen: true, UpdatedAt: testNow.Add(-24 * time.Hour)},
	}
	var checked []int
	for day := range 3 {
		now := testNow.Add(time.Duration(day) * 24 * time.Hour)
		queue := BuildQueue(items, now)
		require.NotEmpty(queue)
		checked = append(checked, queue[0].Number)
		items[queue[0].Number-1].DetailFetchedAt = &now
	}
	assert.Equal(t, []int{1, 2, 3}, checked)
}
