package pullapi

import (
	"context"
)

func (s *Handler) cleanupMergedWorkspace(ctx context.Context, workspaceID string) string {
	if workspaceID == "" {
		return ""
	}
	if s.deleteWorkspace == nil {
		return "workspace cleanup is unavailable"
	}

	err := s.deleteWorkspace(ctx, workspaceID)
	if err != nil {
		return err.Error()
	}
	return ""
}
