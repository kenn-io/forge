package ghshim

import (
	"context"
	"fmt"
	"slices"

	"go.kenn.io/forge/internal/db"
)

// Read projects Forge's persisted snapshot. It never refreshes data, calls a
// provider, or applies a second freshness policy on top of normal Forge sync.
// An unavailable snapshot tells the shim to delegate the original call to gh.
func Read(ctx context.Context, database *db.DB, repo db.Repo, q Query) ([]byte, error) {
	var pulls []db.MergeRequest
	if q.Command == "view" {
		pr, err := database.GetVisibleMergeRequestByRepoIDAndNumber(ctx, repo.ID, q.Number)
		if err != nil {
			return nil, err
		}
		if pr == nil {
			return nil, fmt.Errorf("pull request is not stored")
		}
		pulls = []db.MergeRequest{*pr}
	} else {
		if repo.LastSyncCompletedAt == nil || repo.LastSyncError != "" {
			return nil, fmt.Errorf("repository inventory is unavailable")
		}
		if q.State != "open" {
			states, err := database.ListArchiveRepoStates(ctx, []int64{repo.ID})
			if err != nil {
				return nil, err
			}
			if len(states) != 1 || !states[0].MergeRequestInventory.Complete() {
				return nil, fmt.Errorf("historical inventory is incomplete")
			}
		}
		// Read all states: the dashboard's open/closed filters also classify locked
		// PRs as closed. gh filters the provider state independently of locking.
		stored, err := database.ListMergeRequests(ctx, db.ListMergeRequestsOpts{RepoID: repo.ID, State: "all"})
		if err != nil {
			return nil, err
		}
		for _, pr := range stored {
			state := pullState(pr)
			matchesState := q.State == "all" || string(state) == q.State || (q.State == "closed" && state == db.MergeRequestStateMerged)
			if !matchesState {
				continue
			}
			if (q.Head != "" && pr.HeadBranch != q.Head) || (q.Base != "" && pr.BaseBranch != q.Base) {
				continue
			}
			pulls = append(pulls, pr)
		}
		slices.SortStableFunc(pulls, func(a, b db.MergeRequest) int { return b.CreatedAt.Compare(a.CreatedAt) })
		if len(pulls) > q.Limit {
			pulls = pulls[:q.Limit]
		}
	}
	return Encode(q, pulls)
}
