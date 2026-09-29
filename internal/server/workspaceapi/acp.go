package workspaceapi

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"sync"

	"github.com/coder/websocket"
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
		if err := agent.Command(command); err != nil {
			// Command failures belong to this caller, not every attached browser.
			data, marshalErr := json.Marshal(acpCommandError{Message: err.Error(), Command: command.Type, ID: command.ID})
			if marshalErr != nil || conn.Write(ctx, websocket.MessageText, data) != nil {
				return false
			}
		}
		return true
	}
	// Prompts and settings can wait on the agent; they run in order on their
	// own goroutine so a stalled one never stops this connection from reading
	// a stop or an answer (Conn allows concurrent writes). The backlog is
	// unbounded: the reader never waits on it.
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
				if conn.Write(ctx, websocket.MessageText, data) != nil {
					return
				}
			case "prompt", "config":
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
			if err != nil || conn.Write(ctx, websocket.MessageText, data) != nil {
				return
			}
		}
	}
}

// Reconnect only this workspace. Failed restoration leaves its stored identity
// available for another attempt instead of silently creating a fresh chat.
func (s *Handler) restoreWorkspaceACP(ctx context.Context, workspaceID, cwd string) {
	s.runtimeRestoreMu.Lock()
	defer s.runtimeRestoreMu.Unlock()
	done, admitted := s.beginWorkspaceSetup(workspaceID)
	if !admitted {
		return
	}
	defer s.finishWorkspaceSetup(workspaceID, done)
	stored, err := s.workspaces.RuntimeSessionsForWorkspace(ctx, workspaceID)
	if err != nil {
		slog.Warn("list ACP workspaces for reconnect", "err", err)
		return
	}
	for _, item := range stored {
		if item.Kind != string(localruntime.LaunchTargetACP) {
			continue
		}
		err := s.runtime.RestoreRuntimeSessions(ctx, []localruntime.RestoredRuntimeSession{{WorkspaceID: workspaceID, SessionKey: item.SessionKey, TargetKey: item.TargetKey, Label: item.Label, Kind: localruntime.LaunchTargetACP, TmuxSession: item.TmuxSession, CWD: cwd, CreatedAt: item.CreatedAt}})
		if err != nil {
			slog.Warn("reconnect ACP workspace", "workspace_id", workspaceID, "session_key", item.SessionKey, "err", err)
			continue
		}
		for _, info := range s.runtime.ListSessions(workspaceID) {
			if info.Key != item.SessionKey || info.TmuxSession == item.TmuxSession {
				continue
			}
			info.DisplayRegion = item.DisplayRegion
			if err := s.recordRuntimeSession(ctx, workspaceID, info, item.Scope); err != nil {
				slog.Warn("record resumed ACP backend", "session_key", item.SessionKey, "err", err)
				return
			}
		}
		s.setRuntimeRecoveryPending(item.SessionKey, false)
	}
}
