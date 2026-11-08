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
	// externalSessionAdoptionAfterExit is how long a session that ended without a
	// workspace stays adoptable, since its pull request may sync after the agent
	// exits.
	externalSessionAdoptionAfterExit = time.Hour
)

type externalSessionID struct{ agent, sessionID string }

// externalSessionCheck paces one session's adoption checks.
type externalSessionCheck struct {
	// due marks a hook event since the last adoption check.
	due       bool
	checkedAt time.Time
	// hook is the latest hook event, which gives the session's new chat the
	// agent's status without waiting for the next one.
	hook *agentactivity.HookEvent
}

type externalSessionTracker struct {
	mu     sync.Mutex
	checks map[externalSessionID]externalSessionCheck
}

func (t *externalSessionTracker) markDue(id externalSessionID, hook agentactivity.HookEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.checks == nil {
		t.checks = make(map[externalSessionID]externalSessionCheck)
	}
	check := t.checks[id]
	check.due = true
	check.hook = &hook
	t.checks[id] = check
}

func (t *externalSessionTracker) lastHook(id externalSessionID) (agentactivity.HookEvent, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	check, ok := t.checks[id]
	if !ok || check.hook == nil {
		return agentactivity.HookEvent{}, false
	}
	return *check.hook, true
}

func (t *externalSessionTracker) forget(id externalSessionID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.checks, id)
}

// startCheck reports whether an adoption check is due and, if so, starts its
// recheck interval.
func (t *externalSessionTracker) startCheck(id externalSessionID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.checks == nil {
		t.checks = make(map[externalSessionID]externalSessionCheck)
	}
	check := t.checks[id]
	if !check.due && time.Since(check.checkedAt) < externalSessionRecheckInterval {
		return false
	}
	t.checks[id] = externalSessionCheck{checkedAt: time.Now(), hook: check.hook}
	return true
}

// mirrorDeliveries remembers the prompts typed into each mirrored agent's pane,
// by chat key and submission ID, so a prompt a reconnecting browser sends again
// is typed once, and never runs again in the agent that continues the session.
// Each chat opens its own mirror, so this outlives them.
type mirrorDeliveries struct {
	mu    sync.Mutex
	chats map[string]*mirrorChatDeliveries
}

type mirrorChatDeliveries struct {
	mu   sync.Mutex // one delivery at a time
	sent map[string]struct{}
}

func (d *mirrorDeliveries) chat(key string) *mirrorChatDeliveries {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.chats == nil {
		d.chats = make(map[string]*mirrorChatDeliveries)
	}
	chat := d.chats[key]
	if chat == nil {
		chat = &mirrorChatDeliveries{sent: make(map[string]struct{})}
		d.chats[key] = chat
	}
	return chat
}

// deliver runs send for a prompt with an ID not delivered yet.
func (d *mirrorDeliveries) deliver(key, id string, send func() error) error {
	if id == "" {
		return send()
	}
	chat := d.chat(key)
	chat.mu.Lock()
	defer chat.mu.Unlock()
	if _, ok := chat.sent[id]; ok {
		return nil
	}
	if err := send(); err != nil {
		return err
	}
	chat.sent[id] = struct{}{}
	return nil
}

// delivered reports whether the prompt id reached the pane, waiting for a
// delivery of it still in flight.
func (d *mirrorDeliveries) delivered(key, id string) bool {
	if id == "" {
		return false
	}
	chat := d.chat(key)
	chat.mu.Lock()
	defer chat.mu.Unlock()
	_, ok := chat.sent[id]
	return ok
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
	saved, err := s.db.GetExternalAgentSession(ctx, agent, sessionID)
	if err != nil {
		slog.Warn("read external agent session", "agent", agent, "err", err)
		return
	}
	if saved != nil && saved.HandedOffAt != nil {
		return
	}
	id := externalSessionID{agent, sessionID}
	if hook.HookEventName == "SessionEnd" {
		if err := s.db.MarkExternalAgentSessionEnded(ctx, agent, sessionID, time.Now()); err != nil {
			slog.Warn("record external agent session end", "agent", agent, "err", err)
			return
		}
		s.externalSessions.forget(id)
	} else {
		processID, _ := strconv.Atoi(input.AgentPID)
		if err := s.db.RecordExternalAgentSession(ctx, db.ExternalAgentSession{
			Agent: agent, SessionID: sessionID, CWD: hook.CWD, TranscriptPath: hook.TranscriptPath,
			ProcessID: processID, ProcessStart: input.AgentStart,
			HerdrPaneID: input.HerdrPane, HerdrSocketPath: input.HerdrSocket,
			// A resumed or forked session carries the prompts it had before.
			Prompted: hook.HookEventName == "UserPromptSubmit" || hook.Source == "resume" || hook.Source == "fork",
		}); err != nil {
			slog.Warn("record external agent session", "agent", agent, "err", err)
			return
		}
		s.externalSessions.markDue(id, hook)
	}
	if saved == nil || saved.RuntimeSessionKey == "" || s.agentActivity == nil {
		return
	}
	ws, err := s.db.GetWorkspace(ctx, saved.WorkspaceID)
	if err != nil {
		slog.Warn("get mirrored workspace", "session_key", saved.RuntimeSessionKey, "err", err)
		return
	}
	if ws == nil {
		return
	}
	// The chat's agent status comes from these events, as a launched agent's does.
	hook.CWD = ws.WorktreePath
	if err := s.agentActivity.HandleEvent(agent, hook, saved.RuntimeSessionKey); err != nil {
		slog.Warn("record mirrored agent activity", "session_key", saved.RuntimeSessionKey, "err", err)
	}
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
// hook and adopts sessions whose worktree now holds an open pull request,
// including sessions that ended recently, whose pull request may sync after.
func (s *Handler) runExternalAgentSessionPass(ctx context.Context) {
	sessions, err := s.db.ListRunningExternalAgentSessions(ctx)
	if err != nil {
		slog.Warn("list external agent sessions", "err", err)
		return
	}
	adoptableSince := time.Now().Add(-externalSessionAdoptionAfterExit)
	ended, err := s.db.ListEndedUnadoptedExternalAgentSessions(ctx, adoptableSince)
	if err != nil {
		slog.Warn("list ended external agent sessions", "err", err)
		return
	}
	// Running sessions come first, so a worktree's running agent gets its chat.
	sessions = append(sessions, ended...)
	for i := range sessions {
		session := &sessions[i]
		id := externalSessionID{session.Agent, session.SessionID}
		if session.EndedAt == nil && externalProcessGone(*session) {
			s.endExternalSession(ctx, id, *session)
			continue
		}
		if session.WorkspaceID != "" || !s.externalSessions.startCheck(id) {
			continue
		}
		if err := s.adoptExternalSession(ctx, session, sessions); err != nil {
			slog.Warn("adopt external agent session", "agent", session.Agent, "err", err)
		}
	}
	if err := s.db.DeleteEndedUnadoptedExternalAgentSessions(ctx, adoptableSince); err != nil {
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
	s.externalSessions.forget(id)
	if session.RuntimeSessionKey != "" && s.agentActivity != nil {
		if err := s.agentActivity.Remove(id.agent, id.sessionID, session.RuntimeSessionKey); err != nil {
			slog.Warn("clear mirrored agent activity", "session_key", session.RuntimeSessionKey, "err", err)
		}
	}
}

// adoptExternalSession gives the session a workspace on its own worktree when
// that worktree holds an open pull request's head. A worktree's chat follows
// one agent at a time, so an agent started inside another agent waits its turn.
func (s *Handler) adoptExternalSession(
	ctx context.Context, session *db.ExternalAgentSession, running []db.ExternalAgentSession,
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
	// Later sessions in this pass read running, so they see this adoption.
	session.WorkspaceID = ws.ID
	if hook, ok := s.externalSessions.lastHook(externalSessionID{session.Agent, session.SessionID}); ok && s.agentActivity != nil {
		// Hooks before adoption had no chat to report to; the latest one says
		// whether the agent is mid-turn now.
		hook.CWD = ws.WorktreePath
		if err := s.agentActivity.HandleEvent(session.Agent, hook, key); err != nil {
			slog.Warn("record mirrored agent activity", "session_key", key, "err", err)
		}
	}
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
		// A checkout that moved to another pull request's branch keeps its
		// first workspace, which the new conversation does not belong in.
		if workspaces[i].ExternalWorktree && externalWorkspaceHolds(&workspaces[i], match) {
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

func externalWorkspaceHolds(ws *db.Workspace, match *workspace.ExternalWorktreePR) bool {
	return ws.ItemType == db.WorkspaceItemTypePullRequest && ws.ItemNumber == match.Number &&
		strings.EqualFold(ws.Platform, match.Identity.Platform) && strings.EqualFold(ws.PlatformHost, match.Identity.Host) &&
		strings.EqualFold(ws.RepoOwner, match.Identity.Owner) && strings.EqualFold(ws.RepoName, match.Identity.Name)
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
			id := ""
			if command.Type == "prompt" {
				id = command.ID
			}
			return s.mirrorDeliveries.deliver(item.SessionKey, id, func() error {
				return s.deliverToExternalAgent(background, *session, command)
			})
		},
		Start: func() (localruntime.ACPChat, error) {
			return s.startMirroredChat(background, ws, item, *session)
		},
		Delivered: func(id string) bool { return s.mirrorDeliveries.delivered(item.SessionKey, id) },
		Live: func() localruntime.ACPChat {
			live, err := s.runtime.ACP(ws.ID, item.SessionKey)
			if err != nil {
				return nil
			}
			return live
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
		return localruntime.ACPMirrorStatus{TranscriptPath: current.TranscriptPath, Prompted: current.Prompted}
	}
	status := localruntime.ACPMirrorStatus{
		Running: true, Writable: current.HerdrPaneID != "", TranscriptPath: current.TranscriptPath, Prompted: current.Prompted,
	}
	for _, report := range s.agentActivity.LiveReportsForWorkspace(ws.WorktreePath, []string{item.SessionKey}) {
		switch report.State {
		case agentactivity.StateWorking, agentactivity.StateApproval, agentactivity.StateInput:
			status.Busy = true
		case agentactivity.StateIdle, agentactivity.StateDone:
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
		return nil, fmt.Errorf("To continue this session in Forge, add a chat agent in Settings whose key starts with %q, such as %s-chat.", session.Agent, session.Agent) //nolint:staticcheck // ST1005: the chat shows this text verbatim as a full sentence.
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
	s.externalSessions.forget(externalSessionID{session.Agent, session.SessionID})
	return chat, nil
}
