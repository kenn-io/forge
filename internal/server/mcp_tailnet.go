package server

import (
	"net/http"
	"net/url"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/routepolicy"
)

// SetTailnetMCPHandler publishes the MCP handler on the main listener for
// allowlisted Tailscale Serve users, so a tailnet MCP client needs no bearer.
// The loopback MCP listener remains the bearer-authenticated local path.
func (s *Server) SetTailnetMCPHandler(handler http.Handler) {
	s.tailnetMCP.Store(&handler)
}

// serveTailnetMCP handles /mcp when Tailscale identity mode is enabled. The
// identity header is ambient like a cookie, so a request that names another
// origin is rejected rather than letting a web page drive agent tools.
func (s *Server) serveTailnetMCP(w http.ResponseWriter, r *http.Request) bool {
	if !s.daemonRequests.TailscaleServeEnabled() {
		return false
	}
	path := r.URL.Path
	if s.basePath != "/" {
		path = strings.TrimPrefix(path, strings.TrimSuffix(s.basePath, "/"))
	}
	if path != "/mcp" {
		return false
	}
	handler := s.tailnetMCP.Load()
	if handler == nil {
		http.NotFound(w, r)
		return true
	}
	if !s.daemonRequests.AcceptsTailscaleServeUser(r) {
		routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
			http.StatusUnauthorized,
			httpapi.CodeUnauthorized,
			"MCP on this origin requires an allowed Tailscale Serve user",
			nil,
		))
		return true
	}
	if !sameHTTPSOrigin(r) {
		routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
			http.StatusForbidden,
			httpapi.CodeForbidden,
			"cross-origin MCP access is not allowed",
			nil,
		))
		return true
	}
	request := r.Clone(r.Context())
	requestURL := *r.URL
	requestURL.Path = "/mcp"
	requestURL.RawPath = ""
	request.URL = &requestURL
	(*handler).ServeHTTP(w, request)
	return true
}

func sameHTTPSOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(strings.TrimSpace(origins[0]))
	if err != nil || origin.Scheme != "https" ||
		origin.User != nil || origin.Host == "" || origin.Path != "" ||
		origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	originHost, err := config.ParseHostKey(origin.Host)
	if err != nil {
		return false
	}
	requestHost, err := config.ParseHostKey(r.Host)
	if err != nil {
		return false
	}
	if originHost.Port == "" {
		originHost.Port = "443"
	}
	if requestHost.Port == "" {
		requestHost.Port = "443"
	}
	return originHost.Equal(requestHost)
}
