package workspaceapi

import (
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/forge/internal/terminal"
)

// RegisterTerminal registers workspace terminal websocket routes on either
// the REST or websocket-prefixed Huma API.
func (h *Handler) RegisterTerminal(api huma.API) {
	h.registerTerminal(api, h != nil && h.runtime != nil)
}

// RegisterTerminalInventory registers every terminal operation against a
// tooling-only API that records route contracts and never serves requests.
func RegisterTerminalInventory(api huma.API) {
	(*Handler)(nil).registerTerminal(api, true)
}

func (h *Handler) registerTerminal(api huma.API, includeRuntime bool) {
	var handler *terminal.Handler
	if h != nil {
		handler = &terminal.Handler{
			Workspaces:  h.workspaces,
			TmuxCommand: slices.Clone(h.tmuxCmd),
		}
	}
	op := &huma.Operation{
		OperationID: "connect-workspace-terminal",
		Method:      http.MethodGet,
		Path:        "/workspaces/{id}/terminal",
		Hidden:      true,
	}
	api.Adapter().Handle(op, func(ctx huma.Context) {
		if handler == nil {
			panic("workspace terminal inventory handler cannot serve requests")
		}
		r, w := humago.Unwrap(ctx)
		handler.ServeHTTP(w, r)
	})

	if !includeRuntime {
		return
	}
	sessionOp := &huma.Operation{
		OperationID: "connect-workspace-runtime-session-terminal",
		Method:      http.MethodGet,
		Path:        "/workspaces/{id}/runtime/sessions/{session_key}/terminal",
		Hidden:      true,
	}
	api.Adapter().Handle(sessionOp, func(ctx huma.Context) {
		if h == nil {
			panic("runtime terminal inventory handler cannot serve requests")
		}
		r, w := humago.Unwrap(ctx)
		h.handleWorkspaceRuntimeSessionTerminal(w, r)
	})
}
