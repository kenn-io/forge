package workspaceapi

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ptyowner"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// Start launches workspace-owned background work once. The parent context is
// observed in addition to explicit Shutdown calls so root cancellation cannot
// strand the domain.
func (h *Handler) Start(parent context.Context, disableMonitors bool) {
	if h == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	h.lifecycleMu.Lock()
	if h.lifecycleStarted || h.lifecycleStopping {
		h.lifecycleMu.Unlock()
		return
	}
	h.lifecycleStarted = true
	h.lifecycleMu.Unlock()

	if h.db != nil {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := h.db.FailInterruptedWorkspaceSetups(
			recoveryCtx, interruptedWorkspaceSetupMessage,
		); err != nil {
			slog.Error("recover interrupted workspace setups", "err", err)
		}
		if err := h.db.FailInterruptedWorkspaceDeletions(
			recoveryCtx, interruptedWorkspaceDeletionMessage,
		); err != nil {
			slog.Error("recover interrupted workspace deletions", "err", err)
		}
		cancel()
	}

	h.runBackground(func(ctx context.Context) {
		select {
		case <-parent.Done():
			h.stopBackground()
		case <-ctx.Done():
		}
	})
	if h.workspaces != nil && !disableMonitors {
		h.runBackground(h.runWorkspaceWarmLoop)
		h.runBackground(h.runIdle)
		if !h.executionWorker.Enabled {
			h.runBackground(h.runWorkspacePRMonitorLoop)
			h.runBackground(h.runWorkspacePushedHeadObserverLoop)
		}
	}
}

func (h *Handler) runWorkspaceWarmLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := h.workspaces.WarmWorktrees(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("warm workspace worktrees", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-h.workspaceWarmWake:
		}
	}
}

func (h *Handler) wakeWorkspaceWarmer() {
	select {
	case h.workspaceWarmWake <- struct{}{}:
	default:
	}
}

func (h *Handler) runBackground(run func(context.Context)) bool {
	if h == nil || run == nil {
		return false
	}
	h.lifecycleMu.Lock()
	if h.lifecycleStopping {
		h.lifecycleMu.Unlock()
		return false
	}
	h.lifecycleWG.Add(1)
	ctx := h.lifecycleCtx
	h.lifecycleMu.Unlock()
	go func() {
		defer h.lifecycleWG.Done()
		run(ctx)
	}()
	return true
}

func (h *Handler) stopBackground() {
	h.lifecycleMu.Lock()
	if h.lifecycleStopping {
		h.lifecycleMu.Unlock()
		return
	}
	h.lifecycleStopping = true
	h.lifecycleCancel()
	done := h.lifecycleDone
	h.lifecycleMu.Unlock()
	go func() {
		h.lifecycleWG.Wait()
		if h.workspaceDiffCache != nil {
			h.workspaceDiffCache.Wait()
		}
		close(done)
	}()
}

// Shutdown stops Workspace background work and waits for it within ctx. It is
// safe to call concurrently and repeatedly; later callers can wait with a
// longer context after an earlier deadline expires.
func (h *Handler) Shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.stopBackground()
	select {
	case <-h.lifecycleDone:
		if h.workerBroker != nil {
			h.workerBroker.Close()
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RestoreRuntimeSessions restores persisted workspace runtime sessions after
// the runtime manager has been constructed.
func (h *Handler) RestoreRuntimeSessions(ctx context.Context) error {
	// Idle stop turns on or off before recovery reads its marks; off at
	// startup, it hands what it stopped to recovery and removes its state.
	if h.syncIdle(ctx) == nil {
		h.releaseIdle(ctx)
	}
	// Records of agents a reboot killed name tmux sessions that are gone.
	// Prune them before recovery resumes agents under the same names.
	if h.runtime != nil {
		h.runtime.PruneAgentExits(ctx)
	}
	return h.restoreRuntimeSessions(ctx, false)
}

func (h *Handler) restoreRuntimeSessions(ctx context.Context, pendingOnly bool) error {
	if h == nil || h.db == nil || h.runtime == nil || h.workspaces == nil {
		return nil
	}
	h.runtimeRestoreMu.Lock()
	defer h.runtimeRestoreMu.Unlock()
	stored, err := h.db.ListAllWorkspaceRuntimeSessions(ctx)
	if err != nil {
		return err
	}
	if pendingOnly {
		h.runtimeRecoveryMu.Lock()
		pending := stored[:0]
		keptAfterLoss := make(map[string]bool)
		var newlyKept []string
		for _, session := range stored {
			if !h.runtimeRecoveryPending[session.SessionKey] || session.Kind == string(localruntime.LaunchTargetACP) {
				continue
			}
			// A runtime refuses to restore a key it saw exit, so an agent
			// kept after a tmux server loss waits for the next start.
			if h.runtime.Exited(session.SessionKey) {
				keptAfterLoss[session.WorkspaceID] = true
				if !h.baseRestoreCovered[session.SessionKey] {
					if h.baseRestoreCovered == nil {
						h.baseRestoreCovered = make(map[string]bool)
					}
					h.baseRestoreCovered[session.SessionKey] = true
					newlyKept = append(newlyKept, session.SessionKey)
				}
				continue
			}
			pending = append(pending, session)
		}
		h.runtimeRecoveryMu.Unlock()
		h.restoreBaseTerminalsAfterLoss(ctx, keptAfterLoss, newlyKept)
		stored = pending
		if len(stored) == 0 {
			return nil
		}
	}
	if pendingOnly {
		// Each maintenance pass attempts one retained session. Advance before
		// doing I/O so a timeout cannot monopolize every subsequent pass.
		slices.SortFunc(stored, func(a, b db.WorkspaceRuntimeSession) int {
			return cmp.Compare(a.SessionKey, b.SessionKey)
		})
		next := 0
		for i, session := range stored {
			if session.SessionKey > h.runtimeRecoveryCursor {
				next = i
				break
			}
		}
		stored = stored[next : next+1]
		h.runtimeRecoveryCursor = stored[0].SessionKey
		// Share setup admission so recovery cannot prepare context or touch
		// a terminal while setup or deletion owns the workspace.
		done, start := h.beginWorkspaceSetup(stored[0].WorkspaceID)
		if !start {
			return nil
		}
		defer h.finishWorkspaceSetup(stored[0].WorkspaceID, done)
	}
	if !pendingOnly {
		if workspaces, err := h.db.ListWorkspaces(ctx); err == nil {
			h.idle.Load().Retain(stored, workspaces)
		}
	}
	// A runtime idle stop stopped resumes when its workspace shows, not at
	// startup, so its workspace is not retained for recovery.
	idle := h.idle.Load()
	stored = slices.DeleteFunc(stored, func(session db.WorkspaceRuntimeSession) bool { return idle.Stopped(ctx, session) })
	retainedWorkspaces := make(map[string]bool)
	for _, session := range stored {
		if session.Kind == string(localruntime.LaunchTargetAgent) {
			retainedWorkspaces[session.WorkspaceID] = true
			h.setRuntimeRecoveryPending(session.SessionKey, true)
		}
	}
	h.restoreWorkspaceTerminals(ctx, retainedWorkspaces, pendingOnly)
	for _, session := range stored {
		// ACP owners keep running without daemon attachments. Restore only when
		// this workspace is opened, and retain its row until then.
		if session.Kind == string(localruntime.LaunchTargetACP) {
			h.setRuntimeRecoveryPending(session.SessionKey, true)
			continue
		}
		summary, err := h.workspaces.GetSummary(ctx, session.WorkspaceID)
		if err != nil {
			return err
		}
		if summary == nil || (pendingOnly && !workspaceStatusAllowsRecovery(summary.Status)) {
			continue
		}
		if session.Kind == string(localruntime.LaunchTargetAgent) && session.ConversationID == "" {
			session = h.importReportedConversation(ctx, session, summary.WorktreePath)
		}
		restored := restoredRuntime(session, summary.WorktreePath)
		err = h.runtime.RestoreRuntimeSessions(ctx, []localruntime.RestoredRuntimeSession{restored})
		if err == nil {
			h.setRuntimeRecoveryPending(session.SessionKey, false)
			if session.Kind == string(localruntime.LaunchTargetAgent) {
				h.noteTmuxServer(ctx, session.SessionKey, session.TmuxSession)
			}
			continue
		}
		// Resume a missing tmux agent or a gone pty-owner agent from its saved
		// conversation.
		gone := errors.Is(err, localruntime.ErrSessionNotFound) || errors.Is(err, ptyowner.ErrOwnerGone)
		if gone && workspaceStatusAllowsRecovery(summary.Status) && session.Kind == string(localruntime.LaunchTargetAgent) &&
			session.ConversationID != "" {
			if resumeErr := h.resumeWorkspaceAgent(ctx, session, restored); resumeErr != nil {
				slog.Warn("could not resume workspace agent; retained for recovery", "workspace_id", session.WorkspaceID, "session_key", session.SessionKey, "err", resumeErr)
			}
			continue
		}
		if errors.Is(err, localruntime.ErrSessionNotFound) {
			slog.Info("forget runtime session with nothing to resume",
				"workspace_id", session.WorkspaceID,
				"session_key", session.SessionKey,
				"target_key", session.TargetKey,
				"kind", session.Kind)
			if _, forgetErr := h.workspaces.ForgetRuntimeSessionCreatedAt(
				ctx, session.WorkspaceID, session.SessionKey, session.CreatedAt,
			); forgetErr != nil {
				return forgetErr
			}
			// The agent exited while the daemon was down, so no exit hook
			// removed its activity report; reports do not expire on their
			// own, so the forgotten runtime takes its report with it.
			h.removeAgentActivityRuntimeSession(session.SessionKey)
			h.setRuntimeRecoveryPending(session.SessionKey, false)
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
	h.reconcileAgentActivityReports(ctx)
	return nil
}

// reconcileAgentActivityReports drops agent activity reports whose runtime
// session is neither persisted nor live. Startup pruning and the periodic
// missing-tmux prune delete runtime rows without an exit hook, and reports do
// not expire on their own, so this is what keeps the report directory from
// collecting sessions that died while the daemon was down.
func (h *Handler) reconcileAgentActivityReports(ctx context.Context) {
	if h == nil || h.agentActivity == nil || h.db == nil {
		return
	}
	keep := map[string]struct{}{}
	stored, err := h.db.ListAllWorkspaceRuntimeSessions(ctx)
	if err != nil {
		slog.Warn("reconcile agent activity reports: list runtime sessions", "err", err)
		return
	}
	for _, session := range stored {
		keep[session.SessionKey] = struct{}{}
	}
	if h.runtime != nil {
		summaries, err := h.db.ListWorkspaceSummaries(ctx)
		if err != nil {
			slog.Warn("reconcile agent activity reports: list workspaces", "err", err)
			return
		}
		for _, summary := range summaries {
			for _, session := range h.runtime.ListSessions(summary.ID) {
				keep[session.Key] = struct{}{}
			}
		}
	}
	if err := h.agentActivity.RetainRuntimeSessions(keep); err != nil {
		slog.Warn("reconcile agent activity reports", "err", err)
	}
}

// HandleRuntimeSessionExit reconciles a workspace runtime exit with persisted
// session and enrichment state.
func (h *Handler) HandleRuntimeSessionExit(info localruntime.SessionInfo) {
	if h == nil {
		return
	}
	h.runtimeRecoveryMu.Lock()
	pending := h.runtimeRecoveryPending[info.Key]
	h.runtimeRecoveryMu.Unlock()
	if pending {
		return
	}
	if h.keepAgentAfterTmuxServerLoss(info) {
		h.invalidateWorkspaceEnrichment(info.WorkspaceID)
		return
	}
	h.runtimeRecoveryMu.Lock()
	delete(h.tmuxServerAtLaunch, info.Key)
	h.runtimeRecoveryMu.Unlock()
	h.removeAgentActivityRuntimeSession(info.Key)
	if h.workspaces == nil {
		return
	}
	h.invalidateWorkspaceEnrichment(info.WorkspaceID)
	if h.keepsExitedChat(info) {
		// Its coordinator may restore it from the stored record. Recovery
		// pending also spares the record from the missing-backend prune.
		h.setRuntimeRecoveryPending(info.Key, true)
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

// keepsExitedChat reports whether an exited session is a chat a coordinator
// holds, whose stored record outlives its agent. A taken-over chat is a
// person's chat and is forgotten like any other.
func (h *Handler) keepsExitedChat(info localruntime.SessionInfo) bool {
	if info.Kind != localruntime.LaunchTargetACP || h.runtime == nil {
		return false
	}
	supervised, err := h.runtime.SupervisedACP(info.WorkspaceID, info.Key)
	if err != nil {
		slog.Warn("read exited chat supervision",
			"workspace_id", info.WorkspaceID,
			"session_key", info.Key,
			"err", err)
		return false
	}
	return supervised
}

// keepAgentAfterTmuxServerLoss retains an agent whose tmux server or tmux
// client was killed, as on a host shutdown, so recovery resumes its
// conversation. A tmux client exits 0 whenever its session ends, even when
// tmux exits after its last session. A non-zero exit therefore means the
// client or server was lost, unless tmux reports the session gone while the
// server it ran on still runs, as when a launch fails before its client
// attaches. A replacement server, started by another terminal or by recovery
// before this exit was handled, does not make the session ended. If tmux
// cannot be checked, the agent is kept: a finished agent never reaches this
// check.
//
// The exit handler and post-launch cleanup can both ask about one exit, so
// the first answer for a session generation is cached and reused.
//
// A clean client exit means the session ended. The pane wrapper's record says
// whether the agent finished or was killed with its wrapper, or by HUP, KILL,
// or TERM, as when a shutdown signals every process before tmux and Forge.
func (h *Handler) keepAgentAfterTmuxServerLoss(info localruntime.SessionInfo) bool {
	if h.workspaces == nil || info.Kind != localruntime.LaunchTargetAgent ||
		info.TmuxSession == "" || info.ExitCode == nil {
		return false
	}
	h.exitDecisionMu.Lock()
	defer h.exitDecisionMu.Unlock()
	if decision, ok := h.exitDecisions[info.Key]; ok && decision.createdAt.Equal(info.CreatedAt) {
		return decision.keep
	}
	keep, err := h.classifyAgentExit(info)
	// A clean exit means the session ended and its record is spent. Otherwise
	// the agent may still run, so its record stays; startup prunes the rest.
	if h.runtime != nil && *info.ExitCode == 0 {
		h.runtime.ForgetAgentExit(info.TmuxSession)
	}
	if h.exitDecisions == nil {
		h.exitDecisions = make(map[string]exitDecision)
	}
	h.exitDecisions[info.Key] = exitDecision{createdAt: info.CreatedAt, keep: keep}
	if !keep {
		return false
	}
	h.setRuntimeRecoveryPending(info.Key, true)
	slog.Info("keep workspace agent for recovery after tmux loss",
		"workspace_id", info.WorkspaceID,
		"session_key", info.Key,
		"target_key", info.TargetKey,
		"exit_code", *info.ExitCode,
		"check_err", err)
	return true
}

func (h *Handler) classifyAgentExit(info localruntime.SessionInfo) (bool, error) {
	// The wrapper's record of a killed agent wins whatever the client's exit
	// code: a shutdown can kill the client and the agent together.
	if h.runtime != nil {
		if exit, recorded := h.runtime.AgentExit(info.TmuxSession); recorded && exit.Killed() {
			return true, nil
		}
	}
	if *info.ExitCode == 0 {
		return false, nil
	}
	h.runtimeRecoveryMu.Lock()
	launchServer := h.tmuxServerAtLaunch[info.Key]
	h.runtimeRecoveryMu.Unlock()
	ctx, cancel := context.WithTimeout(h.lifecycleCtx, 2*time.Second)
	defer cancel()
	ended, err := h.workspaces.TmuxSessionEnded(ctx, info.TmuxSession, launchServer)
	return err != nil || !ended, err
}

// noteTmuxServer records the tmux server an agent runtime runs on, read in the
// same tmux call that finds its session, so a replacement server that lacks
// the session is never recorded. Otherwise the server stays unknown, and an
// unknown server keeps the agent on an unclean exit.
func (h *Handler) noteTmuxServer(ctx context.Context, sessionKey, tmuxSession string) {
	if h.workspaces == nil || sessionKey == "" || tmuxSession == "" {
		return
	}
	server, err := h.workspaces.TmuxServerIDWithSession(ctx, tmuxSession)
	if err != nil || server == "" {
		return
	}
	h.runtimeRecoveryMu.Lock()
	defer h.runtimeRecoveryMu.Unlock()
	if h.tmuxServerAtLaunch == nil {
		h.tmuxServerAtLaunch = make(map[string]string)
	}
	h.tmuxServerAtLaunch[sessionKey] = server
}

// restoreBaseTerminalsAfterLoss restores the base terminals a lost tmux server
// took with it, as startup would, before the prune that follows mistakes the
// missing bases for individual exits and errors their workspaces. A newly
// kept agent starts the work for every workspace. Workspaces a pass could not
// finish stay outstanding, so later passes continue from them even after the
// kept agents are stopped, and the prune waits until none remain.
func (h *Handler) restoreBaseTerminalsAfterLoss(ctx context.Context, keptAfterLoss map[string]bool, newlyKept []string) {
	h.runtimeRecoveryMu.Lock()
	remaining := maps.Clone(h.baseRestoreRemaining)
	h.runtimeRecoveryMu.Unlock()
	h.runtimeRecoveryMu.Lock()
	needList := len(newlyKept) > 0 || h.baseRestoreListPending
	h.runtimeRecoveryMu.Unlock()
	if needList {
		workspaces, err := h.db.ListWorkspaces(ctx)
		if err != nil {
			slog.Warn("list workspace terminals for recovery", "err", err)
		}
		// Until the list is read, every workspace is outstanding: the prune
		// waits, and later passes retry even if the kept agents are stopped.
		h.runtimeRecoveryMu.Lock()
		h.baseRestoreListPending = err != nil
		h.runtimeRecoveryMu.Unlock()
		if remaining == nil {
			remaining = make(map[string]bool)
		}
		for _, ws := range workspaces {
			remaining[ws.ID] = true
		}
	}
	if len(remaining) > 0 {
		remaining = h.restoreTerminalsAfterServerLoss(ctx, keptAfterLoss, remaining)
	}
	h.runtimeRecoveryMu.Lock()
	h.baseRestoreRemaining = remaining
	if len(remaining) == 0 {
		h.baseRestoreRemaining = nil
	}
	h.runtimeRecoveryMu.Unlock()
}

// exitDecision caches whether one runtime generation, identified by its key
// and creation time, was kept after its tmux exit.
type exitDecision struct {
	createdAt time.Time
	keep      bool
}

// importReportedConversation copies the conversation from an agent's newest
// activity report into a runtime row recorded before rows saved one. Remove
// it once no running agent can predate migration 000066.
func (h *Handler) importReportedConversation(
	ctx context.Context, session db.WorkspaceRuntimeSession, cwd string,
) db.WorkspaceRuntimeSession {
	if h.agentActivity == nil {
		return session
	}
	reports := h.agentActivity.LiveReportsForWorkspace(cwd, []string{session.SessionKey})
	if len(reports) == 0 {
		return session
	}
	// A failed write still resumes this attempt from the report, which stays
	// with the row for the next one.
	if _, err := h.db.RecordWorkspaceRuntimeConversation(
		ctx, session.SessionKey, reports[0].Agent, reports[0].SessionID,
	); err != nil {
		slog.Warn("import reported agent conversation", "session_key", session.SessionKey, "err", err)
	}
	session.ConversationAgent = reports[0].Agent
	session.ConversationID = reports[0].SessionID
	return session
}

func (h *Handler) removeAgentActivityRuntimeSession(sessionKey string) {
	if h == nil || h.agentActivity == nil || sessionKey == "" {
		return
	}
	if err := h.agentActivity.RemoveRuntimeSession(sessionKey); err != nil {
		slog.Warn("remove agent activity report",
			"session_key", sessionKey,
			"err", err)
	}
}
