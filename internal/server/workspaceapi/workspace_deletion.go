package workspaceapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.kenn.io/forge/internal/db"
)

const (
	interruptedWorkspaceDeletionMessage = "workspace deletion was interrupted by a server restart; retry deletion to continue"
	interruptedWorkspaceSetupMessage    = "workspace setup was interrupted by a server restart; retry setup or delete the workspace"
)

var errWorkspaceDeletionInProgress = errors.New("workspace deletion already in progress")

type workspaceDirtyDeletionError struct {
	files []string
}

func (e *workspaceDirtyDeletionError) Error() string {
	return "workspace has uncommitted changes: " + strings.Join(e.files, ", ")
}

type workspaceDeletedEventData struct {
	WorkspaceID  string `json:"workspace_id"`
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	RepoPath     string `json:"repo_path"`
	Number       int    `json:"number"`
	ItemType     string `json:"item_type"`
}

func workspaceDeletedEvent(ws *db.Workspace) workspaceDeletedEventData {
	return workspaceDeletedEventData{
		WorkspaceID:  ws.ID,
		Provider:     ws.Platform,
		PlatformHost: ws.PlatformHost,
		Owner:        ws.RepoOwner,
		Name:         ws.RepoName,
		RepoPath:     ws.RepoOwner + "/" + ws.RepoName,
		Number:       ws.ItemNumber,
		ItemType:     ws.ItemType,
	}
}

// QueueWorkspaceDeletion durably admits teardown before returning, then runs
// the destructive work under the workspace domain's lifecycle.
func (s *Handler) QueueWorkspaceDeletion(id string) error {
	if s == nil || s.workspaces == nil || s.db == nil {
		return errors.New("workspace cleanup is unavailable")
	}
	ctx := s.lifecycleCtx
	ws, err := s.db.GetWorkspace(ctx, id)
	if err != nil {
		return fmt.Errorf("get workspace: %w", err)
	}
	if ws == nil {
		return nil
	}
	if ws.Status == "deleting" {
		return nil
	}
	if ws.Status == "creating" {
		return db.ErrWorkspaceSetupInProgress
	}
	started, err := s.db.BeginWorkspaceDeletion(ctx, id)
	if err != nil {
		return err
	}
	if !started {
		return nil
	}
	setupDone := s.markWorkspaceDeleting(id)
	s.broadcastWorkspaceStatus(id)
	if s.runBackground(func(ctx context.Context) {
		s.finishQueuedWorkspaceDeletion(ctx, ws, setupDone, false)
	}) {
		return nil
	}

	s.finishWorkspaceDeleting(id, false)
	s.persistWorkspaceDeletionFailure(id, "workspace deletion could not start because the server is shutting down")
	return errors.New("workspace deletion could not start because the server is shutting down")
}

func (s *Handler) finishQueuedWorkspaceDeletion(
	ctx context.Context, ws *db.Workspace, setupDone <-chan struct{}, force bool,
) {
	deleted := false
	defer func() { s.finishWorkspaceDeleting(ws.ID, deleted) }()
	err := s.runWorkspaceDeletion(ctx, ws.ID, setupDone, force, nil)
	if err != nil {
		s.persistWorkspaceDeletionFailure(ws.ID, err.Error())
		slog.Warn("delete queued workspace", "workspace_id", ws.ID, "err", err)
		return
	}
	deleted = true
	s.publishWorkspaceDeleted(ws)
}

func (s *Handler) runWorkspaceDeletion(
	ctx context.Context, id string, setupDone <-chan struct{}, force bool,
	admit func(context.Context) error,
) error {
	if s.runtime != nil {
		s.runtime.BeginStopping(id)
		defer s.runtime.EndStopping(id)
	}
	if err := waitForWorkspaceSetup(ctx, setupDone); err != nil {
		return fmt.Errorf("wait for workspace setup: %w", err)
	}
	dirty, err := s.workspaces.Delete(
		ctx, id, force,
		func(stopCtx context.Context) error {
			if admit != nil {
				if err := admit(stopCtx); err != nil {
					return err
				}
			}
			if s.runtime == nil {
				return nil
			}
			sessions := s.runtime.ListSessions(id)
			s.runtime.StopWorkspace(stopCtx, id)
			for _, session := range sessions {
				s.removeAgentActivityRuntimeSession(session.Key)
			}
			return nil
		},
	)
	if err != nil {
		return err
	}
	if len(dirty) > 0 {
		return &workspaceDirtyDeletionError{files: dirty}
	}
	return nil
}

func (s *Handler) persistWorkspaceDeletionFailure(id, message string) {
	if s == nil || s.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.db.FailWorkspaceDeletion(ctx, id, message); err != nil {
		slog.Error("persist workspace deletion failure", "workspace_id", id, "err", err)
		return
	}
	s.broadcastWorkspaceStatus(id)
}

func (s *Handler) publishWorkspaceDeleted(ws *db.Workspace) {
	s.hub.Broadcast(Event{Type: "workspace_deleted", Data: workspaceDeletedEvent(ws)})
	s.broadcastWorkspaceStatus(ws.ID)
}
