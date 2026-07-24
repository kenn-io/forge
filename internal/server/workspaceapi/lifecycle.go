package workspaceapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.kenn.io/middleman/internal/workspace/localruntime"
)

// RestoreRuntimeSessions restores persisted workspace runtime sessions after
// the runtime manager has been constructed.
func (h *Handler) RestoreRuntimeSessions(ctx context.Context) error {
	if h == nil || h.db == nil || h.runtime == nil || h.workspaces == nil {
		return nil
	}
	stored, err := h.db.ListAllWorkspaceRuntimeSessions(ctx)
	if err != nil {
		return err
	}
	for _, session := range stored {
		summary, err := h.workspaces.GetSummary(ctx, session.WorkspaceID)
		if err != nil {
			return err
		}
		if summary == nil {
			continue
		}
		restored := localruntime.RestoredRuntimeSession{
			WorkspaceID: session.WorkspaceID,
			SessionKey:  session.SessionKey,
			TargetKey:   session.TargetKey,
			Label:       session.Label,
			Kind:        localruntime.LaunchTargetKind(session.Kind),
			TmuxSession: session.TmuxSession,
			CWD:         summary.WorktreePath,
			CreatedAt:   session.CreatedAt,
		}
		err = h.runtime.RestoreRuntimeSessions(ctx, []localruntime.RestoredRuntimeSession{restored})
		if err == nil {
			continue
		}
		if errors.Is(err, localruntime.ErrSessionNotFound) {
			if _, forgetErr := h.workspaces.ForgetRuntimeSessionCreatedAt(
				ctx, session.WorkspaceID, session.SessionKey, session.CreatedAt,
			); forgetErr != nil {
				return forgetErr
			}
			continue
		}
		if errors.Is(err, localruntime.ErrSessionUnavailable) {
			slog.Warn("runtime session unavailable after restore",
				"workspace_id", session.WorkspaceID,
				"session_key", session.SessionKey,
				"target_key", session.TargetKey,
				"tmux_session", session.TmuxSession,
				"err", err)
			continue
		}
		return err
	}
	return nil
}

// HandleRuntimeSessionExit reconciles a workspace runtime exit with persisted
// session and enrichment state.
func (h *Handler) HandleRuntimeSessionExit(info localruntime.SessionInfo) {
	if h == nil || h.workspaces == nil {
		return
	}
	h.invalidateWorkspaceEnrichment(info.WorkspaceID)
	if h.runBackground == nil {
		return
	}
	h.runBackground(func(ctx context.Context) {
		cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if _, err := h.workspaces.ForgetRuntimeSessionAfterExit(
			cleanupCtx, info.WorkspaceID, info.Key, info.CreatedAt, info.TmuxSession,
		); err != nil {
			slog.Warn("forget exited runtime session",
				"workspace_id", info.WorkspaceID,
				"session_key", info.Key,
				"err", err)
		}
	})
}
