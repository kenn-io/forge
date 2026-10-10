package workspaceapi

import (
	"context"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
)

type WorkerWorkspaceTargetRequest struct {
	Repository httpapi.RepoRefResponse `json:"repository"`
	Type       string                  `json:"type" enum:"pr,issue"`
	Number     int                     `json:"number" minimum:"1"`
	Hidden     bool                    `json:"hidden"`
	URL        string                  `json:"url" doc:"Canonical target URL verified by the controller"`
}

func (s *Handler) updateWorkerWorkspaceTarget(ctx context.Context, in *struct {
	ID   string `path:"id"`
	Body WorkerWorkspaceTargetRequest
},
) (*struct{}, error) {
	ws, err := s.db.GetWorkspace(ctx, in.ID)
	if err != nil {
		return nil, httpapi.Internal("get workspace failed")
	}
	if ws == nil {
		return nil, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, "workspace not found", nil)
	}
	request := in.Body
	identity := request.Repository.Identity()
	if !identity.Valid() {
		return nil, httpapi.Validation("body.repository", "verified repository identity is required")
	}
	if !request.Hidden {
		if strings.TrimSpace(request.URL) == "" {
			return nil, httpapi.Validation("body.url", "verified target URL is required")
		}
		// Tracking is provider metadata supplied by the controller, not Git execution admission.
		if _, err := s.db.ObserveRepository(ctx, db.RepoIdentity{
			Platform: identity.Provider, PlatformHost: identity.PlatformHost, Key: identity.Key,
			Owner: request.Repository.Owner, Name: request.Repository.Name, RepoPath: request.Repository.RepoPath,
		}); err != nil {
			return nil, httpapi.Internal("record target repository failed")
		}
	}
	repo, err := s.db.GetRepositoryByProviderID(ctx, identity)
	if err != nil {
		return nil, httpapi.Internal("get target repository failed")
	}
	if repo == nil {
		return nil, httpapi.NotFound(httpapi.CodeRepoNotFound, "target repository not found", nil)
	}
	return nil, s.saveWorkspaceTarget(ctx, in.ID, repo.Repository.ID, WorkspaceTargetSelection{Repository: identity, Type: request.Type, Number: request.Number, Hidden: request.Hidden}, request.URL)
}
