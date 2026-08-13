package terminal

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"

	"github.com/coder/websocket"
	"github.com/creack/pty/v2"
)

type controlMsg struct {
	Type string `json:"type"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func processExitCode(waitErr error) int {
	if waitErr == nil {
		return 0
	}
	if exit, ok := waitErr.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	return -1
}

func ptyToWS(
	ctx context.Context,
	ptmx *os.File,
	conn *websocket.Conn,
) {
	buf := make([]byte, 32*1024)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			if wErr := conn.Write(
				ctx, websocket.MessageBinary, buf[:n],
			); wErr != nil {
				logWebsocketDebug("terminal websocket write ended", "err", wErr)
				return
			}
		}
		if err != nil {
			logWebsocketDebug("terminal pty read ended", "err", err)
			return
		}
	}
}

func wsToPTY(
	ctx context.Context,
	ptmx *os.File,
	conn *websocket.Conn,
) {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			logWebsocketDebug("terminal websocket read ended", "err", err)
			return
		}

		switch typ {
		case websocket.MessageBinary:
			if _, wErr := ptmx.Write(data); wErr != nil {
				logWebsocketDebug("terminal pty write ended", "err", wErr)
				return
			}
		case websocket.MessageText:
			var msg controlMsg
			if jsonErr := json.Unmarshal(data, &msg); jsonErr != nil {
				slog.Warn("bad control message", "err", jsonErr)
				continue
			}
			handleControl(ptmx, &msg)
		}
	}
}

func handleControl(ptmx *os.File, msg *controlMsg) {
	if msg.Type != "resize" && msg.Type != "claim_resize" {
		return
	}
	if msg.Cols <= 0 || msg.Rows <= 0 {
		return
	}
	logWebsocketDebug(
		"terminal resize requested",
		"cols", msg.Cols,
		"rows", msg.Rows,
	)
	if err := pty.Setsize(ptmx, &pty.Winsize{
		Rows: uint16(msg.Rows),
		Cols: uint16(msg.Cols),
	}); err != nil {
		slog.Warn("pty resize", "err", err)
	}
}

func parseSize(r *http.Request) (cols, rows int) {
	cols = parseIntParam(r, "cols", 120)
	rows = parseIntParam(r, "rows", 30)
	return cols, rows
}

func parseIntParam(
	r *http.Request, name string, fallback int,
) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}
