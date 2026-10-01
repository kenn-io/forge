package workspaceapi

import (
	"context"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
)

// LinkWorkspacePullRequestService records explicit agent attribution using the
// same single association as branch discovery. It never replaces an existing PR.
func (s *Handler) LinkWorkspacePullRequestService(ctx context.Context, id string, number int, url string) (bool, error) {
	if number <= 0 || strings.TrimSpace(url) == "" {
		return false, httpapi.BadRequest(httpapi.CodeBadRequest, "PR number and URL are required", nil)
	}
	result, err := s.GetWorkspaceService(ctx, id)
	if err != nil {
		return false, err
	}
	ws, err := s.db.GetWorkspace(ctx, id)
	if err != nil {
		return false, httpapi.Internal("get workspace failed")
	}
	if ws == nil {
		return false, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, "workspace not found", nil)
	}
	repo, err := s.db.GetActiveRepoByID(ctx, ws.RepoID)
	if err != nil {
		return false, httpapi.Internal("get workspace repository failed")
	}
	if repo == nil || repo.Key != result.Workspace.Repo.Key {
		return false, httpapi.NotFound(httpapi.CodeRepoNotFound, "workspace repository is unavailable", nil)
	}
	facts, err := s.resolveMergeRequestWorktreeFacts(ctx, repo.Repo, db.PlatformIdentity{
		Platform: repo.Platform, Host: repo.PlatformHost, Owner: repo.Owner, Name: repo.Name,
	}, number)
	if err != nil {
		return false, err
	}
	if facts.URL != strings.TrimSpace(url) {
		return false, httpapi.BadRequest(httpapi.CodeBadRequest, "PR URL does not match the workspace repository and PR number", nil)
	}
	if ws.ItemType == db.WorkspaceItemTypePullRequest {
		if ws.ItemNumber == number {
			return true, nil
		}
		return false, httpapi.Conflict(httpapi.CodeConflict, "workspace already belongs to another PR", nil)
	}
	switch ws.ItemType {
	case db.WorkspaceItemTypeIssue, db.WorkspaceItemTypeKataTask, db.WorkspaceItemTypeAdHoc:
	default:
		return false, httpapi.BadRequest(httpapi.CodeBadRequest, "workspace cannot be linked to a PR", nil)
	}
	changed, err := s.db.SetWorkspaceAssociatedPRNumberIfNull(ctx, id, number)
	if err != nil {
		return false, httpapi.Internal("link workspace PR failed")
	}
	if !changed {
		current, err := s.db.GetWorkspace(ctx, id)
		if err != nil {
			return false, httpapi.Internal("read workspace PR association failed")
		}
		if current == nil || current.AssociatedPRNumber == nil || *current.AssociatedPRNumber != number {
			return false, httpapi.Conflict(httpapi.CodeConflict, "workspace already has another PR association or was deleted", nil)
		}
		return true, nil
	}
	s.broadcastWorkspaceStatus(id)
	s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	return false, nil
}
