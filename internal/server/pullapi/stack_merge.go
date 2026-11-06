package pullapi

import (
	"context"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
)

// midStackBlocker returns the lowest unmerged member below number that a merge
// of number would leave behind. A native stack has none: the provider merges
// every unmerged member below the merged one in the same operation, so the
// stack still lands bottom-up. Members must be ordered by position.
func midStackBlocker(
	stack *db.Stack, members []db.StackMemberWithPR, number int,
) (db.StackMemberWithPR, bool) {
	if stack == nil || stack.Native {
		return db.StackMemberWithPR{}, false
	}
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

// mergeQueueTarget returns the merge queue state that decides whether merging
// mr adds it to a merge queue, or nil for a direct merge. A native stack
// merge lands every unmerged member below mr on the stack base, so the queue
// of the lowest unmerged member's base branch decides, not mr's own base,
// which is the branch of the member below it. Members must be ordered by
// position.
func (s *Handler) mergeQueueTarget(
	ctx context.Context, repo db.Repo, mr *db.MergeRequest,
	stack *db.Stack, members []db.StackMemberWithPR,
) (*db.MergeQueueState, error) {
	if !s.capabilitiesForRepo(repo).MergeQueueMutation {
		return nil, nil
	}
	number, baseBranch := mergeLandingBase(mr.Number, mr.BaseBranch, stack, members)
	state, err := s.db.GetMergeQueueState(ctx, repo.ID, number)
	if err != nil {
		return nil, httpapi.Internal("load merge queue state failed")
	}
	if state == nil || !state.Enabled || state.BaseBranch != baseBranch {
		return nil, nil
	}
	return state, nil
}

// mergeLandingBase returns the pull request whose base branch a merge of
// number lands on, and that branch. A native stack merge lands every
// unmerged member below number on the lowest one's base branch; any other
// merge lands on number's own base. Members must be ordered by position.
func mergeLandingBase(
	number int, baseBranch string, stack *db.Stack, members []db.StackMemberWithPR,
) (int, string) {
	if stack == nil || !stack.Native {
		return number, baseBranch
	}
	for _, member := range members {
		if member.Number == number {
			break
		}
		if member.State != string(db.MergeRequestStateMerged) {
			return member.Number, member.BaseBranch
		}
	}
	return number, baseBranch
}
