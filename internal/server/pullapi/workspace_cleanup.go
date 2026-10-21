package pullapi

import (
	"context"
	"log/slog"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
)

// mergeWorkspaceCleanupRetryInterval is how often kept merge-queue workspace
// cleanups are re-resolved, retrying deletions the owning daemon refused.
const mergeWorkspaceCleanupRetryInterval = 5 * time.Minute

type workspaceCleanupResult struct {
	Pending bool
	Warning string
}

func (body *mergePRInputBody) bindWorkspaceHost(ctx context.Context) {
	if body.workspaceHostKey != "" {
		return
	}
	if principal, ok := federationauth.PrincipalFromContext(ctx); ok {
		body.workspaceHostKey = principal.NodeID
	}
}

func (s *Handler) queueMergedWorkspaceCleanup(
	ctx context.Context, hostKey, workspaceID string,
) workspaceCleanupResult {
	if workspaceID == "" {
		return workspaceCleanupResult{}
	}
	if s.queueWorkspaceDeletion == nil {
		return workspaceCleanupResult{Warning: "workspace cleanup is unavailable"}
	}

	err := s.queueWorkspaceDeletion(ctx, hostKey, workspaceID)
	if err != nil {
		return workspaceCleanupResult{Warning: err.Error()}
	}
	return workspaceCleanupResult{Pending: true}
}

// keepQueuedMergeWorkspaceCleanup keeps a workspace deletion requested with a
// merge that went to the merge queue. The queue merges the pull request
// later, so the deletion waits until sync records that merge.
func (s *Handler) keepQueuedMergeWorkspaceCleanup(
	ctx context.Context, cleanup db.MergeWorkspaceCleanup,
) workspaceCleanupResult {
	if cleanup.WorkspaceID == "" {
		return workspaceCleanupResult{}
	}
	// The provider already accepted the enqueue, so keep the request even if
	// the call that made it was cancelled.
	if err := s.db.SaveMergeWorkspaceCleanup(context.WithoutCancel(ctx), cleanup); err != nil {
		slog.Error("keep workspace cleanup for merge queue failed",
			"repo_id", cleanup.RepoID, "number", cleanup.Number,
			"workspace_id", cleanup.WorkspaceID, "err", err)
		return workspaceCleanupResult{
			Warning: "the workspace deletion could not be scheduled",
		}
	}
	return workspaceCleanupResult{}
}

// ResolveMergeWorkspaceCleanup acts on a pull request's kept merge-queue
// workspace cleanup after sync records a change to it. Sync calls it from
// its own goroutine, so the work runs in the background.
func (s *Handler) ResolveMergeWorkspaceCleanup(_ context.Context, repoID int64, number int) {
	s.runBackground(func(ctx context.Context) {
		cleanup, err := s.db.GetMergeWorkspaceCleanup(ctx, repoID, number)
		if err != nil && ctx.Err() == nil {
			slog.Error("read merge queue workspace cleanup failed",
				"repo_id", repoID, "number", number, "err", err)
		}
		if cleanup != nil {
			s.resolveMergeWorkspaceCleanup(ctx, *cleanup)
		}
	})
}

// StartMergeWorkspaceCleanup resolves every kept merge-queue workspace
// cleanup now and then periodically. Sync may have recorded a merge while
// this daemon was stopped, or the owning daemon may have refused a deletion.
func (s *Handler) StartMergeWorkspaceCleanup() {
	s.runBackground(func(ctx context.Context) {
		ticker := time.NewTicker(mergeWorkspaceCleanupRetryInterval)
		defer ticker.Stop()
		for {
			s.resolveMergeWorkspaceCleanups(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (s *Handler) resolveMergeWorkspaceCleanups(ctx context.Context) {
	cleanups, err := s.db.ListMergeWorkspaceCleanups(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("list merge queue workspace cleanups failed", "err", err)
		}
		return
	}
	for _, cleanup := range cleanups {
		if ctx.Err() != nil {
			return
		}
		s.resolveMergeWorkspaceCleanup(ctx, cleanup)
	}
}

// resolveMergeWorkspaceCleanup deletes the workspace once sync has recorded
// the pull request merged, and drops the request once it was closed or left
// the queue without merging. Workspace deletion admission is idempotent, so
// a concurrent resolution of the same request is harmless.
func (s *Handler) resolveMergeWorkspaceCleanup(
	ctx context.Context, cleanup db.ObservedMergeWorkspaceCleanup,
) {
	attrs := []any{
		"repo_id", cleanup.RepoID, "number", cleanup.Number,
		"workspace_id", cleanup.WorkspaceID, "host", cleanup.HostKey,
	}
	switch cleanup.Outcome {
	case db.MergeWorkspaceCleanupWaiting:
		return
	case db.MergeWorkspaceCleanupMerged:
		if s.queueWorkspaceDeletion == nil {
			return
		}
		if err := s.queueWorkspaceDeletion(ctx, cleanup.HostKey, cleanup.WorkspaceID); err != nil {
			slog.Warn("delete workspace after merge queue merge failed; will retry",
				append(attrs, "err", err)...)
			return
		}
		slog.Info("deleting workspace after merge queue merged its pull request", attrs...)
	case db.MergeWorkspaceCleanupAbandoned:
		slog.Info("keeping workspace: pull request left the merge queue without merging", attrs...)
	}
	if err := s.db.DeleteMergeWorkspaceCleanup(ctx, cleanup.MergeWorkspaceCleanup); err != nil {
		slog.Error("forget merge queue workspace cleanup failed", append(attrs, "err", err)...)
	}
}
