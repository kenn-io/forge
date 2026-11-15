package ghshim

import (
	"context"
	"errors"

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
			return nil, errors.New("pull request is not stored")
		}
		pulls = []db.MergeRequest{*pr}
	} else {
		if repo.LastSyncCompletedAt == nil || repo.LastSyncError != "" {
			return nil, errors.New("repository inventory is unavailable")
		}
		if q.State != "open" {
			states, err := database.ListArchiveRepoStates(ctx, []int64{repo.ID})
			if err != nil {
				return nil, err
			}
			// Inventory only discovers which pull requests exist; item sync
			// loads each row later. A finished initial full archive is the
			// point where every discovered pull request has a stored row.
			if len(states) != 1 || states[0].CollectionMode != db.ArchiveCollectionModeFull ||
				states[0].InitialCompletedAt == nil {
				return nil, errors.New("historical inventory is incomplete")
			}
		}
		// ProviderState follows gh: merge times mean merged, and locking leaves state unchanged.
		stored, err := database.ListMergeRequests(ctx, db.ListMergeRequestsOpts{
			RepoID: repo.ID, State: q.State, ProviderState: true,
			HeadBranch: q.Head, BaseBranch: q.Base, ByCreated: true, Limit: q.Limit,
		})
		if err != nil {
			return nil, err
		}
		pulls = stored
	}
	return Encode(q, pulls)
}
