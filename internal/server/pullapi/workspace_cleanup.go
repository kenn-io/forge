package pullapi

type workspaceCleanupResult struct {
	Pending bool
	Warning string
}

func (s *Handler) queueMergedWorkspaceCleanup(workspaceID string) workspaceCleanupResult {
	if workspaceID == "" {
		return workspaceCleanupResult{}
	}
	if s.queueWorkspaceDeletion == nil {
		return workspaceCleanupResult{Warning: "workspace cleanup is unavailable"}
	}

	err := s.queueWorkspaceDeletion(workspaceID)
	if err != nil {
		return workspaceCleanupResult{Warning: err.Error()}
	}
	return workspaceCleanupResult{Pending: true}
}
