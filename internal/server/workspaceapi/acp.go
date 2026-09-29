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

func serveACP(w http.ResponseWriter, r *http.Request, agent localruntime.ACPChat) {
	conn, err := terminalwebsocket.Accept(w, r)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "chat detached")
	conn.SetReadLimit(terminalwebsocket.ACPCommandReadLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	changes, unsubscribe := agent.Subscribe()
	defer unsubscribe()
	run := func(command localruntime.ACPCommand) bool {
		if err := agent.Command(command); err != nil {
			// Command failures belong to this caller, not every attached browser.
			data, marshalErr := json.Marshal(map[string]string{"commandError": err.Error()})
			if marshalErr != nil || conn.Write(ctx, websocket.MessageText, data) != nil {
				return false
			}
		}
		return true
	}
	// Prompts and settings can wait on the agent; they run in order on their
	// own goroutine so a stalled one never stops this connection from reading
	// a stop or an answer (Conn allows concurrent writes).
	slow := make(chan localruntime.ACPCommand, 32)
	var readers sync.WaitGroup
	readers.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case command := <-slow:
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
			case "prompt", "config":
				select {
				case slow <- command:
				case <-ctx.Done():
					return
				}
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
