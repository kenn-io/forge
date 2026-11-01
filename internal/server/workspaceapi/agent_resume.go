package workspaceapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sort"
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
// restoreWorkspaceTerminals returns the workspaces it could not finish, a
// failed terminal start or an exhausted budget, and whether the workspace list
// could be read at all.
func (h *Handler) restoreWorkspaceTerminals(
	ctx context.Context, retainedWorkspaces map[string]bool, pendingOnly bool,
) (map[string]bool, bool) {
	workspaces, err := h.db.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("list workspace terminals for recovery", "err", err)
		return nil, false
	}
	unfinished := make(map[string]bool)
	for _, ws := range workspaces {
		if pendingOnly && !retainedWorkspaces[ws.ID] {
			continue
		}
		finished := false
		if ctx.Err() == nil {
			unlock := h.workspaceRuntimeLocks.Lock(ws.ID)
			finished = h.restoreWorkspaceTerminal(ctx, &ws, retainedWorkspaces)
			unlock()
		}
		if !finished {
			unfinished[ws.ID] = true
		}
	}
	return unfinished, true
}

// restoreTerminalsAfterServerLoss restores the base terminals of the remaining
// workspaces while Forge keeps running and returns those it could not finish:
// a busy workspace, a failed read or terminal start, or an exhausted budget.
// Each workspace takes setup admission and is read again under it, so a
// deletion either finishes first and is skipped, or waits.
func (h *Handler) restoreTerminalsAfterServerLoss(
	ctx context.Context, retainedWorkspaces, remaining map[string]bool,
) map[string]bool {
	left := maps.Clone(remaining)
	// A workspace startup restored for its agents stays eligible after they
	// resume, whatever its status.
	eligible := maps.Clone(retainedWorkspaces)
	if eligible == nil {
		eligible = make(map[string]bool)
	}
	for id, retained := range remaining {
		eligible[id] = eligible[id] || retained
	}
	// Start after the workspace the previous pass reached, so one that keeps
	// exhausting the budget cannot starve the rest.
	ids := slices.Sorted(maps.Keys(left))
	h.runtimeRecoveryMu.Lock()
	cursor := h.baseRestoreCursor
	h.runtimeRecoveryMu.Unlock()
	first := sort.SearchStrings(ids, cursor)
	if first < len(ids) && ids[first] == cursor {
		first++
	}
	ids = append(ids[first:], ids[:first]...)
	for _, id := range ids {
		if ctx.Err() != nil {
			return left
		}
		h.runtimeRecoveryMu.Lock()
		h.baseRestoreCursor = id
		h.runtimeRecoveryMu.Unlock()
		if h.restoreTerminalAfterServerLoss(ctx, id, eligible) {
			delete(left, id)
		}
	}
	return left
}

// restoreTerminalAfterServerLoss restores one workspace's base terminal under
// its runtime lock and setup admission, and reports whether the workspace is
// no longer outstanding.
func (h *Handler) restoreTerminalAfterServerLoss(ctx context.Context, id string, eligible map[string]bool) bool {
	defer h.workspaceRuntimeLocks.Lock(id)()
	done, start := h.beginWorkspaceSetup(id)
	if !start {
		// Admission without a wait channel means the workspace was
		// deleted, so its terminal is no longer outstanding.
		return done == nil
	}
	defer h.finishWorkspaceSetup(id, done)
	ws, err := h.db.GetWorkspace(ctx, id)
	switch {
	case err != nil:
		slog.Warn("read workspace terminal for recovery", "workspace_id", id, "err", err)
		return false
	case ws == nil:
		return true
	}
	return h.restoreWorkspaceTerminal(ctx, ws, eligible)
}

// restoreWorkspaceTerminal ensures a workspace's base terminal and reports
// whether the workspace is finished: restored, or not one recovery restores.
func (h *Handler) restoreWorkspaceTerminal(ctx context.Context, ws *db.Workspace, retainedWorkspaces map[string]bool) bool {
	if h.workspaces.UsesPtyOwnerForWorkspace(ws) {
		return true
	}
	if !workspaceStatusAllowsRecovery(ws.Status) || ws.TmuxSession == "" ||
		(ws.Status != "ready" && !retainedWorkspaces[ws.ID]) {
		return true
	}
	info, err := os.Stat(ws.WorktreePath)
	if err != nil || !info.IsDir() {
		return true
	}
	if err := h.workspaces.EnsureTerminal(ctx, ws); err != nil {
		slog.Warn("restore workspace terminal", "workspace_id", ws.ID, "err", err)
		return false
	}
	return true
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
