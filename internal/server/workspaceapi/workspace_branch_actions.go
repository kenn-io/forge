package workspaceapi

import (
	"context"
	"errors"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/workspace"
)

type workspaceBranchActionInput struct {
	ID string `path:"id"`
}

type revealWorkspaceInput struct {
	ID string `path:"id"`
}

type workspaceBranchActionOutput = httpapi.BodyOutput[workspaceResponse]

var revealWorkspacePath = workspace.RevealWorktreePath

func (s *Handler) pushWorkspaceBranch(
	ctx context.Context,
	input *workspaceBranchActionInput,
) (*workspaceBranchActionOutput, error) {
	return s.runWorkspaceBranchAction(ctx, input.ID, s.workspaces.PushWorktreeBranch)
}

func (s *Handler) pullWorkspaceBranch(
	ctx context.Context,
	input *workspaceBranchActionInput,
) (*workspaceBranchActionOutput, error) {
	return s.runWorkspaceBranchAction(ctx, input.ID, s.workspaces.PullWorktreeBranch)
}

func (s *Handler) revealWorkspace(
	ctx context.Context,
	input *revealWorkspaceInput,
) (*struct{}, error) {
	summary, err := s.getWorkspaceActionSummary(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	if err := revealWorkspacePath(ctx, summary.WorktreePath); err != nil {
		if errors.Is(err, workspace.ErrRevealUnsupported) {
			return nil, httpapi.BadRequest(httpapi.CodeUnsupportedCapability, err.Error(), nil)
		}
		return nil, httpapi.Conflict(httpapi.CodeConflict, err.Error(), nil)
	}
	return nil, nil
}

func (s *Handler) runWorkspaceBranchAction(
	ctx context.Context,
	id string,
	action func(ctx context.Context, platformHost, dir string) error,
) (*workspaceBranchActionOutput, error) {
	summary, err := s.getWorkspaceActionSummary(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := action(ctx, summary.PlatformHost, summary.WorktreePath); err != nil {
		return nil, workspaceBranchActionProblem(err)
	}
	refreshed, err := s.workspaces.GetSummary(ctx, id)
	if err != nil {
		return nil, httpapi.Internal("get workspace summary: " + err.Error())
	}
	if refreshed == nil {
		return nil, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, "workspace not found", nil)
	}
	resp := s.refreshWorkspaceResponse(ctx, refreshed)
	return &workspaceBranchActionOutput{Body: resp}, nil
}

func (s *Handler) getWorkspaceActionSummary(
	ctx context.Context,
	id string,
) (*db.WorkspaceSummary, error) {
	if s.workspaces == nil {
		return nil, httpapi.ServiceUnavailable("workspace manager not configured")
	}
	summary, err := s.workspaces.GetSummary(ctx, id)
	if err != nil {
		return nil, httpapi.Internal("get workspace summary: " + err.Error())
	}
	if summary == nil {
		return nil, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, "workspace not found", nil)
	}
	return summary, nil
}

func workspaceBranchActionProblem(err error) error {
	switch {
	case errors.Is(err, workspace.ErrWorktreeDirty):
		return httpapi.Conflict(httpapi.CodeWorktreeDirty, err.Error(), nil)
	case errors.Is(err, workspace.ErrWorktreeDiverged):
		return httpapi.Conflict(httpapi.CodeBranchConflict, err.Error(), nil)
	case errors.Is(err, workspace.ErrWorktreeNoUpstream),
		errors.Is(err, workspace.ErrWorktreeInSync):
		return httpapi.Conflict(httpapi.CodeConflict, err.Error(), nil)
	default:
		return httpapi.Conflict(httpapi.CodeConflict, err.Error(), nil)
	}
}
