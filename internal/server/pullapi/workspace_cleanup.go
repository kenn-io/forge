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

// StartMergeWorkspaceCleanup resolves kept merge-queue workspace cleanups now,
// whenever sync wakes it, and periodically. The periodic pass retries
// deletions the owning daemon refused.
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
			case <-s.mergeWorkspaceCleanupWake:
			}
		}
	})
}

// WakeMergeWorkspaceCleanup asks the cleanup loop for a pass after sync
// records a pull request change. It never blocks.
func (s *Handler) WakeMergeWorkspaceCleanup() {
	select {
	case s.mergeWorkspaceCleanupWake <- struct{}{}:
	default:
	}
}

// resolveMergeWorkspaceCleanups deletes each kept workspace whose pull request
// sync recorded merged, and drops requests whose pull request closed or left
// the queue without merging.
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
		attrs := []any{
			"repo_id", cleanup.RepoID, "number", cleanup.Number,
			"workspace_id", cleanup.WorkspaceID, "host", cleanup.HostKey,
		}
		switch cleanup.Outcome {
		case db.MergeWorkspaceCleanupWaiting:
			continue
		case db.MergeWorkspaceCleanupMerged:
			if s.queueWorkspaceDeletion == nil {
				continue
			}
			if err := s.queueWorkspaceDeletion(ctx, cleanup.HostKey, cleanup.WorkspaceID); err != nil {
				slog.Warn("delete workspace after merge queue merge failed; will retry",
					append(attrs, "err", err)...)
				continue
			}
			slog.Info("deleting workspace after merge queue merged its pull request", attrs...)
		case db.MergeWorkspaceCleanupAbandoned:
			slog.Info("keeping workspace: pull request left the merge queue without merging", attrs...)
		}
		if err := s.db.DeleteMergeWorkspaceCleanup(ctx, cleanup.MergeWorkspaceCleanup); err != nil {
			slog.Error("forget merge queue workspace cleanup failed", append(attrs, "err", err)...)
		}
	}
}
