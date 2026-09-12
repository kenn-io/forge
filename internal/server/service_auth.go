package server

import (
	"html/template"
	"net/http"
	"strings"

	"go.kenn.io/forge/internal/serviceauth"
)

var serviceAccountPage = template.Must(template.New("service-account").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Forge · GitHub account</title><body><main>
<h1>Forge</h1>
{{if .Connected}}<p>Connected as {{.Login}}.</p><p><a href="{{.Base}}/">Open Forge</a></p>
<form action="{{.Base}}/auth/github/logout" method="post"><button>Disconnect GitHub and sign out</button></form>
<p>Disconnecting stops new GitHub credential requests from agent sessions.</p>
{{else}}<p>Sign in with the GitHub account assigned to this Forge.</p>
<a href="{{.Base}}/auth/github/login">Sign in with GitHub</a>{{end}}
</main></body></html>`))

// handleServiceAuth gates the whole application in service mode. The daemon
// bearer remains usable only on the exact direct loopback listener.
func handleServiceAuth(w http.ResponseWriter, r *http.Request, manager *serviceauth.Manager, basePath string, hostOpts HostCheckOptions, daemonToken string) bool {
	base := strings.TrimRight(basePath, "/")
	if r.URL.Path == "/healthz" || r.URL.Path == "/livez" {
		return false
	}
	if base != "" && r.URL.Path != base && !strings.HasPrefix(r.URL.Path, base+"/") {
		http.NotFound(w, r)
		return true
	}
	path := strings.TrimPrefix(r.URL.Path, base)
	if path == "/healthz" || path == "/livez" {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	switch path {
	case "/auth/github/login", "/auth/github/callback":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
		} else if path == "/auth/github/login" {
			manager.StartLogin(w, r)
		} else {
			manager.CompleteLogin(w, r)
		}
		return true
	}
	authenticated := manager.Authenticated(r)
	if path == "/auth/github" || (!authenticated && !strings.HasPrefix(path, "/auth/") && !strings.HasPrefix(path, "/api") && !strings.HasPrefix(path, "/ws") && r.Method == http.MethodGet) {
		status := serviceauth.Status{}
		if authenticated {
			status = manager.Status()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = serviceAccountPage.Execute(w, struct {
			serviceauth.Status
			Base string
		}{status, base})
		return true
	}
	localBearer := hasValidBearer(r, daemonToken) && isDirectLoopbackListenerRequest(r, hostOpts)
	if !authenticated && !localBearer {
		http.Error(w, "Sign in with GitHub", http.StatusUnauthorized)
		return true
	}
	switch path {
	case "/auth/github/logout":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
		} else if checkCrossOrigin(w, r, hostOpts.TrustReverseProxy) {
			manager.Logout(w, r)
		}
		return true
	case "/auth/github/status":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
		} else {
			writeJSON(w, http.StatusOK, manager.Status())
		}
		return true
	}
	return false
}
