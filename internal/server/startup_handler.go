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
	basePath     string
	spa          http.Handler
}

// NewStartupHandler returns a minimal handler for the window between listener
// bind and full backend readiness. It serves the real SPA shell and frontend
// assets immediately, while API and websocket routes report service unavailable
// until the full server is swapped in.
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

	var spa http.Handler
	if frontend != nil {
		spa = newSPAAssetHandler(frontend, basePath, func() string {
			safeBase, _ := json.Marshal(basePath)
			return `window.__BASE_PATH__=` + scriptSafe(string(safeBase)) + `;`
		})
	}

	return &startupHandler{
		hostOpts:     hostOpts,
		allowedHosts: allowedHostsForListener(ln),
		basePath:     basePath,
		spa:          spa,
	}
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
	h.serve(w, r)
}

func (h *startupHandler) serve(w http.ResponseWriter, r *http.Request) {
	if h.basePath == "/" {
		h.serveInner(w, r)
		return
	}

	switch r.URL.Path {
	case "/healthz", "/livez":
		h.serveInner(w, r)
		return
	}

	prefix := strings.TrimSuffix(h.basePath, "/")
	if r.URL.Path == prefix {
		http.Redirect(w, r, prefix+"/", http.StatusMovedPermanently)
		return
	}
	if !strings.HasPrefix(r.URL.Path, h.basePath) {
		http.NotFound(w, r)
		return
	}

	stripped := r.Clone(r.Context())
	stripped.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
	if r.URL.RawPath != "" {
		stripped.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, prefix)
	}
	h.serveInner(w, stripped)
}

func (h *startupHandler) serveInner(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/livez":
		writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
	case r.URL.Path == "/healthz",
		r.URL.Path == "/api",
		strings.HasPrefix(r.URL.Path, "/api/"),
		r.URL.Path == "/ws",
		strings.HasPrefix(r.URL.Path, "/ws/"):
		writeStartupUnavailable(w, r)
	default:
		if h.spa == nil {
			http.NotFound(w, r)
			return
		}
		h.spa.ServeHTTP(w, r)
	}
}
