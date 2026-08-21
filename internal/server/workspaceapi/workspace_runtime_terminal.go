package workspaceapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ptysize"
	"go.kenn.io/forge/internal/terminalwebsocket"
	"go.kenn.io/forge/internal/tracing"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

type runtimeTerminalControlMsg struct {
	Type        string `json:"type"`
	Cols        int    `json:"cols,omitempty"`
	Rows        int    `json:"rows,omitempty"`
	PixelWidth  int    `json:"pixel_width,omitempty"`
	PixelHeight int    `json:"pixel_height,omitempty"`
	Active      *bool  `json:"active,omitempty"`
}

// RuntimeTerminalControlMsg is shared with Fleet's websocket relay.
type RuntimeTerminalControlMsg = runtimeTerminalControlMsg

const (
	runtimeTerminalSetupStepTimeout = 2 * time.Second
	runtimeTerminalExitDrainTimeout = time.Second
)

func (s *Handler) handleWorkspaceRuntimeSessionTerminal(
	w http.ResponseWriter,
	r *http.Request,
) {
	ctx, attachSpan := tracing.StartAttachSpan(r, "terminal.attach")
	r = r.WithContext(ctx)
	// endAttachSpan is called explicitly once setup (attach + initial
	// resize) completes, so the span stays bounded and never covers
	// the bridge loop; it is also deferred as a safety net for every
	// early-return error path. OnceFunc makes the double-call harmless.
	endAttachSpan := sync.OnceFunc(func() { attachSpan.End() })
	defer endAttachSpan()

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
		attachSpan.SetAttributes(attribute.Bool("error", true))
		return
	}

	attachment, err := s.runtime.AttachSessionWithOptions(
		summary.ID, r.PathValue("session_key"),
		localruntime.AttachSessionOptions{
			ResizePriority: runtimeTerminalResizePriority(r),
			ResizeActive:   parseRuntimeTerminalResizeActive(r),
			ReplayBoundary: parseRuntimeTerminalReplayBoundary(r),
		},
	)
	if err != nil {
		logWebsocketDebug(
			"runtime terminal attach failed",
			"workspace_id", summary.ID,
			"session_key", r.PathValue("session_key"),
			"err", err,
		)
		attachSpan.SetAttributes(attribute.Bool("error", true))
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.serveRuntimeTerminal(w, r, attachment, attachSpan, endAttachSpan)
}

func runtimeTerminalResizePriority(r *http.Request) localruntime.ResizePriority {
	if r.Header.Get("X-Kenn-Forge-Fleet-Host") != "" {
		return localruntime.ResizePriorityRemote
	}
	return localruntime.ResizePriorityLocal
}

func parseRuntimeTerminalResizeActive(r *http.Request) bool {
	raw := r.URL.Query().Get("resize_active")
	return raw == "" || raw == "1" || raw == "true"
}

// ParseRuntimeTerminalResizeActive reports whether a proxied terminal resize
// belongs to the active display region.
func ParseRuntimeTerminalResizeActive(r *http.Request) bool {
	return parseRuntimeTerminalResizeActive(r)
}

func parseRuntimeTerminalReplayBoundary(r *http.Request) bool {
	raw := r.URL.Query().Get("replay_boundary")
	return raw == "1" || raw == "true"
}

func (s *Handler) serveRuntimeTerminal(
	w http.ResponseWriter,
	r *http.Request,
	attachment *localruntime.Attachment,
	attachSpan trace.Span,
	endAttachSpan func(),
) {
	conn, err := terminalwebsocket.Accept(w, r)
	if err != nil {
		slog.Error("websocket accept", "err", err)
		attachSpan.SetAttributes(attribute.Bool("error", true))
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
	if geometry, ok := parseRuntimeTerminalGeometry(r); ok &&
		!parseRuntimeTerminalReplayBoundary(r) {
		logWebsocketDebug(
			"runtime terminal initial resize",
			"workspace_id", info.WorkspaceID,
			"session_key", info.Key,
			"cols", geometry.Cols,
			"rows", geometry.Rows,
			"pixel_width", geometry.PixelWidth,
			"pixel_height", geometry.PixelHeight,
		)
		if err := attachment.Resize(geometry); err != nil {
			slog.Warn("runtime terminal initial resize", "err", err)
		}
		// subscribe queues ordinary-screen replay before the websocket is
		// accepted. Forward that already-available frame before the
		// synchronous tmux refresh so the browser can paint while tmux
		// drains its resize IPC. A closed Output remains closed for the
		// bridge to observe, and any later output stays bridge-owned.
		if err := forwardAvailableRuntimeOutput(
			r.Context(), runtimeTerminalSetupStepTimeout, attachment.Output,
			func(ctx context.Context, data []byte) error {
				return conn.Write(ctx, websocket.MessageBinary, data)
			},
		); err != nil {
			slog.Warn("runtime terminal initial replay", "err", err)
			attachSpan.SetAttributes(attribute.Bool("error", true))
			attachment.Close()
			_ = conn.CloseNow()
			return
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
			r.Context(), runtimeTerminalSetupStepTimeout,
		)
		if err := attachment.Refresh(refreshCtx); err != nil {
			slog.Warn(
				"runtime terminal initial refresh", "err", err,
			)
		}
		refreshCancel()
	}

	// Setup (attach + initial resize/refresh) is complete; end the
	// span before the long-lived bridge loop so terminal.attach stays
	// bounded to the attach phase.
	endAttachSpan()

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

func forwardAvailableRuntimeOutput(
	ctx context.Context,
	timeout time.Duration,
	output <-chan []byte,
	write func(context.Context, []byte) error,
) error {
	select {
	case data, ok := <-output:
		if ok {
			writeCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return write(writeCtx, data)
		}
	default:
	}
	return nil
}

func (s *Handler) runtimeWorkspaceForHTTP(
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

func parseRuntimeTerminalGeometry(r *http.Request) (ptysize.Geometry, bool) {
	cols, colsOK := parsePositiveQueryInt(r, "cols")
	rows, rowsOK := parsePositiveQueryInt(r, "rows")
	pixelWidth, _ := parsePositiveQueryInt(r, "pixel_width")
	pixelHeight, _ := parsePositiveQueryInt(r, "pixel_height")
	return ptysize.Normalize(ptysize.Geometry{
		Cols:        cols,
		Rows:        rows,
		PixelWidth:  pixelWidth,
		PixelHeight: pixelHeight,
	}), colsOK && rowsOK
}

// ParseRuntimeTerminalGeometry parses the requested terminal geometry.
func ParseRuntimeTerminalGeometry(r *http.Request) (ptysize.Geometry, bool) {
	return parseRuntimeTerminalGeometry(r)
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
				if err := handleRuntimeTerminalControl(ctx, attachment, data); err != nil {
					slog.Warn("runtime terminal control failed", "err", err)
					return
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
				messageType := websocket.MessageBinary
				if data == nil {
					messageType = websocket.MessageText
					data = []byte(`{"type":"replay_ready"}`)
				}
				if err := conn.Write(
					ctx, messageType, data,
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
		case <-time.After(runtimeTerminalExitDrainTimeout):
		}
		// Write the frame BEFORE cancel: coder/websocket tears down
		// the underlying connection when the input goroutine's
		// Read context is canceled, which races our Write.
		recoverableDetach := attachment.RecoverableDetach()
		if !recoverableDetach {
			writeRuntimeExit(conn, attachment.Info())
		}
		cancel()
		return !recoverableDetach
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
		//      so we do NOT gate the frame on attachment.Done. PTY-owner
		//      sessions publish their exit code before closing subscribers;
		//      other backends may still emit -1 when process wait lags EOF.
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
		exited := closed && !attachment.RecoverableDetach()
		if exited {
			writeRuntimeExit(conn, attachment.Info())
		}
		cancel()
		return exited
	case <-ctx.Done():
		return false
	}
}

// BridgeRuntimeAttachment bridges a websocket to an active runtime
// attachment. It is shared by workspace and Fleet terminal tests.
func BridgeRuntimeAttachment(
	ctx context.Context,
	conn *websocket.Conn,
	attachment *localruntime.Attachment,
) bool {
	return bridgeRuntimeAttachment(ctx, conn, attachment)
}

func handleRuntimeTerminalControl(
	ctx context.Context,
	attachment *localruntime.Attachment,
	data []byte,
) error {
	var msg runtimeTerminalControlMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		slog.Warn("bad runtime terminal control message", "err", err)
		return nil
	}
	info := attachment.Info()
	switch msg.Type {
	case "claim_resize":
		settle, err := attachment.ClaimResize(ptysize.Geometry{
			Cols:        msg.Cols,
			Rows:        msg.Rows,
			PixelWidth:  msg.PixelWidth,
			PixelHeight: msg.PixelHeight,
		})
		if err != nil {
			return err
		}
		if !settle {
			return nil
		}
		refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := attachment.Refresh(refreshCtx); err != nil {
			return err
		}
		attachment.ResizeSettled()
		return nil
	case "resize_active":
		if msg.Active != nil {
			attachment.SetResizeActive(*msg.Active)
		}
		return nil
	case "refresh":
		logWebsocketDebug(
			"runtime terminal refresh requested",
			"workspace_id", info.WorkspaceID,
			"session_key", info.Key,
			"cols", msg.Cols,
			"rows", msg.Rows,
			"pixel_width", msg.PixelWidth,
			"pixel_height", msg.PixelHeight,
		)
		if msg.Cols > 0 && msg.Rows > 0 {
			if err := attachment.Resize(ptysize.Geometry{
				Cols:        msg.Cols,
				Rows:        msg.Rows,
				PixelWidth:  msg.PixelWidth,
				PixelHeight: msg.PixelHeight,
			}); err != nil {
				slog.Warn("runtime terminal refresh resize", "err", err)
			}
		}
		refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := attachment.Refresh(refreshCtx); err != nil {
			slog.Warn("runtime terminal refresh", "err", err)
		}
		return nil
	case "resize":
		logWebsocketDebug(
			"runtime terminal resize requested",
			"workspace_id", info.WorkspaceID,
			"session_key", info.Key,
			"cols", msg.Cols,
			"rows", msg.Rows,
			"pixel_width", msg.PixelWidth,
			"pixel_height", msg.PixelHeight,
		)
		if err := attachment.Resize(ptysize.Geometry{
			Cols:        msg.Cols,
			Rows:        msg.Rows,
			PixelWidth:  msg.PixelWidth,
			PixelHeight: msg.PixelHeight,
		}); err != nil {
			slog.Warn("runtime terminal resize", "err", err)
		}
	}
	return nil
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
