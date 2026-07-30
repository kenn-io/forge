package terminal

import (
	"log/slog"
	"os"
	"strings"
)

func logWebsocketDebug(msg string, args ...any) {
	if !websocketDebugEnabled() {
		return
	}
	slog.Debug(msg, args...)
}

func websocketDebugEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KENN_FORGE_WS_DEBUG"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
