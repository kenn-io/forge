package workspace

import (
	"context"
	"fmt"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/ptyowner"
)

const (
	TerminalBackendTmux     = "tmux"
	TerminalBackendPtyOwner = "pty_owner"
)

type PtyOwnerClient interface {
	HasState(session string) bool
	Ensure(ctx context.Context, session string, cwd string) error
	Attach(
		ctx context.Context,
		session string,
		cols int,
		rows int,
	) (*ptyowner.Attachment, error)
	Stop(ctx context.Context, session string) error
	Snapshot(ctx context.Context, session string) (ptyowner.Status, error)
}

func (m *Manager) SetPtyOwnerClient(client PtyOwnerClient) {
	m.ptyOwner = client
	m.preferPtyOwner = client != nil
}

func (m *Manager) SetPtyOwnerFallbackClient(client PtyOwnerClient) {
	m.ptyOwner = client
}

func (m *Manager) UsesPtyOwner() bool {
	return m.preferPtyOwner
}

func (m *Manager) UsesPtyOwnerForWorkspace(ws *db.Workspace) bool {
	return m.usesPtyOwnerForWorkspace(ws)
}

func (m *Manager) PreferredTerminalBackend() string {
	if m.preferPtyOwner {
		return TerminalBackendPtyOwner
	}
	return TerminalBackendTmux
}

func (m *Manager) EnsureTerminal(
	ctx context.Context,
	ws *db.Workspace,
) error {
	if m.usesPtyOwnerForWorkspace(ws) {
		if m.ptyOwner == nil {
			return fmt.Errorf("pty owner backend unavailable")
		}
		return m.ptyOwner.Ensure(ctx, ws.TmuxSession, ws.WorktreePath)
	}
	return m.EnsureTmux(ctx, ws.TmuxSession, ws.WorktreePath)
}

func (m *Manager) AttachPtyOwnerTerminal(
	ctx context.Context,
	session string,
	cols int,
	rows int,
) (*ptyowner.Attachment, error) {
	if m.ptyOwner == nil {
		return nil, ErrWorkspaceInvalidState
	}
	return m.ptyOwner.Attach(ctx, session, cols, rows)
}

func (m *Manager) newTerminalSession(
	ctx context.Context,
	ws *db.Workspace,
) error {
	if m.usesPtyOwnerForWorkspace(ws) {
		if m.ptyOwner == nil {
			return fmt.Errorf("pty owner backend unavailable")
		}
		return m.ptyOwner.Ensure(ctx, ws.TmuxSession, ws.WorktreePath)
	}
	return m.newTmuxSession(ctx, ws.TmuxSession, ws.WorktreePath)
}

func (m *Manager) usesPtyOwnerForWorkspace(ws *db.Workspace) bool {
	return m.workspaceTerminalBackend(ws) == TerminalBackendPtyOwner
}

func (m *Manager) workspaceTerminalBackend(ws *db.Workspace) string {
	backend := workspaceTerminalBackend(ws, m.preferPtyOwner)
	if backend != "" {
		return backend
	}
	if m.ptyOwner != nil && ws != nil && ws.TmuxSession != "" &&
		m.ptyOwner.HasState(ws.TmuxSession) {
		return TerminalBackendPtyOwner
	}
	return TerminalBackendTmux
}

func workspaceTerminalBackend(ws *db.Workspace, preferPtyOwner bool) string {
	if ws != nil && ws.TerminalBackend != "" {
		return ws.TerminalBackend
	}
	if preferPtyOwner {
		return TerminalBackendPtyOwner
	}
	return ""
}
