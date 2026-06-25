package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
	assert.False(t, b.CanSpend(PRDetailWorstCase))   // 9 > 5 remaining
	assert.True(t, b.CanSpend(IssueDetailWorstCase)) // 2 <= 5 remaining
}
