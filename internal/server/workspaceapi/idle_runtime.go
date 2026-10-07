package workspaceapi

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

const (
	idleRuntimeCheckInterval = 5 * time.Minute
	idleRuntimeStopTimeout   = 30 * time.Second
)

// idleRuntimes owns idle stop: a runtime stops only after the configured hours
// with no page view and no typing in its workspace. Every stop, resume and
// stop mark goes through it; callers only report a page view or typing, or ask
// whether a stored runtime is stopped. A page view is the showing workspace
// page's viewing runtime read. Typing is input on any terminal socket or an
// ACP prompt or reply. Socket opens, reconnects, focus and other reads are
// neither, since the browser keeps hidden panes connected. Only a page view
// resumes stopped runtimes, so a hidden pane reconnecting can't undo a stop.
// It exists only while the setting is on (Handler.idle is nil otherwise, and
// every method is a no-op on nil), so with the setting off nothing is
// recorded, saved or read. Each pass and shutdown save the last use per
// workspace, so a restart keeps idle time; turning the setting on starts every
// workspace's clock at the pass that follows.
type idleRuntimes struct {
	h *Handler

	mu     sync.Mutex
	active map[string]time.Time // last page view or typing per workspace, in UTC
	dirty  bool                 // active changed since it was saved
}

func newIdleRuntimes(h *Handler) *idleRuntimes {
	i := &idleRuntimes{h: h, active: make(map[string]time.Time)}
	saved, err := h.runtime.IdleActivity()
	if err != nil {
		slog.Warn("load idle runtime activity", "err", err)
	}
	maps.Copy(i.active, saved)
	return i
}

// syncIdle turns idle stop on or off to match the setting. Turning it on
// starts the clocks; turning it off hands what it stopped to normal recovery.
// Only startup and the idle loop call it, one after the other.
func (h *Handler) syncIdle(ctx context.Context) *idleRuntimes {
	on := h.configSnapshot().IdleRuntimeStopAfter > 0 && h.runtime != nil
	current := h.idle.Load()
	switch {
	case on && current == nil:
		current = newIdleRuntimes(h)
		h.idle.Store(current)
	case !on && current != nil:
		h.idle.Store(nil)
		h.releaseIdle(ctx)
		current = nil
	}
	return current
}

// releaseIdle hands runtimes idle stop stopped to normal recovery, as after a
// reboot: agents and ACP chats retry, a shell reads as an error. It then
// removes every stop mark and the saved clock.
func (h *Handler) releaseIdle(ctx context.Context) {
	if h.runtime == nil {
		return
	}
	if h.db != nil {
		stored, err := h.db.ListAllWorkspaceRuntimeSessions(ctx)
		if err != nil {
			slog.Warn("list runtimes to release from idle stop", "err", err)
		}
		for _, row := range stored {
			kind := localruntime.LaunchTargetKind(row.Kind)
			if (kind == localruntime.LaunchTargetAgent || kind == localruntime.LaunchTargetACP) && h.runtime.StoppedMark(row.SessionKey) {
				h.setRuntimeRecoveryPending(row.SessionKey, true)
			}
		}
	}
	if err := h.runtime.ClearIdleState(); err != nil {
		slog.Warn("remove idle stop state", "err", err)
	}
}

// runIdle switches idle stop on a config change and runs its pass every
// interval. Shutdown cancels it after the HTTP drain, so its last save
// follows all input.
func (h *Handler) runIdle(ctx context.Context) {
	ticker := time.NewTicker(idleRuntimeCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
		case <-ticker.C:
		case <-h.idleWake:
		}
		i := h.syncIdle(ctx)
		if i != nil && ctx.Err() == nil {
			i.stopIdle(ctx, h.configSnapshot().IdleRuntimeStopAfter)
		}
		i.save()
		if ctx.Err() != nil {
			return
		}
	}
}

// Viewed records a page view. The caller's recovery pass then resumes stopped
// runtimes through Resume, the only path that does.
func (i *idleRuntimes) Viewed(ws *db.Workspace) {
	if i == nil {
		return
	}
	i.used(ws.ID)
}

// Resume restarts a runtime idle stop stopped. A failed or cancelled resume
// clears the stop mark and hands agents and ACP chats to recovery, which
// retries them like any whose owner is gone; a shell reads as an error.
// The resume outlives the request, so a page reload can't fail it. Callers
// hold runtimeRestoreMu and the workspace's setup admission.
func (i *idleRuntimes) Resume(ctx context.Context, ws *db.Workspace, row db.WorkspaceRuntimeSession) error {
	resumeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), idleRuntimeStopTimeout)
	defer cancel()
	if err := i.resume(resumeCtx, ws, row); err != nil {
		if kind := localruntime.LaunchTargetKind(row.Kind); kind == localruntime.LaunchTargetAgent || kind == localruntime.LaunchTargetACP {
			i.h.setRuntimeRecoveryPending(row.SessionKey, true)
		}
		return errors.Join(err, i.h.runtime.Resumed(row.SessionKey))
	}
	i.h.invalidateWorkspaceEnrichment(ws.ID)
	return nil
}

// Typed records input in a workspace.
func (i *idleRuntimes) Typed(workspaceID string) {
	if i == nil {
		return
	}
	i.used(workspaceID)
}

// Stopped reports whether idle stop stopped a stored runtime that has not run
// since. It reads the stop mark before asking the owner, so rows idle stop
// never touched cost nothing.
func (i *idleRuntimes) Stopped(ctx context.Context, row db.WorkspaceRuntimeSession) bool {
	return i != nil && i.h.runtime.Parked(ctx, row.WorkspaceID, row.SessionKey)
}

// Retain drops stop marks and saved use of runtimes and workspaces that are gone.
func (i *idleRuntimes) Retain(stored []db.WorkspaceRuntimeSession, workspaces []db.Workspace) {
	if i == nil {
		return
	}
	keep := make(map[string]struct{}, len(stored))
	for _, row := range stored {
		keep[row.SessionKey] = struct{}{}
	}
	if err := i.h.runtime.RetainParked(keep); err != nil {
		slog.Warn("drop stop marks of forgotten runtimes", "err", err)
	}
	ids := make(map[string]bool, len(workspaces))
	for idx := range workspaces {
		ids[workspaces[idx].ID] = true
	}
	i.mu.Lock()
	n := len(i.active)
	maps.DeleteFunc(i.active, func(id string, _ time.Time) bool { return !ids[id] })
	i.dirty = i.dirty || len(i.active) != n
	i.mu.Unlock()
	i.save()
}

// Forget drops a deleted workspace's clock.
func (i *idleRuntimes) Forget(workspaceID string) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.active, workspaceID)
	i.dirty = true
}

func (i *idleRuntimes) used(workspaceID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.active[workspaceID] = i.h.now().UTC()
	i.dirty = true
}

// seen starts counting a workspace's idle time from now unless it has a last
// use, so restarts before its first use can't reset it.
func (i *idleRuntimes) seen(workspaceID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.active[workspaceID]; !ok {
		i.active[workspaceID] = i.h.now().UTC()
		i.dirty = true
	}
}

// save writes the last use per workspace when it changed.
func (i *idleRuntimes) save() {
	if i == nil {
		return
	}
	i.mu.Lock()
	if !i.dirty {
		i.mu.Unlock()
		return
	}
	active := maps.Clone(i.active)
	i.dirty = false
	i.mu.Unlock()
	if err := i.h.runtime.SaveIdleActivity(active); err != nil {
		slog.Warn("save idle runtime activity", "err", err)
		i.mu.Lock()
		i.dirty = true
		i.mu.Unlock()
	}
}

// idle reports how long a workspace, or one of its runtimes created at
// created, has gone without use.
func (i *idleRuntimes) idle(ws *db.Workspace, created time.Time) time.Duration {
	i.mu.Lock()
	last := i.active[ws.ID]
	i.mu.Unlock()
	for _, t := range []time.Time{ws.CreatedAt, created} {
		if t.After(last) {
			last = t
		}
	}
	return i.h.now().Sub(last)
}

// stopIdle stops what has gone unused for after in each ready pty-owner
// workspace.
func (i *idleRuntimes) stopIdle(ctx context.Context, after time.Duration) {
	h := i.h
	// A reload that turns the setting off mid-pass yields a zero timeout.
	if after <= 0 || h.db == nil || h.runtime == nil || h.workspaces == nil {
		return
	}
	workspaces, err := h.db.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("list workspaces for idle runtime stop", "err", err)
		return
	}
	for idx := range workspaces {
		ws := &workspaces[idx]
		if ctx.Err() != nil {
			return
		}
		i.seen(ws.ID)
		if ws.Status != "ready" || !h.workspaces.UsesPtyOwnerForWorkspace(ws) || i.idle(ws, time.Time{}) < after {
			continue
		}
		if err := i.stopWorkspace(ctx, ws, after); err != nil {
			slog.Warn("stop idle workspace runtimes", "workspace_id", ws.ID, "err", err)
		}
	}
}

// stopWorkspace stops eligible runtimes and the unattached base terminal under setup admission.
// Setup, deletion, recovery and resume skip when admission is held; a viewing read resumes on its next poll.
func (i *idleRuntimes) stopWorkspace(ctx context.Context, ws *db.Workspace, after time.Duration) error {
	h := i.h
	done, admitted := h.beginWorkspaceSetup(ws.ID)
	if !admitted {
		return nil
	}
	defer h.finishWorkspaceSetup(ws.ID, done)
	stored, err := h.workspaces.RuntimeSessionsForWorkspace(ctx, ws.ID)
	if err != nil {
		return err
	}
	var errs []error
	stopped := 0
	for _, row := range stored {
		if ctx.Err() != nil {
			continue
		}
		// Checked after stoppable's reads, so a view or typing during them keeps the runtime.
		if !i.stoppable(ctx, ws, row) || i.idle(ws, row.CreatedAt) < after {
			continue
		}
		ok, err := i.stop(ctx, ws, row)
		errs = append(errs, err)
		if ok {
			stopped++
		}
	}
	if ctx.Err() == nil && h.terminal != nil && i.idle(ws, time.Time{}) >= after {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), idleRuntimeStopTimeout)
		errs = append(errs, h.terminal.StopUnattachedTerminal(stopCtx, ws))
		cancel()
	}
	if stopped > 0 {
		h.invalidateWorkspaceEnrichment(ws.ID)
		slog.Info("stopped idle workspace runtimes", "workspace_id", ws.ID, "runtimes", stopped)
	}
	return errors.Join(errs...)
}

// stoppable reports whether a running runtime may stop. A plain shell may; it
// comes back fresh. An agent may once no conversation in it is working and it
// can resume; a pending question or approval is lost. A hook agent that is
// idle has nothing to resume unless its session continued an earlier
// conversation (Report.Continued). Tmux, command and other sessions never
// stop.
func (i *idleRuntimes) stoppable(ctx context.Context, ws *db.Workspace, row db.WorkspaceRuntimeSession) bool {
	h := i.h
	if row.TmuxSession != "" || h.runtime.PtyOwnerGone(ctx, ws.ID, row.SessionKey) {
		return false
	}
	kind := localruntime.LaunchTargetKind(row.Kind)
	if kind == localruntime.LaunchTargetPlainShell {
		return true
	}
	if kind != localruntime.LaunchTargetAgent && kind != localruntime.LaunchTargetACP {
		return false
	}
	reports := h.agentActivity.LiveReportsForWorkspace(ws.WorktreePath, []string{row.SessionKey})
	reports = slices.DeleteFunc(reports, func(report agentactivity.Report) bool {
		_, ok := reportedAgent(report, localruntime.SessionInfo{Kind: kind})
		return !ok
	})
	if len(reports) == 0 || slices.ContainsFunc(reports, func(report agentactivity.Report) bool { return report.State == agentactivity.StateWorking }) {
		return false
	}
	if kind == localruntime.LaunchTargetACP {
		return h.runtime.ACPReloadable(row.SessionKey)
	}
	latest := reports[0]
	if latest.State == agentactivity.StateIdle && !latest.Continued {
		return false
	}
	return h.runtime.AgentResumable(row.TargetKey, latest.Agent, latest.SessionID)
}

// stop stops one runtime, marking it first. An owner that survives a failed
// stop is unmarked and reattached under a fresh deadline; one that is gone
// stays stopped. It reports whether the runtime stopped.
func (i *idleRuntimes) stop(ctx context.Context, ws *db.Workspace, row db.WorkspaceRuntimeSession) (bool, error) {
	h := i.h
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), idleRuntimeStopTimeout)
	err := h.runtime.Park(stopCtx, ws.ID, row.SessionKey)
	cancel()
	repairCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), idleRuntimeStopTimeout)
	defer cancel()
	if err == nil || h.runtime.PtyOwnerGone(repairCtx, ws.ID, row.SessionKey) {
		// Startup marks agents pending; a stopped one waits for a view instead.
		h.setRuntimeRecoveryPending(row.SessionKey, false)
		return true, err
	}
	return false, errors.Join(err, h.runtime.Resumed(row.SessionKey),
		h.runtime.RestoreRuntimeSessions(repairCtx, []localruntime.RestoredRuntimeSession{restoredRuntime(row, ws.WorktreePath)}))
}

// resume restarts a stopped runtime under its key: an agent in its saved
// conversation, an ACP chat from its saved session, a shell fresh, and clears
// the stop mark.
func (i *idleRuntimes) resume(ctx context.Context, ws *db.Workspace, row db.WorkspaceRuntimeSession) error {
	h := i.h
	restored := restoredRuntime(row, ws.WorktreePath)
	switch restored.Kind {
	case localruntime.LaunchTargetAgent:
		if err := h.resumeWorkspaceAgent(ctx, row, restored); err != nil {
			return err
		}
	case localruntime.LaunchTargetPlainShell:
		session, err := h.runtime.RestartShell(ctx, restored)
		if err != nil {
			return err
		}
		if err := h.recordRestartedRuntime(ctx, row, session); err != nil {
			return err
		}
		h.forgetRecordedRuntimeSessionIfExited(ctx, session)
	case localruntime.LaunchTargetACP:
		if err := h.restoreACPRow(ctx, ws.ID, ws.WorktreePath, row); err != nil {
			return err
		}
	default:
		return errors.New("cannot resume a stopped " + row.Kind + " runtime")
	}
	return h.runtime.Resumed(row.SessionKey)
}
