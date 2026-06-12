package server

import (
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"go.kenn.io/middleman/internal/config"
)

// SwitchHandler delegates each request to the currently installed handler.
// It lets startup bind and serve a small UI-ready handler, then swap to the
// full server without closing the listener.
type SwitchHandler struct {
	current atomic.Value
}

type switchHandlerTarget struct {
	handler http.Handler
}

// NewSwitchHandler creates a handler that initially delegates to initial.
func NewSwitchHandler(initial http.Handler) *SwitchHandler {
	h := &SwitchHandler{}
	h.current.Store(switchHandlerTarget{handler: initial})
	return h
}

// Swap replaces the delegate used for subsequent requests.
func (h *SwitchHandler) Swap(next http.Handler) {
	h.current.Store(switchHandlerTarget{handler: next})
}

// ServeHTTP implements http.Handler.
func (h *SwitchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.current.Load().(switchHandlerTarget).handler.ServeHTTP(w, r)
}

type startupHandler struct {
	hostOpts     HostCheckOptions
	allowedHosts map[string]struct{}
	handler      http.Handler
}

// NewStartupHandler returns a minimal handler for the window between listener
// bind and full backend readiness. It serves the embedded SPA and health/livez,
// but reports API and websocket routes as service unavailable.
func NewStartupHandler(
	frontend fs.FS,
	cfg *config.Config,
	options ServerOptions,
	ln net.Listener,
) http.Handler {
	basePath := "/"
	if cfg != nil && cfg.BasePath != "" {
		basePath = cfg.BasePath
	}
	hostOpts := resolveHostCheckOptions(
		cfg,
		options.HostCheck,
		options.HostCheckAllowLoopbackAnyPort,
	)

	inner := http.NewServeMux()
	inner.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
	})
	inner.HandleFunc("/healthz", writeStartupUnavailable)
	inner.HandleFunc("/api/", writeStartupUnavailable)
	inner.HandleFunc("/api", writeStartupUnavailable)
	inner.HandleFunc("/ws/", writeStartupUnavailable)
	inner.HandleFunc("/ws", writeStartupUnavailable)
	if frontend != nil {
		inner.Handle("/", newSPAAssetHandler(frontend, basePath, func() string {
			return startupBootstrapScript(basePath)
		}))
	} else {
		inner.HandleFunc("/", writeStartupUnavailable)
	}

	var handler http.Handler = inner
	if basePath != "/" {
		outer := http.NewServeMux()
		prefix := strings.TrimSuffix(basePath, "/")
		outer.Handle("/healthz", inner)
		outer.Handle("/livez", inner)
		outer.Handle(basePath, http.StripPrefix(prefix, inner))
		handler = outer
	}

	return &startupHandler{
		hostOpts:     hostOpts,
		allowedHosts: allowedHostsForListener(ln),
		handler:      handler,
	}
}

func startupBootstrapScript(basePath string) string {
	safeBase, _ := json.Marshal(basePath)
	return `window.__BASE_PATH__=` + scriptSafe(string(safeBase)) + `;`
}

func writeStartupUnavailable(w http.ResponseWriter, _ *http.Request) {
	writeProblemResponse(w, newProblem(
		http.StatusServiceUnavailable,
		CodeServiceUnavailable,
		"middleman is still starting",
		map[string]any{"reason": "starting"},
	))
}

func (h *startupHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !checkHost(w, r, h.hostOpts) {
		return
	}
	if !checkListenerHost(w, r, h.allowedHosts) {
		return
	}
	h.handler.ServeHTTP(w, r)
}
