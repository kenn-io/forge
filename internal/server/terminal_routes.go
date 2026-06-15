package server

import (
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/middleman/internal/terminal"
)

func terminalAPIConfig() huma.Config {
	config := huma.DefaultConfig("middleman terminal websocket", "0.1.0")
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	return config
}

func (s *Server) registerTerminalAPI(api huma.API, tmuxCmd []string) {
	s.registerFleetTerminalRoutes(api)

	handler := &terminal.Handler{
		Workspaces:  s.workspaces,
		TmuxCommand: slices.Clone(tmuxCmd),
	}
	op := &huma.Operation{
		OperationID: "connect-workspace-terminal",
		Method:      http.MethodGet,
		Path:        "/workspaces/{id}/terminal",
		Hidden:      true,
	}
	api.Adapter().Handle(op, func(ctx huma.Context) {
		r, w := humago.Unwrap(ctx)
		handler.ServeHTTP(w, r)
	})

	if s.runtime == nil {
		return
	}
	sessionOp := &huma.Operation{
		OperationID: "connect-workspace-runtime-session-terminal",
		Method:      http.MethodGet,
		Path:        "/workspaces/{id}/runtime/sessions/{session_key}/terminal",
		Hidden:      true,
	}
	api.Adapter().Handle(sessionOp, func(ctx huma.Context) {
		r, w := humago.Unwrap(ctx)
		s.handleWorkspaceRuntimeSessionTerminal(w, r)
	})
}
