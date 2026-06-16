package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

type runtimeTerminalControlMsg struct {
	Type   string `json:"type"`
	Cols   int    `json:"cols,omitempty"`
	Rows   int    `json:"rows,omitempty"`
	Active *bool  `json:"active,omitempty"`
}

func (s *Server) handleWorkspaceRuntimeSessionTerminal(
	w http.ResponseWriter,
	r *http.Request,
) {
	logWebsocketDebug(
		"runtime terminal websocket request",
		"workspace_id", r.PathValue("id"),
		"session_key", r.PathValue("session_key"),
		"path", r.URL.Path,
		"query", r.URL.RawQuery,
	)
	summary, ok := s.runtimeWorkspaceForHTTP(
		w, r, r.PathValue("id"),
	)
	if !ok {
		return
	}

	attachment, err := s.runtime.AttachSessionWithOptions(
		summary.ID, r.PathValue("session_key"),
		localruntime.AttachSessionOptions{
			ResizePriority: runtimeTerminalResizePriority(r),
			ResizeActive:   parseRuntimeTerminalResizeActive(r),
		},
	)
	if err != nil {
		logWebsocketDebug(
			"runtime terminal attach failed",
			"workspace_id", summary.ID,
			"session_key", r.PathValue("session_key"),
			"err", err,
		)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.serveRuntimeTerminal(w, r, attachment)
}

func runtimeTerminalResizePriority(r *http.Request) localruntime.ResizePriority {
	if r.Header.Get("X-Middleman-Fleet-Host") != "" {
		return localruntime.ResizePriorityRemote
	}
	return localruntime.ResizePriorityLocal
}

func parseRuntimeTerminalResizeActive(r *http.Request) bool {
	raw := r.URL.Query().Get("resize_active")
	return raw == "" || raw == "1" || raw == "true"
}

func (s *Server) serveRuntimeTerminal(
	w http.ResponseWriter,
	r *http.Request,
	attachment *localruntime.Attachment,
) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Error("websocket accept", "err", err)
		attachment.Close()
		return
	}
	info := attachment.Info()
	logWebsocketDebug(
		"runtime terminal websocket accepted",
		"workspace_id", info.WorkspaceID,
		"session_key", info.Key,
		"target_key", info.TargetKey,
	)
	if cols, rows, ok := parseRuntimeTerminalSize(r); ok {
		logWebsocketDebug(
			"runtime terminal initial resize",
			"workspace_id", info.WorkspaceID,
			"session_key", info.Key,
			"cols", cols,
			"rows", rows,
		)
		if err := attachment.Resize(cols, rows); err != nil {
			slog.Warn("runtime terminal initial resize", "err", err)
		}
		// pty.Setsize SIGWINCHs the foreground process of the master,
		// but for tmux-backed sessions the pane refit happens via
		// async client-to-server IPC. If the bridge starts forwarding
		// client input before that refit lands, the agent inside the
		// pane sees the pre-resize geometry. Refresh runs
		// `tmux refresh-client` against the attached client, which
		// is a synchronous round-trip to the tmux server and forces
		// it to drain any pending resize messages from the client
		// before returning. For non-tmux sessions, refresh is a
		// no-op. The 2 s budget mirrors the bridge's resize/refresh
		// control handler.
		refreshCtx, refreshCancel := context.WithTimeout(
			r.Context(), 2*time.Second,
		)
		if err := attachment.Refresh(refreshCtx); err != nil {
			slog.Warn(
				"runtime terminal initial refresh", "err", err,
			)
		}
		refreshCancel()
	}

	exited := bridgeRuntimeAttachment(r.Context(), conn, attachment)
	if exited {
		logWebsocketDebug(
			"runtime terminal websocket closing after session exit",
			"workspace_id", info.WorkspaceID,
			"session_key", info.Key,
		)
		conn.Close(websocket.StatusNormalClosure, "session ended")
	} else {
		logWebsocketDebug(
			"runtime terminal websocket closing after detach",
			"workspace_id", info.WorkspaceID,
			"session_key", info.Key,
		)
		conn.Close(websocket.StatusNormalClosure, "detached")
	}
}

func (s *Server) runtimeWorkspaceForHTTP(
	w http.ResponseWriter,
	r *http.Request,
	id string,
) (*db.WorkspaceSummary, bool) {
	if s.workspaces == nil || s.runtime == nil {
		http.Error(
			w, "workspace runtime not configured",
			http.StatusServiceUnavailable,
		)
		return nil, false
	}

	summary, err := s.workspaces.GetSummary(r.Context(), id)
	if err != nil {
		http.Error(w, "get workspace failed", http.StatusInternalServerError)
		return nil, false
	}
	if summary == nil {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return nil, false
	}
	return summary, true
}

func parseRuntimeTerminalSize(
	r *http.Request,
) (cols int, rows int, ok bool) {
	cols, colsOK := parsePositiveQueryInt(r, "cols")
	rows, rowsOK := parsePositiveQueryInt(r, "rows")
	return cols, rows, colsOK && rowsOK
}

func parsePositiveQueryInt(r *http.Request, name string) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func bridgeRuntimeAttachment(
	ctx context.Context,
	conn *websocket.Conn,
	attachment *localruntime.Attachment,
) bool {
	defer attachment.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				logWebsocketDebug("runtime terminal websocket read ended", "err", err)
				return
			}
			switch typ {
			case websocket.MessageBinary:
				if err := attachment.Write(data); err != nil {
					logWebsocketDebug(
						"runtime terminal pty write ended",
						"err", err,
					)
					return
				}
			case websocket.MessageText:
				handleRuntimeTerminalControl(ctx, attachment, data)
			}
		}
	}()

	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		for {
			select {
			case data, ok := <-attachment.Output:
				if !ok {
					return
				}
				if err := conn.Write(
					ctx, websocket.MessageBinary, data,
				); err != nil {
					logWebsocketDebug(
						"runtime terminal websocket write ended",
						"err", err,
					)
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	select {
	case <-attachment.Done:
		// attachment.Done reports process exit, not that every byte read
		// from the PTY has reached the websocket. Give the output goroutine
		// a short chance to observe the closed output channel first so a
		// fast-exiting command can still deliver its final terminal repaint.
		// Keep this bounded: a slow or disconnected browser must not hold the
		// session exit frame forever.
		select {
		case <-outputDone:
		case <-time.After(100 * time.Millisecond):
		}
		// Write the frame BEFORE cancel: coder/websocket tears down
		// the underlying connection when the input goroutine's
		// Read context is canceled, which races our Write.
		writeRuntimeExit(conn, attachment.Info())
		cancel()
		return true
	case <-inputDone:
		cancel()
		return false
	case <-outputDone:
		// outputDone fires when the per-subscriber Output channel
		// closes. There are two distinct reasons that can happen:
		//
		//   1. drainOutput observed PTY EOF and closed every
		//      subscriber via closeSubscribers. The session itself
		//      is over; send the "exited" frame so the client's
		//      onExit fires. attachment.Done follows in a separate
		//      goroutine and can lag noticeably for wrapped sessions
		//      (systemd-run --wait collecting the transient unit),
		//      so we do NOT gate the frame on attachment.Done —
		//      ExitCode may be nil and writeRuntimeExit emits -1,
		//      which the frontend treats identically.
		//
		//   2. broadcast dropped this subscriber because its 64-slot
		//      buffer filled (slow client, congested writer, etc.).
		//      The session is still running, and reporting "exited"
		//      here would auto-close the drawer on a healthy shell.
		//      Close the websocket without an exit frame; the client
		//      can reconnect and resubscribe from the replay buffer.
		//
		// Order matters: write the exit frame BEFORE cancel(). The
		// input goroutine's conn.Read uses ctx, and coder/websocket
		// tears down the underlying TCP connection when that ctx is
		// canceled. Cancelling first races writeRuntimeExit's Write
		// against socket teardown — the Write loses ~25 % of the
		// time and the frame never reaches the client.
		closed := attachment.SessionOutputClosed()
		if closed {
			writeRuntimeExit(conn, attachment.Info())
		}
		cancel()
		return closed
	case <-ctx.Done():
		return false
	}
}

func handleRuntimeTerminalControl(
	ctx context.Context,
	attachment *localruntime.Attachment,
	data []byte,
) {
	var msg runtimeTerminalControlMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		slog.Warn("bad runtime terminal control message", "err", err)
		return
	}
	info := attachment.Info()
	switch msg.Type {
	case "resize_active":
		if msg.Active != nil {
			attachment.SetResizeActive(*msg.Active)
		}
		return
	case "refresh":
		logWebsocketDebug(
			"runtime terminal refresh requested",
			"workspace_id", info.WorkspaceID,
			"session_key", info.Key,
			"cols", msg.Cols,
			"rows", msg.Rows,
		)
		if msg.Cols > 0 && msg.Rows > 0 {
			if err := attachment.Resize(msg.Cols, msg.Rows); err != nil {
				slog.Warn("runtime terminal refresh resize", "err", err)
			}
		}
		refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := attachment.Refresh(refreshCtx); err != nil {
			slog.Warn("runtime terminal refresh", "err", err)
		}
		return
	case "resize":
		logWebsocketDebug(
			"runtime terminal resize requested",
			"workspace_id", info.WorkspaceID,
			"session_key", info.Key,
			"cols", msg.Cols,
			"rows", msg.Rows,
		)
		if err := attachment.Resize(msg.Cols, msg.Rows); err != nil {
			slog.Warn("runtime terminal resize", "err", err)
		}
	}
}

func writeRuntimeExit(
	conn *websocket.Conn,
	info localruntime.SessionInfo,
) {
	exitCode := -1
	if info.ExitCode != nil {
		exitCode = *info.ExitCode
	}
	exitMsg, _ := json.Marshal(map[string]any{
		"type": "exited",
		"code": exitCode,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, exitMsg)
}
