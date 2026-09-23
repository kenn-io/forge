package hostapi

import (
	"net/http"
	"strings"
	"sync/atomic"

	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	"go.kenn.io/forge/internal/server/streamapi"
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

type StartupHandler struct {
	HostOpts       authapi.HostCheckOptions
	AllowedHosts   map[string]struct{}
	DaemonRequests authapi.DaemonRequestPolicy
	BasePath       string
	Spa            http.Handler
	Health         routepolicy.HealthResponse
}

func writeStartupUnavailable(w http.ResponseWriter, _ *http.Request) {
	routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
		http.StatusServiceUnavailable,
		httpapi.CodeServiceUnavailable,
		"kenn-forge is still starting",
		map[string]any{"reason": "starting"},
	))
}

func (h *StartupHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	admission := h.DaemonRequests.Admit(w, r, h.HostOpts, false)
	if admission.Handled {
		return
	}
	if !CheckHost(w, r, h.HostOpts) {
		return
	}
	if !streamapi.CheckListenerHost(w, r, h.AllowedHosts) {
		return
	}
	h.serve(w, r)
}

func (h *StartupHandler) serve(w http.ResponseWriter, r *http.Request) {
	if h.BasePath == "/" {
		h.serveInner(w, r)
		return
	}

	switch r.URL.Path {
	case "/healthz", "/livez":
		h.serveInner(w, r)
		return
	}

	prefix := strings.TrimSuffix(h.BasePath, "/")
	if r.URL.Path == prefix {
		http.Redirect(w, r, prefix+"/", http.StatusMovedPermanently)
		return
	}
	if !strings.HasPrefix(r.URL.Path, h.BasePath) {
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

func (h *StartupHandler) serveInner(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/livez":
		authapi.WriteJSON(w, http.StatusOK, h.Health)
	case r.URL.Path == "/healthz",
		r.URL.Path == "/api",
		strings.HasPrefix(r.URL.Path, "/api/"),
		r.URL.Path == "/ws",
		strings.HasPrefix(r.URL.Path, "/ws/"):
		writeStartupUnavailable(w, r)
	default:
		if h.Spa == nil {
			http.NotFound(w, r)
			return
		}
		h.Spa.ServeHTTP(w, r)
	}
}
