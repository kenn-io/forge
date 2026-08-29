package pullapi

import (
	"context"

	"go.kenn.io/forge/internal/federationauth"
)

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
