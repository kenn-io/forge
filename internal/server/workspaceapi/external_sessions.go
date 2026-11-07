package workspaceapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/agentprocess"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/projects"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/forge/platform"
)

const (
	// externalSessionPassInterval paces adoption and exit checks.
	externalSessionPassInterval = 5 * time.Second
	// externalSessionRecheckInterval bounds how long a quiet agent waits for its
	// pull request to be noticed; a hook event makes it due at once. It keeps a
	// session from costing Git processes on every pass.
	externalSessionRecheckInterval = 30 * time.Second
	// herdrDeliverTimeout bounds one Herdr CLI call.
	herdrDeliverTimeout = 30 * time.Second
)

type externalSessionID struct{ agent, sessionID string }

// externalSessionEntry is what the hook path remembers about a session, so a
// hook event costs a database write only when something changed.
type externalSessionEntry struct {
	saved        db.ExternalAgentSession
	runtimeKey   string
	worktreePath string
	handedOff    bool
	// due marks a hook event since the last adoption check.
	due       bool
	checkedAt time.Time
}

type externalSessionTracker struct {
	mu      sync.Mutex
	entries map[externalSessionID]*externalSessionEntry
}

func (t *externalSessionTracker) entry(id externalSessionID) *externalSessionEntry {
	if t.entries == nil {
		t.entries = make(map[externalSessionID]*externalSessionEntry)
	}
	return t.entries[id]
}

// recordExternalAgentHook handles a hook event from an agent Forge did not
// launch. Hooks are installed for the whole machine, so such an agent reports
// from anywhere it runs; Forge learns its session, worktree, and transcript
// with no step from the person running it.
func (s *Handler) recordExternalAgentHook(ctx context.Context, agent string, input *receiveAgentHookInput) {
	hook := input.Body
	sessionID := strings.TrimSpace(hook.SessionID)
	if s.db == nil || strings.TrimSpace(input.RuntimeSessionKey) != "" || hook.AgentID != "" ||
		sessionID == "" || strings.TrimSpace(hook.CWD) == "" {
		return
	}
	id := externalSessionID{agent, sessionID}
	entry, err := s.externalSessionEntry(ctx, id)
	if err != nil {
		slog.Warn("read external agent session", "agent", agent, "err", err)
		return
	}
	if entry != nil && entry.handedOff {
		return
	}
	var runtimeKey, worktreePath string
	if hook.HookEventName == "SessionEnd" {
		if err := s.db.MarkExternalAgentSessionEnded(ctx, agent, sessionID, time.Now()); err != nil {
			slog.Warn("record external agent session end", "agent", agent, "err", err)
			return
		}
		if entry != nil {
			runtimeKey, worktreePath = entry.runtimeKey, entry.worktreePath
		}
		s.externalSessions.mu.Lock()
		delete(s.externalSessions.entries, id)
		s.externalSessions.mu.Unlock()
	} else {
		runtimeKey, worktreePath, err = s.saveExternalAgentReport(ctx, id, input)
		if err != nil {
			slog.Warn("record external agent session", "agent", agent, "err", err)
			return
		}
	}
	if runtimeKey == "" || s.agentActivity == nil {
		return
	}
	// The chat's agent status comes from these events, as a launched agent's does.
	hook.CWD = worktreePath
	if err := s.agentActivity.HandleEvent(agent, hook, runtimeKey); err != nil {
		slog.Warn("record mirrored agent activity", "session_key", runtimeKey, "err", err)
	}
}

func (s *Handler) saveExternalAgentReport(
	ctx context.Context, id externalSessionID, input *receiveAgentHookInput,
) (runtimeKey, worktreePath string, err error) {
	processID, _ := strconv.Atoi(input.AgentPID)
	report := db.ExternalAgentSession{
		Agent: id.agent, SessionID: id.sessionID, CWD: input.Body.CWD,
		TranscriptPath: input.Body.TranscriptPath,
		ProcessID:      processID, ProcessStart: input.AgentStart,
		HerdrPaneID: input.HerdrPane, HerdrSocketPath: input.HerdrSocket,
	}
	s.externalSessions.mu.Lock()
	defer s.externalSessions.mu.Unlock()
	entry := s.externalSessions.entry(id)
	if entry == nil {
		entry = &externalSessionEntry{}
		s.externalSessions.entries[id] = entry
	}
	entry.due = true
	if report != entry.saved {
		if err := s.db.RecordExternalAgentSession(ctx, report); err != nil {
			return "", "", err
		}
		entry.saved = report
	}
	return entry.runtimeKey, entry.worktreePath, nil
}

// externalSessionEntry returns the remembered entry, loading one saved before
// this daemon started.
func (s *Handler) externalSessionEntry(ctx context.Context, id externalSessionID) (*externalSessionEntry, error) {
	s.externalSessions.mu.Lock()
	entry := s.externalSessions.entry(id)
	s.externalSessions.mu.Unlock()
	if entry != nil {
		return entry, nil
	}
	saved, err := s.db.GetExternalAgentSession(ctx, id.agent, id.sessionID)
	if err != nil || saved == nil {
		return nil, err
	}
	loaded := &externalSessionEntry{runtimeKey: saved.RuntimeSessionKey, handedOff: saved.HandedOffAt != nil}
	if saved.WorkspaceID != "" {
		ws, err := s.db.GetWorkspace(ctx, saved.WorkspaceID)
		if err != nil {
			return nil, fmt.Errorf("get adopting workspace: %w", err)
		}
		if ws != nil {
			loaded.worktreePath = ws.WorktreePath
		} else {
			loaded.runtimeKey = ""
		}
	}
	s.externalSessions.mu.Lock()
	defer s.externalSessions.mu.Unlock()
	if current := s.externalSessions.entry(id); current != nil {
		return current, nil
	}
	s.externalSessions.entries[id] = loaded
	return loaded, nil
}

func (s *Handler) runExternalAgentSessionLoop(ctx context.Context) {
	ticker := time.NewTicker(externalSessionPassInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runExternalAgentSessionPass(ctx)
		}
	}
}

// runExternalAgentSessionPass notices agents that exited without a SessionEnd
// hook and adopts sessions whose worktree now holds an open pull request.
func (s *Handler) runExternalAgentSessionPass(ctx context.Context) {
	sessions, err := s.db.ListRunningExternalAgentSessions(ctx)
	if err != nil {
		slog.Warn("list external agent sessions", "err", err)
		return
	}
	for _, session := range sessions {
		id := externalSessionID{session.Agent, session.SessionID}
		if externalProcessGone(session) {
			s.endExternalSession(ctx, id, session)
			continue
		}
		if session.WorkspaceID != "" || !s.externalSessionDue(id) {
			continue
		}
		if err := s.adoptExternalSession(ctx, session, sessions); err != nil {
			slog.Warn("adopt external agent session", "agent", session.Agent, "err", err)
		}
	}
	if err := s.db.DeleteEndedUnadoptedExternalAgentSessions(ctx); err != nil {
		slog.Warn("forget ended external agent sessions", "err", err)
	}
}

func externalProcessGone(session db.ExternalAgentSession) bool {
	return agentprocess.Identity{PID: session.ProcessID, Start: session.ProcessStart}.Gone()
}

func (s *Handler) endExternalSession(ctx context.Context, id externalSessionID, session db.ExternalAgentSession) {
	if err := s.db.MarkExternalAgentSessionEnded(ctx, id.agent, id.sessionID, time.Now()); err != nil {
		slog.Warn("record external agent exit", "agent", id.agent, "err", err)
		return
	}
	s.externalSessions.mu.Lock()
	delete(s.externalSessions.entries, id)
	s.externalSessions.mu.Unlock()
	if session.RuntimeSessionKey != "" && s.agentActivity != nil {
		if err := s.agentActivity.Remove(id.agent, id.sessionID, session.RuntimeSessionKey); err != nil {
			slog.Warn("clear mirrored agent activity", "session_key", session.RuntimeSessionKey, "err", err)
		}
	}
}

// externalSessionDue reports whether an adoption check is due and, if so,
// starts its recheck interval.
func (s *Handler) externalSessionDue(id externalSessionID) bool {
	s.externalSessions.mu.Lock()
	defer s.externalSessions.mu.Unlock()
	entry := s.externalSessions.entry(id)
	if entry == nil {
		entry = &externalSessionEntry{}
		s.externalSessions.entries[id] = entry
	}
	if !entry.due && time.Since(entry.checkedAt) < externalSessionRecheckInterval {
		return false
	}
	entry.due, entry.checkedAt = false, time.Now()
	return true
}

// adoptExternalSession gives the session a workspace on its own worktree when
// that worktree holds an open pull request's head. A worktree's chat follows
// one agent at a time, so an agent started inside another agent waits its turn.
func (s *Handler) adoptExternalSession(
	ctx context.Context, session db.ExternalAgentSession, running []db.ExternalAgentSession,
) error {
	match, err := s.workspacePRMonitor.FindExternalWorktreePR(ctx, session.CWD, func(remote string) *db.PlatformIdentity {
		return projects.ParseRemoteURLWithKnownPlatforms(remote, s.knownProjectPlatformHosts())
	})
	if err != nil || match == nil {
		return err
	}
	ws, created, err := s.externalWorkspace(ctx, match)
	if err != nil || ws == nil {
		return err
	}
	if !created {
		for _, other := range running {
			if other.WorkspaceID == ws.ID {
				return nil
			}
		}
	}
	key, err := localruntime.NewSessionKey(ws.ID)
	if err != nil {
		return err
	}
	targetKey, label := session.Agent, session.Agent
	if target, ok := s.externalChatTarget(session.Agent); ok {
		targetKey, label = target.Key, target.Label
	}
	if err := s.workspaces.RecordRuntimeSession(ctx, db.WorkspaceRuntimeSession{
		WorkspaceID: ws.ID, SessionKey: key, TargetKey: targetKey, Label: label,
		Kind: string(localruntime.LaunchTargetACP), Scope: "session", CreatedAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("record mirrored chat: %w", err)
	}
	if err := s.db.AdoptExternalAgentSession(ctx, session.Agent, session.SessionID, ws.ID, key); err != nil {
		return err
	}
	s.externalSessions.mu.Lock()
	if entry := s.externalSessions.entry(externalSessionID{session.Agent, session.SessionID}); entry != nil {
		entry.runtimeKey, entry.worktreePath = key, ws.WorktreePath
	}
	s.externalSessions.mu.Unlock()
	if created {
		s.PublishWorkspaceCreated(ws.ID, true)
		s.runWorkspaceSetup(ws)
	} else {
		s.broadcastWorkspaceStatus(ws.ID)
	}
	return nil
}

// externalWorkspace returns the workspace a match belongs in, creating it on the
// external worktree when the pull request has none. It returns nil when the pull
// request already has a workspace elsewhere or Forge owns the checkout.
func (s *Handler) externalWorkspace(ctx context.Context, match *workspace.ExternalWorktreePR) (*workspace.Workspace, bool, error) {
	workspaces, err := s.db.ListWorkspaces(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("list workspaces: %w", err)
	}
	for i := range workspaces {
		if !agentactivity.SameWorkspace(workspaces[i].WorktreePath, match.WorktreePath) {
			continue
		}
		if workspaces[i].ExternalWorktree {
			return &workspaces[i], false, nil
		}
		return nil, false, nil
	}
	provider, err := platform.NormalizeKind(match.Identity.Platform)
	if err != nil {
		return nil, false, fmt.Errorf("external worktree provider: %w", err)
	}
	spec, err := s.resolveWorkspaceLaunchSpec(ctx, providerplane.RepositoryRoute{
		Provider: string(provider), PlatformHost: match.Identity.Host,
		Owner: match.Identity.Owner, Name: match.Identity.Name,
	}, platform.RepositoryKey{}, db.WorkspaceItemTypePullRequest, match.Number, "", false)
	if err != nil {
		return nil, false, fmt.Errorf("resolve launch specification: %w", err)
	}
	ws, err := s.workspaces.CreateExternalFromLaunchSpec(ctx, spec, match.WorktreePath, match.Branch)
	if errors.Is(err, workspace.ErrWorkspaceDuplicate) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("create workspace: %w", err)
	}
	return ws, true, nil
}

// externalChatTarget finds the configured chat agent of an agent family, which
// takes over the session once the external agent exits.
func (s *Handler) externalChatTarget(agent string) (localruntime.LaunchTarget, bool) {
	for _, target := range s.runtime.LaunchTargets() {
		if target.Kind == localruntime.LaunchTargetACP && target.Available &&
			strings.HasPrefix(strings.ToLower(target.Key), agent) {
			return target, true
		}
	}
	return localruntime.LaunchTarget{}, false
}

// mirroredWorkspaceChat serves a chat that follows an agent running outside
// Forge, for as long as ctx lasts. It returns nil when key is not such a chat.
func (s *Handler) mirroredWorkspaceChat(
	ctx context.Context, ws *db.Workspace, item db.WorkspaceRuntimeSession,
) (*localruntime.DormantACP, error) {
	session, err := s.db.GetExternalAgentSessionByRuntimeKey(ctx, item.SessionKey)
	if err != nil {
		return nil, fmt.Errorf("get mirrored session: %w", err)
	}
	if session == nil || session.HandedOffAt != nil {
		return nil, nil
	}
	background := context.WithoutCancel(ctx)
	chat := s.runtime.MirrorACP(localruntime.ACPMirror{
		Key: item.SessionKey, Agent: session.Agent, SessionID: session.SessionID,
		TranscriptPath: session.TranscriptPath,
		Status:         func() localruntime.ACPMirrorStatus { return s.mirrorStatus(background, ws, item, *session) },
		Deliver: func(command localruntime.ACPCommand) error {
			return s.deliverToExternalAgent(background, *session, command)
		},
		Start: func() (localruntime.ACPChat, error) {
			return s.startMirroredChat(background, ws, item, *session)
		},
	})
	s.runBackground(func(bgCtx context.Context) {
		followCtx, cancel := context.WithCancel(bgCtx)
		defer cancel()
		defer context.AfterFunc(ctx, cancel)()
		chat.Follow(followCtx)
	})
	return chat, nil
}

// mirrorStatus reads the agent's state. Anything unknown reads as still running,
// so a second process never starts on the session.
func (s *Handler) mirrorStatus(
	ctx context.Context, ws *db.Workspace, item db.WorkspaceRuntimeSession, session db.ExternalAgentSession,
) localruntime.ACPMirrorStatus {
	current, err := s.db.GetExternalAgentSession(ctx, session.Agent, session.SessionID)
	if err != nil || current == nil {
		return localruntime.ACPMirrorStatus{Running: true}
	}
	if current.EndedAt != nil || externalProcessGone(*current) {
		return localruntime.ACPMirrorStatus{}
	}
	status := localruntime.ACPMirrorStatus{Running: true, Writable: current.HerdrPaneID != ""}
	for _, report := range s.agentActivity.LiveReportsForWorkspace(ws.WorktreePath, []string{item.SessionKey}) {
		switch report.State {
		case agentactivity.StateWorking, agentactivity.StateApproval, agentactivity.StateInput:
			status.Busy = true
		}
	}
	return status
}

// deliverToExternalAgent types a person's prompt or stop into the Herdr pane the
// agent runs in. The agent's transcript then shows it, the same as if it had
// been typed there.
func (s *Handler) deliverToExternalAgent(
	ctx context.Context, session db.ExternalAgentSession, command localruntime.ACPCommand,
) error {
	current, err := s.db.GetExternalAgentSession(ctx, session.Agent, session.SessionID)
	if err != nil {
		return err
	}
	if current == nil || current.HerdrPaneID == "" {
		return localruntime.ErrACPMirrorReadOnly
	}
	args := []string{"agent", "send-keys", current.HerdrPaneID, "esc"}
	if command.Type == "prompt" {
		args = []string{"agent", "prompt", current.HerdrPaneID, command.Text}
	}
	ctx, cancel := context.WithTimeout(ctx, herdrDeliverTimeout)
	defer cancel()
	cmd := procutil.CommandContext(ctx, "herdr", args...)
	cmd.Env = append(localruntime.SessionEnvironment(os.Environ(), nil), "HERDR_SOCKET_PATH="+current.HerdrSocketPath)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("herdr isn't on the Forge daemon's PATH, so Forge can't type into this agent's pane")
	}
	var failure struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(output, &failure) == nil && failure.Error.Message != "" {
		return fmt.Errorf("herdr: %s", failure.Error.Message)
	}
	return fmt.Errorf("herdr: %w: %s", err, strings.TrimSpace(string(output)))
}

// startMirroredChat starts a Forge-run agent on the session once its external
// agent has exited, and records that Forge now holds it.
func (s *Handler) startMirroredChat(
	ctx context.Context, ws *db.Workspace, item db.WorkspaceRuntimeSession, session db.ExternalAgentSession,
) (localruntime.ACPChat, error) {
	target, ok := s.externalChatTarget(session.Agent)
	if !ok {
		return nil, fmt.Errorf("add an ACP agent named %s in Settings to continue this session here", session.Agent)
	}
	if target.Key != item.TargetKey {
		item.TargetKey, item.Label = target.Key, target.Label
		if err := s.workspaces.RecordRuntimeSession(ctx, item); err != nil {
			return nil, fmt.Errorf("record chat agent: %w", err)
		}
	}
	if err := s.runtime.PrepareMirroredACP(ctx, target.Key, ws.ID, item.SessionKey, ws.WorktreePath); err != nil {
		return nil, fmt.Errorf("prepare chat agent: %w", err)
	}
	chat, err := s.startWorkspaceChat(ctx, ws, item.SessionKey)
	if err != nil {
		return nil, err
	}
	if err := s.db.MarkExternalAgentSessionHandedOff(ctx, session.Agent, session.SessionID, time.Now()); err != nil {
		slog.Warn("record mirrored chat handoff", "session_key", item.SessionKey, "err", err)
	}
	s.externalSessions.mu.Lock()
	delete(s.externalSessions.entries, externalSessionID{session.Agent, session.SessionID})
	s.externalSessions.mu.Unlock()
	return chat, nil
}
