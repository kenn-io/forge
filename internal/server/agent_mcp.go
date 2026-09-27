package server

import (
	"net/http"
	"net/url"
	"strings"

	"go.kenn.io/forge/internal/config"
)

// SetAgentMCPHandler exposes the host's Forge tools to its launched agents even
// when the optional companion listener is disabled.
func (s *Server) SetAgentMCPHandler(handler http.Handler) {
	endpoint, err := url.Parse(s.options.AgentMCPURL)
	if err != nil || endpoint.Host == "" {
		return
	}
	bind, err := config.ParseHostKey(endpoint.Host)
	if err != nil {
		return
	}
	guarded := NewMCPHTTPGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := r.Clone(r.Context())
		requestURL := *r.URL
		requestURL.Path, requestURL.RawPath = "/mcp", ""
		request.URL = &requestURL
		handler.ServeHTTP(w, request)
	}), MCPHTTPGuardOptions{Bind: bind, Token: s.options.DaemonAccess.Token, RequireAuth: true})
	s.agentMCP.Store(&guarded)
}

func (s *Server) serveAgentMCP(w http.ResponseWriter, r *http.Request) bool {
	requestPath := r.URL.Path
	if s.basePath != "/" {
		requestPath = strings.TrimPrefix(requestPath, strings.TrimSuffix(s.basePath, "/"))
	}
	if requestPath != "/agent-mcp" {
		return false
	}
	handler := s.agentMCP.Load()
	if handler == nil {
		http.Error(w, "Agent tools are not ready", http.StatusServiceUnavailable)
		return true
	}
	(*handler).ServeHTTP(w, r)
	return true
}
