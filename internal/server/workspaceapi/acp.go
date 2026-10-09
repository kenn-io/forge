package workspaceapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/terminalwebsocket"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// acpCommandError reports a failed command to the client that sent it, naming
// the command and its ID so the client settles only that request.
type acpCommandError struct {
	Message string `json:"commandError"`
	Command string `json:"command"`
	ID      string `json:"id"`
}

// acpAccepted acknowledges a command to the client that sent it.
type acpAccepted struct {
	Command string `json:"command"`
	ID      string `json:"id"`
}

func serveACP(w http.ResponseWriter, r *http.Request, agent localruntime.ACPChat) {
	conn, err := terminalwebsocket.Accept(w, r)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "chat detached")
	conn.SetReadLimit(terminalwebsocket.ACPReadLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	changes, unsubscribe := agent.Subscribe()
	defer unsubscribe()
	run := func(command localruntime.ACPCommand) bool {
		err := agent.Command(command)
		var data []byte
		var marshalErr error
		switch {
		case err != nil:
			// Command failures belong to this caller, not every attached browser.
			data, marshalErr = json.Marshal(acpCommandError{Message: err.Error(), Command: command.Type, ID: command.ID})
		case command.Type == "prompt":
			// The sender learns its prompt was taken even when a retry changed
			// nothing, or the message has left the window it can see.
			data, marshalErr = json.Marshal(map[string]acpAccepted{"accepted": {Command: command.Type, ID: command.ID}})
		default:
			return true
		}
		return marshalErr == nil && conn.Write(ctx, websocket.MessageText, data) == nil
	}
	// Prompts, settings, and supervision claims can wait on the agent; they
	// run in order on their own goroutine so a stalled one never stops this
	// connection from reading a stop or an answer (Conn allows concurrent
	// writes). The backlog is unbounded: the reader never waits on it.
	var (
		slowMu  sync.Mutex
		backlog []localruntime.ACPCommand
	)
	slowReady := make(chan struct{}, 1)
	enqueue := func(command localruntime.ACPCommand) {
		slowMu.Lock()
		backlog = append(backlog, command)
		slowMu.Unlock()
		select {
		case slowReady <- struct{}{}:
		default:
		}
	}
	var readers sync.WaitGroup
	readers.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-slowReady:
			}
			for {
				slowMu.Lock()
				if len(backlog) == 0 {
					slowMu.Unlock()
					break
				}
				command := backlog[0]
				backlog = backlog[1:]
				slowMu.Unlock()
				if !run(command) {
					cancel()
					return
				}
			}
		}
	})
	readers.Go(func() {
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var command localruntime.ACPCommand
			if err := json.Unmarshal(data, &command); err != nil {
				return
			}
			switch command.Type {
			case "heartbeat":
				if conn.Write(ctx, websocket.MessageText, []byte(`{"type":"heartbeat"}`)) != nil {
					return
				}
			case "history":
				// Older transcript pages go only to the client that asked.
				data, err := agent.History(command.Before, command.Limit)
				if err != nil {
					data, _ = json.Marshal(acpCommandError{Message: err.Error(), Command: command.Type})
				}
				if conn.Write(ctx, websocket.MessageText, acpImagePreviews(data)) != nil {
					return
				}
			case "prompt", "config", "supervise":
				enqueue(command)
			default:
				if !run(command) {
					return
				}
			}
		}
	})
	defer readers.Wait()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			data, err := agent.Snapshot()
			if err != nil || conn.Write(ctx, websocket.MessageText, acpImagePreviews(data)) != nil {
				return
			}
		}
	}
}

// Reconnect only this workspace. Failed restoration leaves its stored identity
// available for another attempt instead of silently creating a fresh chat.
func (s *Handler) restoreWorkspaceACP(ctx context.Context, workspaceID, cwd string) {
	err := s.openWorkspaceACP(ctx, workspaceID, cwd, nil)
	if err != nil && !errors.Is(err, errWorkspaceACPNotAdmitted) {
		slog.Warn("reconnect ACP workspace", "workspace_id", workspaceID, "err", err)
	}
}

// errWorkspaceACPNotAdmitted reports that setup or deletion owns the workspace.
var errWorkspaceACPNotAdmitted = errors.New("workspace setup or deletion is in progress")

// openWorkspaceACP reconnects the workspace's stored chats, then calls then,
// when it is not nil, with the stored records under the same restore lock and
// setup admission. A chat this daemon saw exit stays down.
func (s *Handler) openWorkspaceACP(ctx context.Context, workspaceID, cwd string, then func([]db.WorkspaceRuntimeSession) error) error {
	s.runtimeRestoreMu.Lock()
	defer s.runtimeRestoreMu.Unlock()
	done, admitted := s.beginWorkspaceSetup(workspaceID)
	if !admitted {
		return errWorkspaceACPNotAdmitted
	}
	defer s.finishWorkspaceSetup(workspaceID, done)
	stored, err := s.workspaces.RuntimeSessionsForWorkspace(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("list ACP sessions: %w", err)
	}
	for _, item := range stored {
		if item.Kind != string(localruntime.LaunchTargetACP) || s.runtime.Exited(item.SessionKey) {
			continue
		}
		err := s.runtime.RestoreRuntimeSessions(ctx, []localruntime.RestoredRuntimeSession{{WorkspaceID: workspaceID, SessionKey: item.SessionKey, TargetKey: item.TargetKey, Label: item.Label, Kind: localruntime.LaunchTargetACP, TmuxSession: item.TmuxSession, CWD: cwd, CreatedAt: item.CreatedAt}})
		if err != nil {
			slog.Warn("reconnect ACP workspace", "workspace_id", workspaceID, "session_key", item.SessionKey, "err", err)
			continue
		}
		if err := s.recordResumedACP(ctx, item); err != nil {
			return fmt.Errorf("record resumed ACP backend %q: %w", item.SessionKey, err)
		}
		s.setRuntimeRecoveryPending(item.SessionKey, false)
	}
	if then == nil {
		return nil
	}
	return then(stored)
}

// recordResumedACP records the backend of a resumed chat when it differs
// from the stored one.
func (s *Handler) recordResumedACP(ctx context.Context, item db.WorkspaceRuntimeSession) error {
	for _, info := range s.runtime.ListSessions(item.WorkspaceID) {
		if info.Key != item.SessionKey || info.TmuxSession == item.TmuxSession {
			continue
		}
		info.DisplayRegion = item.DisplayRegion
		return s.recordRuntimeSession(ctx, item.WorkspaceID, info, item.Scope)
	}
	return nil
}
