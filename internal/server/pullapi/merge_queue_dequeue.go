package pullapi

import (
	"context"
	"errors"
	"log/slog"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/platform"
)

// dequeuePR removes an open pull request from its base branch's merge queue.
// It turns the provider's auto-merge off first, because auto-merge left on
// would put the pull request straight back in the queue, and it stops Forge's
// waiting worker for the same reason. A kept workspace deletion stays, as a
// cancel keeps it: the pull request's merge still honors it.
func (s *Handler) dequeuePR(ctx context.Context, input *repoNumberInput) (*actionStatusOutput, error) {
	repo, err := s.requireRepoRouteCapability(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, capabilityMergeQueueMutation)
	if err != nil {
		return nil, err
	}
	if err := s.requireSyncerCapability(repo.Repo, capabilityMergeQueueMutation); err != nil {
		return nil, err
	}
	release := s.mergeLocks.Lock(deferredMergeKey(repo.Repo, input.Number))
	defer release()
	mr, err := s.visibleMergeRequest(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get pull request failed")
	}
	if mr == nil {
		return nil, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found", nil)
	}
	if mr.State != db.MergeRequestStateOpen {
		return nil, httpapi.Conflict(httpapi.CodeConflict, "pull request is not open", map[string]any{"reason": "not_open"})
	}
	kind, host := repoProviderKind(repo.Repo), repoProviderHost(repo.Repo)
	queue, err := s.syncer.MergeQueueMutator(kind, host)
	if err != nil {
		return nil, unsupportedCapabilityProblem(repo.Repo, capabilityMergeQueueMutation)
	}
	ref := platformRepoRefFromDB(repo.Repo)
	autoMerge, err := s.syncer.AutoMergeMutator(kind, host)
	switch {
	case err == nil:
		if err := autoMerge.DisableAutoMerge(ctx, ref, input.Number); err != nil &&
			!errors.Is(err, platform.ErrUnsupportedCapability) {
			return nil, httpapi.ProviderMutationProblem(err, string(kind), host)
		}
	case !errors.Is(err, platform.ErrUnsupportedCapability):
		return nil, httpapi.ProviderCallProblemWithDetail(err, string(kind), host, "turn auto-merge off: "+err.Error())
	}
	if err := queue.DequeueMergeRequest(ctx, ref, input.Number); err != nil {
		return nil, httpapi.ProviderMutationProblem(err, string(kind), host)
	}
	s.supersedeDeferredMerge(deferredMergeKey(repo.Repo, input.Number))
	// GitHub already removed it, so record that even if the caller is gone.
	// The provider refresh below keeps the request context: it runs under
	// the merge lock, and a stalled read must not hold the lock forever.
	persist := context.WithoutCancel(ctx)
	if err := s.db.SetMergeRequestAutoMergeEnabled(persist, repo.ID, input.Number, false); err != nil {
		slog.Warn("record auto-merge off after dequeue failed", "repo_id", repo.ID, "number", input.Number, "err", err)
	}
	// The provider confirmed the removal, so the stored entry is wrong now.
	// Clear it before the refresh, which may not run and records the
	// provider's removal event when it does.
	if stored, err := s.db.GetMergeQueueState(persist, repo.ID, input.Number); err == nil && stored != nil && stored.Queued {
		cleared := *stored
		cleared.Queued, cleared.Position, cleared.State = false, 0, ""
		cleared.EnqueuedAt, cleared.EstimatedTimeToMerge = nil, nil
		cleared.ObservedAt = s.now().UTC()
		if _, err := s.db.UpsertMergeQueueState(persist, repo.ID, cleared); err != nil {
			slog.Warn("clear merge queue entry after dequeue failed", "repo_id", repo.ID, "number", input.Number, "err", err)
		}
	}
	s.syncer.RefreshPullRequestMergeQueueForRepository(ctx, repo.ID, input.Number)
	s.publish(Event{Type: "data_changed", Data: struct{}{}})
	return &actionStatusOutput{Body: ActionStatusBody{Status: "dequeued"}}, nil
}
