package workspaceapi

import (
	"context"
	"log/slog"
	"time"
)

func (s *Server) runWorkspacePRMonitorLoop(ctx context.Context) {
	if s.workspacePRMonitor == nil {
		return
	}

	s.runWorkspacePRMonitorPass(ctx)

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runWorkspacePRMonitorPass(ctx)
		}
	}
}

func (s *Server) runWorkspacePRMonitorPass(ctx context.Context) {
	if s.workspacePRMonitor == nil {
		return
	}

	updates, err := s.workspacePRMonitor.RunOnce(ctx)
	if err != nil {
		slog.Warn("workspace PR monitor pass failed", "err", err)
		return
	}
	for i := range updates {
		update := updates[i]
		s.broadcastWorkspaceStatus(update.WorkspaceID)
		s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	}
}

func (s *Server) runWorkspacePushedHeadObserverLoop(ctx context.Context) {
	if s.workspacePushedHeadObserver == nil {
		return
	}

	s.runWorkspacePushedHeadObserverPass(ctx)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runWorkspacePushedHeadObserverPass(ctx)
		}
	}
}

func (s *Server) runWorkspacePushedHeadObserverPass(ctx context.Context) {
	if s.workspacePushedHeadObserver == nil {
		return
	}

	result, err := s.workspacePushedHeadObserver.RunOnce(ctx)
	if err != nil {
		slog.Warn("workspace pushed-head observer pass failed", "err", err)
		return
	}
	for i := range result.Associations {
		association := result.Associations[i]
		s.hub.Broadcast(Event{
			Type: "workspace_pr_associated",
			Data: workspacePRAssociatedPayload{
				WorkspaceID:  association.WorkspaceID,
				Provider:     string(association.Provider),
				PlatformHost: association.PlatformHost,
				RepoPath:     association.RepoPath,
				Owner:        association.Owner,
				Name:         association.Name,
				IssueNumber:  association.IssueNumber,
				PRNumber:     association.PRNumber,
				AssociatedAt: formatUTCRFC3339(association.AssociatedAt),
			},
		})
		s.broadcastWorkspaceStatus(association.WorkspaceID)
		s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	}
	for i := range result.HeadChanges {
		change := result.HeadChanges[i]
		s.hub.Broadcast(Event{
			Type: "workspace_pushed_head_changed",
			Data: workspacePushedHeadChangedPayload{
				WorkspaceID:  change.WorkspaceID,
				Provider:     string(change.Provider),
				PlatformHost: change.PlatformHost,
				RepoPath:     change.RepoPath,
				Owner:        change.Owner,
				Name:         change.Name,
				Number:       change.Number,
				OldSHA:       change.OldSHA,
				NewSHA:       change.NewSHA,
				Remote:       change.RemoteName,
				Branch:       change.BranchName,
				TrackingRef:  change.TrackingRef,
				ObservedAt:   formatUTCRFC3339(change.ObservedAt),
			},
		})
		s.enqueueWorkspacePushedHeadRefresh(change)
	}
}

func (s *Server) broadcastWorkspaceStatus(workspaceID string) {
	s.hub.Broadcast(Event{
		Type: "workspace_status",
		Data: map[string]string{"id": workspaceID},
	})
}
