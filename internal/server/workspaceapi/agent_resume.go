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
	if h.agentActivity == nil || restored.Kind != localruntime.LaunchTargetAgent {
		return errors.New("no saved agent conversation")
	}
	reports := h.agentActivity.LiveReportsForWorkspace(restored.CWD, []string{restored.SessionKey})
	if len(reports) == 0 {
		return errors.New("no saved agent conversation")
	}
	report := reports[0]
	if err := h.workspaces.ValidateExecutionIdentity(ctx, restored.CWD); err != nil {
		return fmt.Errorf("validate resumed agent identity: %w", err)
	}
	if err := h.workspaces.PrepareAgentLaunchContext(ctx, workspace.PrepareAgentLaunchContextOptions{
		WorkspaceID: restored.WorkspaceID, TargetKey: restored.TargetKey,
	}); err != nil {
		return fmt.Errorf("prepare resumed agent context: %w", err)
	}
	session, err := h.runtime.Resume(ctx, restored, report.Agent, report.SessionID)
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
		if h.workspaces.UsesPtyOwnerForWorkspace(&ws) {
			continue
		}
		if !workspaceStatusAllowsRecovery(ws.Status) || ws.TmuxSession == "" ||
			(ws.Status != "ready" && !retainedWorkspaces[ws.ID]) {
			continue
		}
		info, err := os.Stat(ws.WorktreePath)
		if err != nil || !info.IsDir() {
			continue
		}
		if err := h.workspaces.EnsureTerminal(ctx, &ws); err != nil {
			slog.Warn("restore workspace terminal", "workspace_id", ws.ID, "err", err)
		}
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
