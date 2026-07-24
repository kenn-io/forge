package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/middleman/internal/platform"
)

func TestSyncBudgetBasics(t *testing.T) {
	assert := assert.New(t)
	b := NewSyncBudget(100)

	assert.Equal(100, b.Limit())
	assert.Equal(0, b.Spent())
	assert.Equal(100, b.Remaining())
	assert.True(b.CanSpend(6))

	b.Spend(50)
	assert.Equal(50, b.Spent())
	assert.Equal(50, b.Remaining())
	assert.True(b.CanSpend(6))
	assert.False(b.CanSpend(51))

	b.Reset()
	assert.Equal(0, b.Spent())
	assert.Equal(100, b.Remaining())
}

func TestSyncBudgetWorstCase(t *testing.T) {
	b := NewSyncBudget(10)
	b.Spend(5)
	assert.Equal(t, 10, PRDetailWorstCase)
	assert.False(t, b.CanSpend(PRDetailWorstCase))   // 10 > 5 remaining
	assert.True(t, b.CanSpend(IssueDetailWorstCase)) // 2 <= 5 remaining
}

func TestArchiveLiveFloorReservesWorstCaseWireAttempts(t *testing.T) {
	assert.Equal(t, 24, archiveLiveFloor(platform.KindGitHub))
	assert.Equal(t, 24, archiveLiveFloor(platform.KindGitLab))
	assert.Equal(t, 24, archiveLiveFloor(platform.KindGitea))
}

func TestSyncBudgetArchiveAdmissionRampsTowardReset(t *testing.T) {
	assert := assert.New(t)
	budget := NewSyncBudget(100)
	reset := time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)
	liveFloor := 20

	tests := []struct {
		name        string
		now         time.Time
		wantCeiling int
		wantSpend   int
	}{
		{name: "start", now: reset.Add(-time.Hour), wantCeiling: 0},
		{name: "half", now: reset.Add(-30 * time.Minute), wantCeiling: 20, wantSpend: 20},
		{name: "three quarters", now: reset.Add(-15 * time.Minute), wantCeiling: 45, wantSpend: 25},
		{name: "reset", now: reset, wantCeiling: 80, wantSpend: 35},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(test.wantCeiling, budget.ArchiveSpendCeiling(test.now, &reset, liveFloor))
			if test.wantSpend == 0 {
				assert.False(budget.CanSpendArchive(1, test.now, &reset, liveFloor))
				return
			}
			assert.True(budget.CanSpendArchive(test.wantSpend, test.now, &reset, liveFloor))
			budget.SpendArchive(test.wantSpend)
		})
	}
	assert.Equal(80, budget.ArchiveSpent())
	assert.Equal(20, budget.Remaining())
}

func TestSyncBudgetArchiveAdmissionPreservesLiveFloor(t *testing.T) {
	assert := assert.New(t)
	budget := NewSyncBudget(50)
	reset := time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)

	assert.False(budget.CanSpendArchive(1, reset, &reset, 50))
	assert.False(budget.CanSpendArchive(1, reset, &reset, 60))
	assert.True(budget.CanSpendArchive(37, reset, &reset, 13))
	budget.SpendArchive(37)
	assert.Equal(13, budget.Remaining())
	assert.False(budget.CanSpendArchive(1, reset, &reset, 13))
}

func TestSyncBudgetArchiveAdmissionRequiresPlausibleReset(t *testing.T) {
	assert := assert.New(t)
	budget := NewSyncBudget(100)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Second)
	tooFar := now.Add(time.Hour + time.Second)

	assert.Zero(budget.ArchiveSpendCeiling(now, nil, 20))
	assert.Zero(budget.ArchiveSpendCeiling(now, &past, 20))
	assert.Zero(budget.ArchiveSpendCeiling(now, &tooFar, 20))
	assert.False(budget.CanSpendArchive(1, now, nil, 20))
}

func TestSyncBudgetResetClearsArchiveSpend(t *testing.T) {
	budget := NewSyncBudget(100)
	budget.SpendArchive(12)
	budget.Reset()

	assert.Zero(t, budget.Spent())
	assert.Zero(t, budget.ArchiveSpent())
}
