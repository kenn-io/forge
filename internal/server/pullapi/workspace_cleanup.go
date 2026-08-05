package pullapi

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/workspace"
)

const (
	workspaceCleanupDeleted              = "deleted"
	workspaceCleanupAlreadyAbsent        = "already_absent"
	workspaceCleanupNotFoundAtSubmission = "not_found_at_submission"
	workspaceCleanupFailed               = "failed"
)

type workspaceCleanupPlan struct {
	Requested   bool
	WorkspaceID string
}

func (s *Handler) captureWorkspaceCleanupPlan(
	ctx context.Context,
	repo db.Repo,
	number int,
	requested bool,
) (workspaceCleanupPlan, error) {
	plan := workspaceCleanupPlan{Requested: requested}
	if !requested {
		return plan, nil
	}
	if s.workspaces == nil {
		return workspaceCleanupPlan{}, errors.New("workspace manager not configured")
	}
	ws, err := s.workspaces.GetByMRForProvider(
		ctx,
		repo.Platform,
		repoProviderHost(repo),
		repo.Owner,
		repo.Name,
		number,
	)
	if err != nil {
		return workspaceCleanupPlan{}, err
	}
	if ws != nil {
		plan.WorkspaceID = ws.ID
	}
	return plan, nil
}

func (s *Handler) executeWorkspaceCleanup(
	ctx context.Context,
	plan workspaceCleanupPlan,
) *WorkspaceCleanupResult {
	if !plan.Requested {
		return nil
	}
	if plan.WorkspaceID == "" {
		return &WorkspaceCleanupResult{Status: workspaceCleanupNotFoundAtSubmission}
	}
	if s.deleteWorkspace == nil {
		warning := "workspace cleanup is unavailable"
		slog.Warn("workspace cleanup after merge failed",
			"workspace_id", plan.WorkspaceID,
			"err", warning)
		return &WorkspaceCleanupResult{
			WorkspaceID: plan.WorkspaceID,
			Status:      workspaceCleanupFailed,
			Warning:     warning,
		}
	}
	dirty, err := s.deleteWorkspace(ctx, plan.WorkspaceID, false)
	if errors.Is(err, workspace.ErrWorkspaceNotFound) {
		return &WorkspaceCleanupResult{
			WorkspaceID: plan.WorkspaceID,
			Status:      workspaceCleanupAlreadyAbsent,
		}
	}
	if err != nil {
		slog.Warn("workspace cleanup after merge failed",
			"workspace_id", plan.WorkspaceID,
			"err", err)
		return &WorkspaceCleanupResult{
			WorkspaceID: plan.WorkspaceID,
			Status:      workspaceCleanupFailed,
			Warning:     err.Error(),
		}
	}
	if len(dirty) > 0 {
		return &WorkspaceCleanupResult{
			WorkspaceID: plan.WorkspaceID,
			Status:      workspaceCleanupFailed,
			Warning:     "workspace has uncommitted changes: " + strings.Join(dirty, ", "),
		}
	}
	return &WorkspaceCleanupResult{
		WorkspaceID: plan.WorkspaceID,
		Status:      workspaceCleanupDeleted,
	}
}
