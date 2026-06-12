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
	frontend     fs.FS
	fileServer   http.Handler
	startupPage  []byte
}

// NewStartupHandler returns a minimal handler for the window between listener
// bind and full backend readiness. It serves a startup page that reloads when
// the full server is ready, serves immutable frontend assets, and reports API
// and websocket routes as service unavailable.
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

	var fileServer http.Handler
	if frontend != nil {
		fileServer = http.FileServerFS(frontend)
	}

	return &startupHandler{
		hostOpts:     hostOpts,
		allowedHosts: allowedHostsForListener(ln),
		basePath:     basePath,
		frontend:     frontend,
		fileServer:   fileServer,
		startupPage:  []byte(startupPageHTML()),
	}
}

func startupPageHTML() string {
	healthPath, _ := json.Marshal("/healthz")
	return `<!DOCTYPE html><html><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>middleman is starting</title><style>` +
		`:root{color-scheme:light dark;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}` +
		`body{min-height:100vh;margin:0;display:grid;place-items:center;background:#f7f8fb;color:#20242c}` +
		`main{max-width:34rem;padding:2rem;text-align:center}` +
		`h1{margin:0 0 .75rem;font-size:1.35rem;font-weight:650}` +
		`p{margin:0;color:#596273;line-height:1.5}` +
		`@media (prefers-color-scheme:dark){body{background:#15171c;color:#eef1f6}p{color:#aab2c0}}` +
		`</style></head><body><main><h1>middleman is starting</h1>` +
		`<p>The interface will reload when the local server is ready.</p></main><script>` +
		`const middlemanReadyPath=` + scriptSafe(string(healthPath)) + `;` +
		`async function waitForMiddleman(){try{const response=await fetch(middlemanReadyPath,{cache:"no-store",headers:{Accept:"application/json"}});` +
		`if(response.ok){window.location.reload();return;}}catch(error){}` +
		`window.setTimeout(waitForMiddleman,750);}window.setTimeout(waitForMiddleman,250);` +
		`</script></body></html>`
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
	case strings.HasPrefix(r.URL.Path, "/assets/") && h.fileServer != nil:
		h.serveAsset(w, r)
	default:
		h.serveStartupPage(w)
	}
}

func (h *startupHandler) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	f, err := h.frontend.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_ = f.Close()
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	h.fileServer.ServeHTTP(w, r)
}

func (h *startupHandler) serveStartupPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.startupPage)
}
