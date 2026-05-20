package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty/v2"

	"github.com/wesm/middleman/internal/procutil"
	"github.com/wesm/middleman/internal/ptyowner"
	"github.com/wesm/middleman/internal/workspace"
)

// Handler serves WebSocket connections that bridge a
// PTY-attached tmux session to the browser.
type Handler struct {
	Workspaces  *workspace.Manager
	TmuxCommand []string

	mu     sync.Mutex
	active map[string]int
}

func (h *Handler) ServeHTTP(
	w http.ResponseWriter, r *http.Request,
) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing workspace id", http.StatusBadRequest)
		return
	}
	logWebsocketDebug(
		"workspace terminal websocket request",
		"workspace_id", id,
		"path", r.URL.Path,
		"query", r.URL.RawQuery,
		"remote_addr", r.RemoteAddr,
		"user_agent", r.UserAgent(),
	)

	ws, err := h.Workspaces.Get(r.Context(), id)
	if err != nil {
		slog.Error("get workspace", "id", id, "err", err)
		http.Error(
			w, "internal error",
			http.StatusInternalServerError,
		)
		return
	}
	if ws == nil {
		logWebsocketDebug(
			"workspace terminal rejected: workspace not found",
			"workspace_id", id,
		)
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	if ws.Status != "ready" {
		logWebsocketDebug(
			"workspace terminal rejected: workspace not ready",
			"workspace_id", id,
			"status", ws.Status,
		)
		http.Error(
			w,
			fmt.Sprintf("workspace not ready (status: %s)", ws.Status),
			http.StatusConflict,
		)
		return
	}

	cols, rows := parseSize(r)
	logWebsocketDebug(
		"workspace terminal attaching",
		"workspace_id", id,
		"tmux_session", ws.TmuxSession,
		"cols", cols,
		"rows", rows,
	)

	releaseTerminal, err := h.claimTerminalSlot(id)
	if err != nil {
		logWebsocketDebug(
			"workspace terminal rejected: slot unavailable",
			"workspace_id", id,
			"err", err,
		)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer releaseTerminal()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Error("websocket accept", "err", err)
		return
	}
	logWebsocketDebug("workspace terminal websocket accepted", "workspace_id", id)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	if h.Workspaces.UsesPtyOwnerForWorkspace(ws) {
		if err := h.Workspaces.EnsureTerminal(ctx, ws); err != nil {
			slog.Error("ensure pty owner", "err", err)
			conn.Close(websocket.StatusInternalError, "failed to start pty owner")
			return
		}
		attachment, err := h.Workspaces.AttachPtyOwnerTerminal(
			ctx, ws.TmuxSession, cols, rows,
		)
		if err != nil {
			slog.Error("attach pty owner", "err", err)
			conn.Close(websocket.StatusInternalError, "failed to attach pty owner")
			return
		}
		exited := bridgePtyOwnerAttachment(ctx, conn, attachment)
		if exited {
			conn.Close(websocket.StatusNormalClosure, "session ended")
		} else {
			conn.Close(websocket.StatusNormalClosure, "detached")
		}
		return
	}

	if tmuxErr := h.Workspaces.EnsureTmux(
		ctx, ws.TmuxSession, ws.WorktreePath,
	); tmuxErr != nil {
		slog.Error("ensure tmux", "err", tmuxErr)
		reason := "failed to start tmux"
		if procutil.IsResourceExhausted(tmuxErr) {
			reason = "host process limit reached"
		}
		conn.Close(
			websocket.StatusInternalError,
			reason,
		)
		return
	}
	logWebsocketDebug(
		"workspace terminal tmux ready",
		"workspace_id", id,
		"tmux_session", ws.TmuxSession,
	)

	prefix := h.TmuxCommand
	if len(prefix) == 0 {
		prefix = []string{"tmux"}
	}
	argv := make([]string, 0, len(prefix)+3)
	argv = append(argv, prefix...)
	argv = append(argv, "attach-session", "-t", ws.TmuxSession)
	cmd := procutil.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	logWebsocketDebug(
		"workspace terminal starting tmux attach",
		"workspace_id", id,
		"program", argv[0],
		"argc", len(argv),
		"tmux_session", ws.TmuxSession,
	)

	releaseProc, err := procutil.TryAcquire(
		ctx, "terminal attach subprocess capacity",
	)
	if err != nil {
		slog.Error("terminal attach capacity", "err", err)
		conn.Close(
			websocket.StatusInternalError,
			"host process limit reached",
		)
		return
	}
	defer releaseProc()

	winSize := &pty.Winsize{
		Rows: uint16(rows),
		Cols: uint16(cols),
	}
	ptmx, err := pty.StartWithSize(cmd, winSize)
	if err != nil {
		slog.Error("pty start", "err", err)
		reason := "failed to start pty"
		if procutil.IsResourceExhausted(err) {
			reason = "host process limit reached"
		}
		conn.Close(
			websocket.StatusInternalError,
			reason,
		)
		return
	}
	defer ptmx.Close()
	logWebsocketDebug(
		"workspace terminal pty started",
		"workspace_id", id,
		"pid", cmd.Process.Pid,
	)

	var wg sync.WaitGroup
	wg.Add(2)

	// Cancel context when either bridge goroutine exits.
	// This unblocks cmd.Wait below if the browser disconnects.
	bridgeDone := make(chan struct{})

	// PTY -> WebSocket
	go func() {
		defer wg.Done()
		defer func() {
			select {
			case bridgeDone <- struct{}{}:
			default:
			}
		}()
		ptyToWS(ctx, ptmx, conn)
	}()

	// WebSocket -> PTY
	go func() {
		defer wg.Done()
		defer func() {
			select {
			case bridgeDone <- struct{}{}:
			default:
			}
		}()
		wsToPTY(ctx, ptmx, conn)
	}()

	// Wait for tmux exit OR bridge disconnect (whichever first).
	cmdDone := make(chan int, 1)
	go func() {
		cmdDone <- processExitCode(cmd.Wait())
	}()

	var exitCode int
	select {
	case exitCode = <-cmdDone:
		// tmux exited normally.
		logWebsocketDebug(
			"workspace terminal tmux attach exited",
			"workspace_id", id,
			"exit_code", exitCode,
		)
	case <-bridgeDone:
		// Browser disconnected. Cancel context to kill tmux attach.
		logWebsocketDebug(
			"workspace terminal bridge disconnected",
			"workspace_id", id,
		)
		cancel()
		_ = ptmx.Close()
		// Wait briefly for cmd to finish.
		select {
		case exitCode = <-cmdDone:
		case <-time.After(2 * time.Second):
			exitCode = -1
		}
	}

	exitMsg, _ := json.Marshal(map[string]any{
		"type": "exited",
		"code": exitCode,
	})
	// Use a short-lived context for the final write — the bridge
	// context may already be cancelled from a disconnect path.
	writeCtx, writeCancel := context.WithTimeout(
		context.Background(), 2*time.Second)
	_ = conn.Write(writeCtx, websocket.MessageText, exitMsg)
	writeCancel()
	cancel()
	wg.Wait()
	conn.Close(websocket.StatusNormalClosure, "session ended")
	logWebsocketDebug(
		"workspace terminal websocket closed",
		"workspace_id", id,
		"exit_code", exitCode,
	)
}

func bridgePtyOwnerAttachment(
	ctx context.Context,
	conn *websocket.Conn,
	attachment *ptyowner.Attachment,
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
				return
			}
			switch typ {
			case websocket.MessageBinary:
				if err := attachment.Write(data); err != nil {
					return
				}
			case websocket.MessageText:
				var msg controlMsg
				if err := json.Unmarshal(data, &msg); err != nil {
					slog.Warn("bad pty owner terminal control message", "err", err)
					continue
				}
				if msg.Type == "resize" {
					_ = attachment.Resize(msg.Cols, msg.Rows)
				}
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
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	select {
	case <-attachment.Done:
		select {
		case <-outputDone:
		case <-time.After(2 * time.Second):
			cancel()
			return false
		}
		cancel()
		writeTerminalExit(conn, attachment.ExitCode())
		return true
	case <-inputDone:
		cancel()
		return false
	case <-outputDone:
		select {
		case <-attachment.Done:
			cancel()
			writeTerminalExit(conn, attachment.ExitCode())
			return true
		case <-time.After(100 * time.Millisecond):
			cancel()
			return false
		}
	case <-ctx.Done():
		return false
	}
}

func writeTerminalExit(conn *websocket.Conn, exitCode int) {
	exitMsg, _ := json.Marshal(map[string]any{
		"type": "exited",
		"code": exitCode,
	})
	writeCtx, writeCancel := context.WithTimeout(
		context.Background(), 2*time.Second)
	_ = conn.Write(writeCtx, websocket.MessageText, exitMsg)
	writeCancel()
}

func (h *Handler) claimTerminalSlot(
	id string,
) (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active == nil {
		h.active = make(map[string]int)
	}
	if h.active[id] > 0 {
		return nil, fmt.Errorf("workspace terminal already active")
	}
	h.active[id]++
	active := h.active[id]
	logWebsocketDebug(
		"workspace terminal slot claimed",
		"workspace_id", id,
		"active", active,
	)
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.active[id]--
			active := h.active[id]
			if active <= 0 {
				delete(h.active, id)
				active = 0
			}
			logWebsocketDebug(
				"workspace terminal slot released",
				"workspace_id", id,
				"active", active,
			)
		})
	}, nil
}

// Ensure Handler implements http.Handler.
var _ http.Handler = (*Handler)(nil)
