package localruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/kit/atomicfile"
)

// ACPAgentSession is a session an ACP agent keeps for a working directory,
// whichever client started it.
type ACPAgentSession struct {
	SessionID string
	Title     string
	// UpdatedAt is zero when the agent does not say.
	UpdatedAt time.Time
}

// ACPWorkspaceChats names the chats a workspace still has, which hold agent
// sessions a new chat must not resume. Keys are its stored chats. Mirrored are
// the sessions its chats follow while the agent runs outside Forge: such a
// chat keeps no saved record of its session until Forge takes over the agent.
type ACPWorkspaceChats struct {
	Keys     []string
	Mirrored []string
}

// ErrACPCannotListSessions reports an agent that offers no session list.
var ErrACPCannotListSessions = errors.New("this agent cannot list its sessions")

// ErrACPSessionInUse reports an agent session a chat of the workspace already
// holds. Two chats would drive the one session over each other.
var ErrACPSessionInUse = errors.New("a chat in this workspace already has this session open")

// acpTarget returns a configured ACP target that can start.
func (m *Manager) acpTarget(targetKey string) (LaunchTarget, error) {
	target, err := m.target(targetKey)
	if err != nil {
		return LaunchTarget{}, err
	}
	if target.Kind != LaunchTargetACP {
		return LaunchTarget{}, fmt.Errorf("target %q is not an ACP agent", targetKey)
	}
	if !target.Available {
		reason := target.DisabledReason
		if reason == "" {
			reason = "target not available"
		}
		return LaunchTarget{}, fmt.Errorf("target %q not available: %s", targetKey, reason)
	}
	return target, nil
}

// ListACPSessions starts a configured ACP target in cwd, without Forge tools,
// and returns the sessions it keeps for cwd, newest first, leaving out any a
// chat of workspaceID holds; see heldACPSessions. It retains no process.
func (m *Manager) ListACPSessions(ctx context.Context, workspaceID, cwd, targetKey string, chats ACPWorkspaceChats) ([]ACPAgentSession, error) {
	target, err := m.acpTarget(targetKey)
	if err != nil {
		return nil, err
	}
	held, err := m.heldACPSessions(workspaceID, chats)
	if err != nil {
		return nil, err
	}
	process, err := startACPProcess(target.Command, cwd, procutil.MergeEnvironment(SessionEnvironment(os.Environ(), m.currentStripEnvVars()), target.Env))
	if err != nil {
		return nil, err
	}
	agent := newACP(process.stdin, process.stdout, &acpStderrTail{out: os.Stderr, done: make(chan struct{})})
	agent.cmd = process.cmd
	go agent.stderr.drain(process.stderr, agent.done)
	agent.open()
	defer func() { _ = agent.Stop(context.WithoutCancel(ctx)) }()

	initCtx, cancel := context.WithTimeout(ctx, acpHandshakeTimeout)
	initialized, err := agent.client.Initialize(initCtx, acpInitializeRequest())
	cancel()
	if err != nil {
		return nil, fmt.Errorf("initialize ACP agent: %w", err)
	}
	if initialized.AgentCapabilities.SessionCapabilities.List == nil || !initialized.AgentCapabilities.LoadSession {
		return nil, ErrACPCannotListSessions
	}
	var sessions []ACPAgentSession
	var cursor *string
	// Agents can answer a page with no sessions and a cursor to the next, so
	// follow cursors to the end. A repeated cursor would never end.
	cursors := make(map[string]bool)
	for {
		pageCtx, cancel := context.WithTimeout(ctx, acpHandshakeTimeout)
		page, err := agent.client.ListSessions(pageCtx, acpsdk.ListSessionsRequest{Cwd: &cwd, Cursor: cursor})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("list ACP sessions: %w", err)
		}
		for _, info := range page.Sessions {
			id := string(info.SessionId)
			if id == "" || held[id] {
				continue
			}
			session := ACPAgentSession{SessionID: id}
			if info.Title != nil {
				session.Title = *info.Title
			}
			if info.UpdatedAt != nil {
				if updated, err := time.Parse(time.RFC3339, *info.UpdatedAt); err == nil {
					session.UpdatedAt = updated.UTC()
				}
			}
			sessions = append(sessions, session)
		}
		if page.NextCursor == nil || *page.NextCursor == "" || cursors[*page.NextCursor] {
			break
		}
		cursors[*page.NextCursor] = true
		cursor = page.NextCursor
	}
	slices.SortStableFunc(sessions, func(a, b ACPAgentSession) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return sessions, nil
}

// ResumeACPSession opens a new chat on a session the agent keeps for cwd, such
// as one another client started, with the agent's history of it as the
// transcript. It never falls back to a new session, and refuses a session a
// chat of the workspace holds; see heldACPSessions.
func (m *Manager) ResumeACPSession(ctx context.Context, workspaceID, cwd, targetKey, sessionID string, chats ACPWorkspaceChats, acpOptions map[string]string) (SessionInfo, error) {
	return m.launch(ctx, workspaceID, cwd, targetKey, nil, "", "", "", acpOptions, &acpImport{sessionID: sessionID, chats: chats})
}

// acpImport is an agent session a new chat resumes, with the chats of its
// workspace that may already hold it.
type acpImport struct {
	sessionID string
	chats     ACPWorkspaceChats
}

// pendingACPImport is a resumed session whose chat is starting: it is neither
// running nor stored yet, but already holds its session.
type pendingACPImport struct {
	workspaceID string
	sessionID   string
}

// importACPLaunchCommand prepares a chat that loads the resumed session. The
// record it seeds is the chat's saved session, so a reload, park, or restart
// after a reboot loads the same session. The check and the pending mark hold
// acpImportMu, so two launches cannot both take one session; the caller ends
// the mark once the chat runs or fails.
func (m *Manager) importACPLaunchCommand(ctx context.Context, target LaunchTarget, workspaceID, key, cwd string, resume acpImport, options map[string]string) (launchCommand, error) {
	m.acpImportMu.Lock()
	defer m.acpImportMu.Unlock()
	held, err := m.heldACPSessions(workspaceID, resume.chats)
	if err != nil {
		return launchCommand{}, err
	}
	if held[resume.sessionID] {
		return launchCommand{}, ErrACPSessionInUse
	}
	sessionID := resume.sessionID
	// Preparing a tmux launch already starts the owner, which reads its saved
	// session at once, so the seed must exist first.
	path := m.acpSessionPath(key)
	if path == "" {
		return launchCommand{}, errors.New("ACP owner state directory is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return launchCommand{}, err
	}
	data, err := json.Marshal(acpSavedSession{SessionID: sessionID, LoadSession: true, Import: true})
	if err != nil {
		return launchCommand{}, err
	}
	if err := atomicfile.WriteFile(path, data, atomicfile.WithPerm(0o600)); err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return launchCommand{}, err
	}
	launch, err := m.acpLaunchCommand(ctx, target, workspaceID, key, cwd, options)
	if err != nil {
		return launchCommand{}, errors.Join(err, os.Remove(path))
	}
	m.mu.Lock()
	m.acpImports[key] = pendingACPImport{workspaceID: workspaceID, sessionID: sessionID}
	m.mu.Unlock()
	return launch, nil
}

// ACPSessionHeld reports whether a chat of workspaceID holds the agent session
// sessionID; see heldACPSessions.
func (m *Manager) ACPSessionHeld(workspaceID, sessionID string, chats ACPWorkspaceChats) (bool, error) {
	held, err := m.heldACPSessions(workspaceID, chats)
	return held[sessionID], err
}

// endACPImport ends a resumed chat's pending mark. By then the chat runs, so
// it holds its session as a live chat, or it failed and holds nothing.
func (m *Manager) endACPImport(key string) {
	m.mu.Lock()
	delete(m.acpImports, key)
	m.mu.Unlock()
}

// heldACPSessions returns the agent sessions the chats of workspaceID hold.
// Only chats the workspace still has count: its stored chats, which include
// parked, dormant, and recovery-pending ones; the sessions its mirrored chats
// follow; its running chats, which may not be stored yet; and resumed chats
// still starting. A chat forgotten after its agent exited holds nothing,
// though its saved record remains.
func (m *Manager) heldACPSessions(workspaceID string, chats ACPWorkspaceChats) (map[string]bool, error) {
	held := make(map[string]bool)
	for _, sessionID := range chats.Mirrored {
		held[sessionID] = true
	}
	keys := slices.Clone(chats.Keys)
	m.mu.Lock()
	for key, s := range m.sessions {
		if info := s.snapshot(); info.WorkspaceID == workspaceID && info.Kind == LaunchTargetACP {
			keys = append(keys, key)
		}
	}
	for _, pending := range m.acpImports {
		if pending.workspaceID == workspaceID {
			held[pending.sessionID] = true
		}
	}
	m.mu.Unlock()
	slices.Sort(keys)
	for _, key := range slices.Compact(keys) {
		saved, err := m.loadACPSaved(key)
		if err != nil {
			return nil, fmt.Errorf("read saved chat %s: %w", key, err)
		}
		if saved != nil && saved.SessionID != "" {
			held[saved.SessionID] = true
		}
	}
	return held, nil
}
