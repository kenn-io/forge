package workspaceapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func (h *Handler) resumeWorkspaceAgent(ctx context.Context, stored db.WorkspaceRuntimeSession, restored localruntime.RestoredRuntimeSession) error {
	if restored.Kind == localruntime.LaunchTargetAgent && stored.ConversationID == "" {
		stored = h.importReportedConversation(ctx, stored, restored.CWD)
	}
	if restored.Kind != localruntime.LaunchTargetAgent || stored.ConversationID == "" {
		return errors.New("no saved agent conversation")
	}
	if err := h.workspaces.ValidateExecutionIdentity(ctx, restored.CWD); err != nil {
		return fmt.Errorf("validate resumed agent identity: %w", err)
	}
	if err := h.workspaces.PrepareAgentLaunchContext(ctx, workspace.PrepareAgentLaunchContextOptions{
		WorkspaceID: restored.WorkspaceID, TargetKey: restored.TargetKey,
	}); err != nil {
		return fmt.Errorf("prepare resumed agent context: %w", err)
	}
	session, err := h.runtime.Resume(ctx, restored, stored.ConversationAgent, stored.ConversationID)
	if err != nil {
		return err
	}
	if err := h.recordRestartedRuntime(ctx, stored, session); err != nil {
		return err
	}
	h.setRuntimeRecoveryPending(session.Key, false)
	h.forgetRecordedRuntimeSessionIfExited(ctx, session)
	slog.Info("resumed workspace agent", "workspace_id", session.WorkspaceID, "target_key", session.TargetKey)
	return nil
}

// recordRestartedRuntime records a runtime restarted under a stored row's key,
// or stops it when the record fails.
func (h *Handler) recordRestartedRuntime(ctx context.Context, stored db.WorkspaceRuntimeSession, session localruntime.SessionInfo) error {
	session.DisplayRegion = stored.DisplayRegion
	if err := h.recordRuntimeSession(ctx, stored.WorkspaceID, session, stored.Scope); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if cleanupErr := h.runtime.RollbackLaunch(cleanupCtx, session); cleanupErr != nil {
			slog.Warn("roll back restarted runtime", "session_key", session.Key, "err", cleanupErr)
		}
		return err
	}
	return nil
}

// restoredRuntime describes a stored row for the runtime manager.
func restoredRuntime(stored db.WorkspaceRuntimeSession, cwd string) localruntime.RestoredRuntimeSession {
	return localruntime.RestoredRuntimeSession{
		WorkspaceID: stored.WorkspaceID, SessionKey: stored.SessionKey, TargetKey: stored.TargetKey, Label: stored.Label,
		Kind: localruntime.LaunchTargetKind(stored.Kind), TmuxSession: stored.TmuxSession, CWD: cwd, CreatedAt: stored.CreatedAt,
	}
}

// Restore tmux base terminals before agents create new tmux sessions. Otherwise
// the periodic prune would mistake the remaining missing bases for individual
// exits. Pty-owner bases are left to the attach path, which reuses a live owner.
func (h *Handler) restoreWorkspaceTerminals(ctx context.Context, retainedWorkspaces map[string]bool, pendingOnly bool) {
	workspaces, err := h.db.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("list workspace terminals for recovery", "err", err)
		return
	}
	for _, ws := range workspaces {
		if ctx.Err() != nil {
			return
		}
		if pendingOnly && !retainedWorkspaces[ws.ID] {
			continue
		}
		h.restoreWorkspaceTerminal(ctx, &ws, retainedWorkspaces)
	}
}

// restoreTerminalsAfterServerLoss restores every base terminal while Forge
// keeps running. Each workspace takes setup admission and is read again under
// it, so a deletion either finishes first and is skipped, or waits. It reports
// whether every workspace was reached; a busy workspace or an exhausted budget
// leaves the rest for the next pass.
func (h *Handler) restoreTerminalsAfterServerLoss(ctx context.Context, retainedWorkspaces map[string]bool) bool {
	workspaces, err := h.db.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("list workspace terminals for recovery", "err", err)
		return false
	}
	complete := true
	for _, listed := range workspaces {
		if ctx.Err() != nil {
			return false
		}
		done, start := h.beginWorkspaceSetup(listed.ID)
		if !start {
			complete = false
			continue
		}
		ws, err := h.db.GetWorkspace(ctx, listed.ID)
		if err != nil {
			slog.Warn("read workspace terminal for recovery", "workspace_id", listed.ID, "err", err)
		} else if ws != nil {
			h.restoreWorkspaceTerminal(ctx, ws, retainedWorkspaces)
		}
		h.finishWorkspaceSetup(listed.ID, done)
	}
	return complete
}

func (h *Handler) restoreWorkspaceTerminal(ctx context.Context, ws *db.Workspace, retainedWorkspaces map[string]bool) {
	if h.workspaces.UsesPtyOwnerForWorkspace(ws) {
		return
	}
	if !workspaceStatusAllowsRecovery(ws.Status) || ws.TmuxSession == "" ||
		(ws.Status != "ready" && !retainedWorkspaces[ws.ID]) {
		return
	}
	info, err := os.Stat(ws.WorktreePath)
	if err != nil || !info.IsDir() {
		return
	}
	if err := h.workspaces.EnsureTerminal(ctx, ws); err != nil {
		slog.Warn("restore workspace terminal", "workspace_id", ws.ID, "err", err)
	}
}

func workspaceStatusAllowsRecovery(status string) bool {
	return status == "ready" || status == "creating" || status == "error"
}

func (h *Handler) setRuntimeRecoveryPending(key string, pending bool) {
	h.runtimeRecoveryMu.Lock()
	defer h.runtimeRecoveryMu.Unlock()
	if pending {
		if h.runtimeRecoveryPending == nil {
			h.runtimeRecoveryPending = make(map[string]bool)
		}
		h.runtimeRecoveryPending[key] = true
	} else {
		delete(h.runtimeRecoveryPending, key)
	}
}
