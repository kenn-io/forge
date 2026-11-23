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
// chat of workspaceID holds. It retains no process.
func (m *Manager) ListACPSessions(ctx context.Context, workspaceID, cwd, targetKey string) ([]ACPAgentSession, error) {
	target, err := m.acpTarget(targetKey)
	if err != nil {
		return nil, err
	}
	held, err := m.heldACPSessions(workspaceID)
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
// transcript. It never falls back to a new session.
func (m *Manager) ResumeACPSession(ctx context.Context, workspaceID, cwd, targetKey, sessionID string, acpOptions map[string]string) (SessionInfo, error) {
	return m.launch(ctx, workspaceID, cwd, targetKey, nil, "", sessionID, "", acpOptions)
}

// importACPLaunchCommand prepares a chat that loads sessionID. The record it
// seeds is the chat's saved session, so a reload, park, or restart after a
// reboot loads the same session. The check and the seed hold acpImportMu, so
// two launches cannot both take one session.
func (m *Manager) importACPLaunchCommand(ctx context.Context, target LaunchTarget, workspaceID, key, cwd, sessionID string, options map[string]string) (launchCommand, error) {
	m.acpImportMu.Lock()
	defer m.acpImportMu.Unlock()
	held, err := m.heldACPSessions(workspaceID)
	if err != nil {
		return launchCommand{}, err
	}
	if held[sessionID] {
		return launchCommand{}, ErrACPSessionInUse
	}
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
	return launch, nil
}

// heldACPSessions returns the agent sessions the chats of workspaceID hold,
// running or not.
func (m *Manager) heldACPSessions(workspaceID string) (map[string]bool, error) {
	held := make(map[string]bool)
	if m.acpSessionsDir == "" {
		return held, nil
	}
	entries, err := os.ReadDir(m.acpSessionsDir)
	if errors.Is(err, os.ErrNotExist) {
		return held, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(m.acpSessionsDir, entry.Name())
		data, err := os.ReadFile(filepath.Join(dir, "config.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var cfg acpOwnerConfig
		if err := json.Unmarshal(data, &cfg); err != nil || cfg.Info.WorkspaceID != workspaceID {
			continue
		}
		saved, err := readACPSaved(filepath.Join(dir, "session.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read saved chat %s: %w", entry.Name(), err)
		}
		if saved.SessionID != "" {
			held[saved.SessionID] = true
		}
	}
	return held, nil
}
