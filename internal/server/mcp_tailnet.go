package server

import (
	"net/http"
	"strings"

	"go.kenn.io/forge/internal/server/httpapi"
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
	if !s.daemonRequests.tailscaleServeEnabled {
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
	if !s.daemonRequests.acceptsTailscaleServeUser(r) {
		writeProblemResponse(w, httpapi.NewProblem(
			http.StatusUnauthorized,
			httpapi.CodeUnauthorized,
			"MCP on this origin requires an allowed Tailscale Serve user",
			nil,
		))
		return true
	}
	if !sameHTTPSOrigin(r) {
		writeProblemResponse(w, httpapi.NewProblem(
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
