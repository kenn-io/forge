package workspaceapi

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"go.kenn.io/forge/internal/server/httpapi"
)

// WorkspaceViewState is the persisted workflow selection for one workspace.
type WorkspaceViewState struct {
	ActiveTab string `json:"active_tab" doc:"Selected workflow tab: home, terminal, or session:<key>; empty when unset"`
}

type updateWorkspaceViewStateInput struct {
	ID   string `path:"id"`
	Body WorkspaceViewState
}

func (s *Handler) getWorkspaceViewState(ctx context.Context, input *getWorkspaceInput) (*httpapi.BodyOutput[WorkspaceViewState], error) {
	activeTab, err := s.db.GetWorkspaceActiveTab(ctx, input.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, "workspace not found", nil)
	}
	if err != nil {
		return nil, httpapi.Internal("get workspace view state failed")
	}
	return &httpapi.BodyOutput[WorkspaceViewState]{Body: WorkspaceViewState{ActiveTab: activeTab}}, nil
}

func (s *Handler) updateWorkspaceViewState(ctx context.Context, input *updateWorkspaceViewStateInput) (*httpapi.BodyOutput[WorkspaceViewState], error) {
	activeTab := input.Body.ActiveTab
	sessionKey, isSession := strings.CutPrefix(activeTab, "session:")
	if activeTab != "home" && activeTab != "terminal" && (!isSession || strings.TrimSpace(sessionKey) == "") {
		return nil, httpapi.Validation("body.active_tab", "active_tab must be home, terminal, or session:<key>")
	}
	err := s.db.SetWorkspaceActiveTab(ctx, input.ID, activeTab)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, "workspace not found", nil)
	}
	if err != nil {
		return nil, httpapi.Internal("update workspace view state failed")
	}
	return &httpapi.BodyOutput[WorkspaceViewState]{Body: input.Body}, nil
}
