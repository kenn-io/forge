package server

import (
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"go.kenn.io/middleman/internal/db"
)

func member(number, pos int, state, ci, review string) db.StackMemberWithPR {
	return db.StackMemberWithPR{
		Number: number, Position: pos, State: state,
		CIStatus: ci, ReviewDecision: review,
	}
}

func draftMember(number, pos int, ci, review string) db.StackMemberWithPR {
	return db.StackMemberWithPR{
		Number: number, Position: pos, State: "open",
		CIStatus: ci, ReviewDecision: review, IsDraft: true,
	}
}

func TestComputeStackHealth(t *testing.T) {
	tests := []struct {
		name    string
		members []db.StackMemberWithPR
		want    string
	}{
		{"empty", nil, "in_progress"},
		{"all green", []db.StackMemberWithPR{
			member(1, 1, "open", "success", "APPROVED"),
			member(2, 2, "open", "success", "APPROVED"),
		}, "all_green"},
		{"blocked by failing CI with descendant", []db.StackMemberWithPR{
			member(1, 1, "open", "failure", ""),
			member(2, 2, "open", "success", "APPROVED"),
		}, "blocked"},
		{"partial merge", []db.StackMemberWithPR{
			member(1, 1, "merged", "success", "APPROVED"),
			member(2, 2, "open", "success", "APPROVED"),
		}, "partial_merge"},
		{"base ready", []db.StackMemberWithPR{
			member(1, 1, "open", "success", "APPROVED"),
			member(2, 2, "open", "pending", ""),
		}, "base_ready"},
		{"single open failure not blocked (nothing downstream)", []db.StackMemberWithPR{
			member(1, 1, "open", "failure", ""),
		}, "in_progress"},
		{"tip PR failing not blocked (nothing downstream)", []db.StackMemberWithPR{
			member(1, 1, "merged", "success", "APPROVED"),
			member(2, 2, "open", "failure", ""),
		}, "partial_merge"},
		{"changes requested at tip not blocked", []db.StackMemberWithPR{
			member(1, 1, "open", "success", "APPROVED"),
			member(2, 2, "open", "success", "CHANGES_REQUESTED"),
		}, "base_ready"},
		{"changes requested with descendant is blocked", []db.StackMemberWithPR{
			member(1, 1, "open", "success", "CHANGES_REQUESTED"),
			member(2, 2, "open", "success", "APPROVED"),
		}, "blocked"},
		{"in progress", []db.StackMemberWithPR{
			member(1, 1, "open", "pending", ""),
			member(2, 2, "open", "pending", ""),
		}, "in_progress"},
		{"draft with green CI is not all_green", []db.StackMemberWithPR{
			draftMember(1, 1, "success", "APPROVED"),
			member(2, 2, "open", "success", "APPROVED"),
		}, "in_progress"},
		{"draft base is not base_ready", []db.StackMemberWithPR{
			draftMember(1, 1, "success", "APPROVED"),
			member(2, 2, "open", "pending", ""),
		}, "in_progress"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Assert.Equal(t, tt.want, computeStackHealth(tt.members))
		})
	}
}

func TestComputeBlockedBy(t *testing.T) {
	assert := Assert.New(t)

	// No blockers
	members := []db.StackMemberWithPR{
		member(1, 1, "open", "success", "APPROVED"),
		member(2, 2, "open", "success", "APPROVED"),
	}
	assert.Empty(computeBlockedBy(members))

	// #1 blocks #2 and #3 (transitive cascade)
	members = []db.StackMemberWithPR{
		member(1, 1, "open", "failure", ""),
		member(2, 2, "open", "success", "APPROVED"),
		member(3, 3, "open", "success", "APPROVED"),
	}
	blocked := computeBlockedBy(members)
	assert.Equal(1, blocked[2])
	assert.Equal(1, blocked[3])

	// Merged PRs skipped, blocker is #2
	members = []db.StackMemberWithPR{
		member(1, 1, "merged", "success", "APPROVED"),
		member(2, 2, "open", "failure", ""),
		member(3, 3, "open", "success", ""),
	}
	blocked = computeBlockedBy(members)
	assert.Equal(2, blocked[3])
	assert.NotContains(blocked, 1)

	// CHANGES_REQUESTED also triggers blocking
	members = []db.StackMemberWithPR{
		member(1, 1, "open", "success", "CHANGES_REQUESTED"),
		member(2, 2, "open", "success", "APPROVED"),
	}
	blocked = computeBlockedBy(members)
	assert.Equal(1, blocked[2])
}
