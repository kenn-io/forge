package pullapi

import (
	"context"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
)

// lowestUnmergedBelow returns the lowest unmerged member below number.
// Members must be ordered by position.
func lowestUnmergedBelow(members []db.StackMemberWithPR, number int) (db.StackMemberWithPR, bool) {
	for _, member := range members {
		if member.Number == number {
			break
		}
		if member.State != string(db.MergeRequestStateMerged) {
			return member, true
		}
	}
	return db.StackMemberWithPR{}, false
}

// midStackBlocker returns the lowest unmerged member below number that a merge
// of number would leave behind. A native stack has none: the provider merges
// every unmerged member below the merged one in the same operation, so the
// stack still lands bottom-up.
func midStackBlocker(
	stack *db.Stack, members []db.StackMemberWithPR, number int,
) (db.StackMemberWithPR, bool) {
	if stack == nil || stack.Native {
		return db.StackMemberWithPR{}, false
	}
	return lowestUnmergedBelow(members, number)
}

// mergeQueueSubject returns the pull request whose merge queue state decides
// whether merging number, based on baseBranch, adds it to a merge queue. A
// native stack merge lands every unmerged member below number on the stack
// base, so the lowest of those members decides, not number's own base, which
// is the branch of the member below it.
func mergeQueueSubject(
	stack *db.Stack, members []db.StackMemberWithPR, number int, baseBranch string,
) (int, string) {
	if stack != nil && stack.Native {
		if member, ok := lowestUnmergedBelow(members, number); ok {
			return member.Number, member.BaseBranch
		}
	}
	return number, baseBranch
}

// mergeQueueTarget returns the merge queue state that decides whether merging
// mr adds it to a merge queue, or nil for a direct merge.
func (s *Handler) mergeQueueTarget(
	ctx context.Context, repo db.Repo, mr *db.MergeRequest,
	stack *db.Stack, members []db.StackMemberWithPR,
) (*db.MergeQueueState, error) {
	if !s.capabilitiesForRepo(repo).MergeQueueMutation {
		return nil, nil
	}
	number, baseBranch := mergeQueueSubject(stack, members, mr.Number, mr.BaseBranch)
	state, err := s.db.GetMergeQueueState(ctx, repo.ID, number)
	if err != nil {
		return nil, httpapi.Internal("load merge queue state failed")
	}
	if state == nil || !state.Enabled || state.BaseBranch != baseBranch {
		return nil, nil
	}
	return state, nil
}
