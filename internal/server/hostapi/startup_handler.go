package hostapi

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	"go.kenn.io/forge/internal/server/streamapi"
)

// SwitchHandler delegates each request to the currently installed handler.
// It lets startup bind before the full server exists, then swap to the full
// server without closing the listener.
type SwitchHandler struct {
	current   atomic.Value
	startup   *StartupHandler
	ready     chan struct{}
	readyOnce sync.Once
}

type switchHandlerTarget struct {
	handler http.Handler
}

// NewSwitchHandler creates a handler that initially delegates to initial.
func NewSwitchHandler(initial http.Handler) *SwitchHandler {
	h := &SwitchHandler{ready: make(chan struct{})}
	h.current.Store(switchHandlerTarget{handler: initial})
	return h
}

// NewStartupSwitch answers startup probes from startup and holds every other
// request until Swap installs the full server, which then serves it.
func NewStartupSwitch(startup *StartupHandler) *SwitchHandler {
	h := NewSwitchHandler(startup)
	h.startup = startup
	return h
}

// Swap replaces the delegate used for subsequent requests and releases any
// held requests to it.
func (h *SwitchHandler) Swap(next http.Handler) {
	h.current.Store(switchHandlerTarget{handler: next})
	h.readyOnce.Do(func() { close(h.ready) })
}

// ServeHTTP implements http.Handler.
func (h *SwitchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.startup != nil {
		select {
		case <-h.ready:
		default:
			if h.startup.IsProbe(r) {
				h.startup.ServeHTTP(w, r)
				return
			}
			select {
			case <-h.ready:
			case <-r.Context().Done():
				return
			}
		}
	}
	h.current.Load().(switchHandlerTarget).handler.ServeHTTP(w, r)
}

// StartupHandler answers the probes that must work before the full server
// exists: liveness, readiness (not ready yet), and daemon identity proof.
type StartupHandler struct {
	HostOpts       authapi.HostCheckOptions
	AllowedHosts   map[string]struct{}
	DaemonRequests authapi.DaemonRequestPolicy
	BasePath       string
	Health         routepolicy.HealthResponse
}

// IsProbe reports whether r targets a startup probe, at the root or under
// the base path, as the full server routes them.
func (h *StartupHandler) IsProbe(r *http.Request) bool {
	switch r.URL.Path {
	case daemonruntime.ProofPingPath, "/livez", "/healthz":
		return true
	}
	prefix := strings.TrimSuffix(h.BasePath, "/")
	return prefix != "" &&
		(r.URL.Path == prefix+"/livez" || r.URL.Path == prefix+"/healthz")
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
	if strings.HasSuffix(r.URL.Path, "/livez") {
		authapi.WriteJSON(w, http.StatusOK, h.Health)
		return
	}
	routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
		http.StatusServiceUnavailable,
		httpapi.CodeServiceUnavailable,
		"kenn-forge is still starting",
		map[string]any{"reason": "starting"},
	))
}
