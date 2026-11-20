package server

import (
	"net/http"
	"strings"
	"sync"
)

var tracingTransportInventory = sync.OnceValues(NewTransportInventory)

// otelTraceable reports whether a request should get an otelhttp
// server span. WebSocket upgrades and known SSE/NDJSON modes live for
// the connection lifetime and would produce hours-long spans;
// terminal attach gets its own bounded span instead (internal/tracing).
// Browser trace exports are skipped too: each would add a span per export.
// The transport inventory is built once per process.
func otelTraceable(basePath string) func(*http.Request) bool {
	prefix := strings.TrimSuffix(basePath, "/")
	inventory, err := tracingTransportInventory()
	if err != nil {
		panic("build transport inventory: " + err.Error())
	}
	return func(r *http.Request) bool {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			return false
		}
		path := r.URL.Path
		if prefix != "" && strings.HasPrefix(path, prefix+"/") {
			path = strings.TrimPrefix(path, prefix)
		}
		if path != r.URL.Path {
			cloned := r.Clone(r.Context())
			cloned.URL.Path = path
			r = cloned
		}
		return path != "/api/v1/telemetry/traces" && !inventory.MatchesHTTPStream(r)
	}
}
