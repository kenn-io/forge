package server

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/mcpapi"
	"go.kenn.io/forge/internal/server/routepolicy"
)

// SetTailnetMCPHandler publishes the MCP handler on the main listener for
// daemon bearer clients and allowlisted Tailscale Serve users, so an MCP
// client on another machine can use the main port.
func (s *Server) SetTailnetMCPHandler(handler http.Handler) {
	s.tailnetMCP.Store(&handler)
}

type localMCPHandler struct {
	handler http.Handler
	port    string
}

// SetLocalMCPHandler serves the loopback companion on the main listener when
// it is configured on this listener's port. Direct loopback /mcp requests get
// the companion guard, with or without a bearer, and skip the main listener's
// host and reverse-proxy checks as on the dedicated companion listener; other
// /mcp requests keep the bearer and Serve policy.
func (s *Server) SetLocalMCPHandler(next http.Handler, opts mcpapi.MCPHTTPGuardOptions) {
	s.localMCP.Store(&localMCPHandler{
		handler: mcpapi.NewMCPHTTPGuard(next, opts),
		port:    opts.Bind.Port,
	})
}

// isDirectLocalMCPRequest reports whether r is a direct loopback request for
// the shared companion, which the companion guard admits instead of the main
// listener's host checks.
func (s *Server) isDirectLocalMCPRequest(r *http.Request) bool {
	local := s.localMCP.Load()
	return local != nil &&
		r.URL.Path == strings.TrimSuffix(s.basePath, "/")+"/mcp" &&
		mcpapi.IsDirectLoopbackRequest(r, local.port)
}

// registerMCPRoute registers /mcp as hidden operations for the Streamable
// HTTP methods; the MCP SDK owns the request and response bodies.
func (s *Server) registerMCPRoute(adapter huma.Adapter) {
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		op := &huma.Operation{
			OperationID: "mcp-" + strings.ToLower(method), Method: method, Path: "/mcp", Hidden: true,
		}
		adapter.Handle(op, func(ctx huma.Context) {
			r, w := humago.Unwrap(ctx)
			s.serveMCP(w, r)
		})
	}
}

// serveMCP is the /mcp route. Direct loopback requests use the shared
// companion when one is installed; others need a daemon bearer or an allowed
// Tailscale Serve user. The identity header is ambient like a cookie, so a
// request that names another origin is rejected rather than letting a web
// page drive agent tools.
func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	if local := s.localMCP.Load(); local != nil && mcpapi.IsDirectLoopbackRequest(r, local.port) {
		local.handler.ServeHTTP(w, r)
		return
	}
	handler := s.tailnetMCP.Load()
	if handler == nil {
		http.NotFound(w, r)
		return
	}
	if !authapi.HasValidBearer(r, s.daemonRequests.Token) &&
		!s.daemonRequests.AcceptsTailscaleServeUser(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="kenn-forge"`)
		routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
			http.StatusUnauthorized,
			httpapi.CodeUnauthorized,
			"MCP on this origin requires the daemon bearer token or an allowed Tailscale Serve user",
			nil,
		))
		return
	}
	if !sameHTTPSOrigin(r) {
		routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
			http.StatusForbidden,
			httpapi.CodeForbidden,
			"cross-origin MCP access is not allowed",
			nil,
		))
		return
	}
	(*handler).ServeHTTP(w, r)
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
