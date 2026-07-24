package workspaceapi

import (
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/middleman/internal/terminal"
)

// RegisterTerminal registers workspace terminal websocket routes on either
// the REST or websocket-prefixed Huma API.
func (h *Handler) RegisterTerminal(api huma.API) {
	handler := &terminal.Handler{
		Workspaces:  h.workspaces,
		TmuxCommand: slices.Clone(h.tmuxCmd),
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

	if h.runtime == nil {
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
		h.handleWorkspaceRuntimeSessionTerminal(w, r)
	})
}
