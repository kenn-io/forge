package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"go.kenn.io/middleman/internal/config"
)

type fleetRESTProxyRoute struct {
	operationID string
	method      string
	path        string
	summary     string
	pathParams  []string
	queryParams []*huma.Param
	body        bool
	targetPath  func(*http.Request) string
}

type fleetHostTarget struct {
	self    bool
	peer    config.FleetPeer
	sshPeer *config.FleetSSHPeer
}

func (s *Server) registerFleetOperationRoutes(api huma.API) {
	routes := []fleetRESTProxyRoute{
		{
			operationID: "list-fleet-workspaces",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/workspaces",
			summary:     "List workspaces on fleet host",
			pathParams:  []string{"host_key"},
			targetPath: func(*http.Request) string {
				return "/api/v1/workspaces"
			},
		},
		{
			operationID: "create-fleet-workspace",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/workspaces",
			summary:     "Create workspace on fleet host",
			pathParams:  []string{"host_key"},
			body:        true,
			targetPath: func(*http.Request) string {
				return "/api/v1/workspaces"
			},
		},
		{
			operationID: "create-fleet-issue-workspace",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/issues/{provider}/{owner}/{name}/{number}/workspace",
			summary:     "Create issue workspace on fleet host",
			pathParams:  []string{"host_key", "provider", "owner", "name", "number"},
			body:        true,
			targetPath: func(r *http.Request) string {
				return "/api/v1/issues/" +
					escapePath(r.PathValue("provider")) + "/" +
					escapePath(r.PathValue("owner")) + "/" +
					escapePath(r.PathValue("name")) + "/" +
					escapePath(r.PathValue("number")) + "/workspace"
			},
		},
		{
			operationID: "create-fleet-issue-workspace-on-platform-host",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/host/{platform_host}/issues/{provider}/{owner}/{name}/{number}/workspace",
			summary:     "Create issue workspace on fleet host",
			pathParams:  []string{"host_key", "platform_host", "provider", "owner", "name", "number"},
			body:        true,
			targetPath: func(r *http.Request) string {
				return "/api/v1/host/" + escapePath(r.PathValue("platform_host")) +
					"/issues/" + escapePath(r.PathValue("provider")) + "/" +
					escapePath(r.PathValue("owner")) + "/" +
					escapePath(r.PathValue("name")) + "/" +
					escapePath(r.PathValue("number")) + "/workspace"
			},
		},
		{
			operationID: "get-fleet-workspace",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}",
			summary:     "Get workspace on fleet host",
			pathParams:  []string{"host_key", "id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id"))
			},
		},
		{
			operationID: "retry-fleet-workspace",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/retry",
			summary:     "Retry workspace setup on fleet host",
			pathParams:  []string{"host_key", "id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) + "/retry"
			},
		},
		{
			operationID: "refresh-fleet-workspace",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/refresh",
			summary:     "Refresh workspace metadata on fleet host",
			pathParams:  []string{"host_key", "id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) + "/refresh"
			},
		},
		{
			operationID: "get-fleet-workspace-runtime",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/runtime",
			summary:     "Get workspace runtime on fleet host",
			pathParams:  []string{"host_key", "id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) + "/runtime"
			},
		},
		{
			operationID: "launch-fleet-workspace-runtime-session",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions",
			summary:     "Launch workspace session on fleet host",
			pathParams:  []string{"host_key", "id"},
			body:        true,
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) + "/runtime/sessions"
			},
		},
		{
			operationID: "rename-fleet-workspace-runtime-session",
			method:      http.MethodPatch,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions/{session_key}",
			summary:     "Rename workspace session on fleet host",
			pathParams:  []string{"host_key", "id", "session_key"},
			body:        true,
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) +
					"/runtime/sessions/" + escapePath(r.PathValue("session_key"))
			},
		},
		{
			operationID: "stop-fleet-workspace-runtime-session",
			method:      http.MethodDelete,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions/{session_key}",
			summary:     "Stop workspace session on fleet host",
			pathParams:  []string{"host_key", "id", "session_key"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) +
					"/runtime/sessions/" + escapePath(r.PathValue("session_key"))
			},
		},
		{
			operationID: "get-fleet-workspace-runtime-session-attach-spec",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions/{session_key}/attach-spec",
			summary:     "Get workspace session attach spec on fleet host",
			pathParams:  []string{"host_key", "id", "session_key"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) +
					"/runtime/sessions/" + escapePath(r.PathValue("session_key")) +
					"/attach-spec"
			},
		},
		{
			operationID: "complete-fleet-filesystem-path",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/filesystem/complete",
			summary:     "Complete a filesystem path on fleet host",
			pathParams:  []string{"host_key"},
			queryParams: []*huma.Param{fleetFilesystemPathQueryParam(
				"The partial path to complete on the owning host.",
			)},
			targetPath: func(r *http.Request) string {
				return "/api/v1/filesystem/complete"
			},
		},
		{
			operationID: "validate-fleet-filesystem-repo",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/filesystem/validate-repo",
			summary:     "Resolve a repository root on fleet host",
			pathParams:  []string{"host_key"},
			queryParams: []*huma.Param{fleetFilesystemPathQueryParam(
				"The path to resolve to a repository root on the owning host.",
			)},
			targetPath: func(r *http.Request) string {
				return "/api/v1/filesystem/validate-repo"
			},
		},
		{
			operationID: "launch-fleet-host-runtime-session",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/runtime/sessions",
			summary:     "Launch host runtime session on fleet host",
			pathParams:  []string{"host_key"},
			body:        true,
			targetPath: func(r *http.Request) string {
				return "/api/v1/runtime/sessions"
			},
		},
		{
			operationID: "stop-fleet-host-runtime-session",
			method:      http.MethodDelete,
			path:        "/fleet/hosts/{host_key}/runtime/sessions/{session_key}",
			summary:     "Stop host runtime session on fleet host",
			pathParams:  []string{"host_key", "session_key"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/runtime/sessions/" +
					escapePath(r.PathValue("session_key"))
			},
		},
		{
			operationID: "get-fleet-host-runtime-session-attach-spec",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/runtime/sessions/{session_key}/attach-spec",
			summary:     "Get host runtime session attach spec on fleet host",
			pathParams:  []string{"host_key", "session_key"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/runtime/sessions/" +
					escapePath(r.PathValue("session_key")) +
					"/attach-spec"
			},
		},
		{
			operationID: "clone-fleet-project",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/projects/clone",
			summary:     "Clone a repository into a project on fleet host",
			pathParams:  []string{"host_key"},
			body:        true,
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/clone"
			},
		},
		{
			operationID: "list-fleet-project-branches",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/branches",
			summary:     "List project branches on fleet host",
			pathParams:  []string{"host_key", "project_id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/branches"
			},
		},
		{
			operationID: "inspect-fleet-project-worktree",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/inspect",
			summary:     "Inspect project worktree on fleet host",
			pathParams:  []string{"host_key", "project_id", "worktree_id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees/" + escapePath(r.PathValue("worktree_id")) +
					"/inspect"
			},
		},
		{
			operationID: "get-fleet-project-worktree-runtime",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime",
			summary:     "Get project worktree runtime on fleet host",
			pathParams:  []string{"host_key", "project_id", "worktree_id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees/" + escapePath(r.PathValue("worktree_id")) +
					"/runtime"
			},
		},
		{
			operationID: "ensure-fleet-project-worktree-runtime-shell",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime/shell",
			summary:     "Ensure project worktree shell on fleet host",
			pathParams:  []string{"host_key", "project_id", "worktree_id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees/" + escapePath(r.PathValue("worktree_id")) +
					"/runtime/shell"
			},
		},
		{
			operationID: "launch-fleet-project-worktree-runtime-session",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime/sessions",
			summary:     "Launch project worktree session on fleet host",
			pathParams:  []string{"host_key", "project_id", "worktree_id"},
			body:        true,
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees/" + escapePath(r.PathValue("worktree_id")) +
					"/runtime/sessions"
			},
		},
		{
			operationID: "stop-fleet-project-worktree-runtime-session",
			method:      http.MethodDelete,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime/sessions/{session_key}",
			summary:     "Stop project worktree session on fleet host",
			pathParams:  []string{"host_key", "project_id", "worktree_id", "session_key"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees/" + escapePath(r.PathValue("worktree_id")) +
					"/runtime/sessions/" + escapePath(r.PathValue("session_key"))
			},
		},
		{
			operationID: "get-fleet-project-worktree-runtime-session-attach-spec",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime/sessions/{session_key}/attach-spec",
			summary:     "Get project worktree session attach spec on fleet host",
			pathParams:  []string{"host_key", "project_id", "worktree_id", "session_key"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees/" + escapePath(r.PathValue("worktree_id")) +
					"/runtime/sessions/" + escapePath(r.PathValue("session_key")) +
					"/attach-spec"
			},
		},
		{
			operationID: "delete-fleet-workspace",
			method:      http.MethodDelete,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}",
			summary:     "Delete workspace on fleet host",
			pathParams:  []string{"host_key", "id"},
			queryParams: []*huma.Param{fleetForceQueryParam()},
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id"))
			},
		},
		{
			operationID: "create-fleet-project-worktree",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees",
			summary:     "Create project worktree on fleet host",
			pathParams:  []string{"host_key", "project_id"},
			body:        true,
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees"
			},
		},
		{
			operationID: "create-fleet-project-worktree-from-merge-request",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees/from-merge-request",
			summary:     "Create project worktree from a merge request on fleet host",
			pathParams:  []string{"host_key", "project_id"},
			body:        true,
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees/from-merge-request"
			},
		},
		{
			operationID: "remove-fleet-project-worktree",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/delete",
			summary:     "Remove project worktree on fleet host",
			pathParams:  []string{"host_key", "project_id", "worktree_id"},
			body:        true,
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees/" + escapePath(r.PathValue("worktree_id")) +
					"/delete"
			},
		},
		{
			operationID: "set-fleet-project-worktree-session-backend",
			method:      http.MethodPut,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/session-backend",
			summary:     "Set project worktree session backend on fleet host",
			pathParams:  []string{"host_key", "project_id", "worktree_id"},
			body:        true,
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees/" + escapePath(r.PathValue("worktree_id")) +
					"/session-backend"
			},
		},
		{
			// "links" (not "linked-issues") to match the local op id's
			// generic-registry naming; the path keeps the precise field name.
			operationID: "set-fleet-project-worktree-links",
			method:      http.MethodPut,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/linked-issues",
			summary:     "Set project worktree linked issues on fleet host",
			pathParams:  []string{"host_key", "project_id", "worktree_id"},
			body:        true,
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees/" + escapePath(r.PathValue("worktree_id")) +
					"/linked-issues"
			},
		},
		{
			operationID: "refresh-fleet-project-worktree-stats",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/refresh-stats",
			summary:     "Refresh project worktree git stats on fleet host",
			pathParams:  []string{"host_key", "project_id", "worktree_id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees/" + escapePath(r.PathValue("worktree_id")) +
					"/refresh-stats"
			},
		},
	}

	for _, route := range routes {
		op := &huma.Operation{
			OperationID:  route.operationID,
			Method:       route.method,
			Path:         route.path,
			Summary:      route.summary,
			Tags:         []string{"Fleet"},
			Parameters:   fleetProxyParams(route.pathParams, route.queryParams...),
			Responses:    fleetProxyResponses(),
			MaxBodyBytes: -1,
		}
		if route.body {
			op.RequestBody = fleetProxyRequestBody()
		}
		api.OpenAPI().AddOperation(op)
		api.Adapter().Handle(op, func(ctx huma.Context) {
			r, w := humago.Unwrap(ctx)
			s.serveFleetRESTProxy(w, r, route.targetPath(r))
		})
	}
}

func (s *Server) registerFleetTerminalRoutes(api huma.API) {
	routes := []struct {
		operationID string
		path        string
		targetPath  func(*http.Request) string
	}{
		{
			operationID: "connect-fleet-workspace-terminal",
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/terminal",
			targetPath: func(r *http.Request) string {
				return "/ws/v1/workspaces/" + escapePath(r.PathValue("id")) + "/terminal"
			},
		},
		{
			operationID: "connect-fleet-workspace-runtime-session-terminal",
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions/{session_key}/terminal",
			targetPath: func(r *http.Request) string {
				return "/ws/v1/workspaces/" + escapePath(r.PathValue("id")) +
					"/runtime/sessions/" + escapePath(r.PathValue("session_key")) + "/terminal"
			},
		},
	}

	for _, route := range routes {
		op := &huma.Operation{
			OperationID: route.operationID,
			Method:      http.MethodGet,
			Path:        route.path,
			Hidden:      true,
		}
		api.Adapter().Handle(op, func(ctx huma.Context) {
			r, w := humago.Unwrap(ctx)
			s.serveFleetWebSocketProxy(w, r, route.targetPath(r))
		})
	}
}

func fleetProxyParams(pathParams []string, queryParams ...*huma.Param) []*huma.Param {
	params := make([]*huma.Param, 0, len(pathParams)+len(queryParams))
	for _, name := range pathParams {
		params = append(params, &huma.Param{
			Name:     name,
			In:       "path",
			Required: true,
			Schema:   fleetStringSchema(),
		})
	}
	params = append(params, queryParams...)
	return params
}

func fleetFilesystemPathQueryParam(description string) *huma.Param {
	return &huma.Param{
		Name:        "path",
		In:          "query",
		Description: description,
		Required:    true,
		Schema:      fleetStringSchema(),
	}
}

func fleetForceQueryParam() *huma.Param {
	return &huma.Param{
		Name:        "force",
		In:          "query",
		Description: "Forward force deletion to the owning host.",
		Schema: &huma.Schema{
			Type: "boolean",
		},
	}
}

func fleetStringSchema() *huma.Schema {
	return &huma.Schema{Type: "string"}
}

func fleetProxyRequestBody() *huma.RequestBody {
	return &huma.RequestBody{
		Description: "JSON payload forwarded to the owning host.",
		Required:    true,
		Content: map[string]*huma.MediaType{
			"application/json": {
				Schema: &huma.Schema{
					Type:                 "object",
					AdditionalProperties: true,
				},
			},
		},
	}
}

func fleetProxyResponses() map[string]*huma.Response {
	return map[string]*huma.Response{
		"default": {
			Description: "Response returned by the owning fleet host.",
			Content: map[string]*huma.MediaType{
				"application/json": {
					Schema: &huma.Schema{
						Type:                 "object",
						AdditionalProperties: true,
					},
				},
				"application/problem+json": {
					Schema: &huma.Schema{
						Ref: "#/components/schemas/ProblemError",
					},
				},
			},
		},
	}
}

func (s *Server) serveFleetRESTProxy(
	w http.ResponseWriter,
	r *http.Request,
	targetPath string,
) {
	target, ok := s.resolveFleetHostTarget(r.PathValue("host_key"))
	if !ok {
		writeProblemResponse(w, fleetHostNotFoundProblem(r.PathValue("host_key")))
		return
	}
	if target.self {
		s.serveLocalFleetRESTProxy(w, r, targetPath)
		return
	}
	if target.sshPeer != nil {
		s.serveSSHFleetRESTProxy(w, r, *target.sshPeer, targetPath)
		return
	}
	s.serveRemoteFleetRESTProxy(w, r, target.peer, targetPath)
}

func (s *Server) serveLocalFleetRESTProxy(
	w http.ResponseWriter,
	r *http.Request,
	targetPath string,
) {
	if s.handler == nil {
		writeProblemResponse(w, newProblem(
			http.StatusServiceUnavailable,
			CodeServiceUnavailable,
			"server handler not configured",
			nil,
		))
		return
	}
	proxyReq := cloneRequestForLocalPath(r, s.localProxyPath(targetPath))
	s.handler.ServeHTTP(w, proxyReq)
}

func (s *Server) serveRemoteFleetRESTProxy(
	w http.ResponseWriter,
	r *http.Request,
	peer config.FleetPeer,
	targetPath string,
) {
	req, err := http.NewRequestWithContext(
		r.Context(),
		r.Method,
		remoteHTTPURL(peer.BaseURL, targetPath, r.URL.RawQuery),
		r.Body,
	)
	if err != nil {
		writeProblemResponse(w, newProblem(
			http.StatusBadGateway,
			CodeUpstreamError,
			"build fleet peer request: "+err.Error(),
			map[string]any{"hostKey": peer.Key},
		))
		return
	}
	copyProxyRequestHeaders(req.Header, r.Header)
	req.Header.Set("X-Middleman-Fleet-Host", peer.Key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeProblemResponse(w, newProblem(
			http.StatusBadGateway,
			CodeUpstreamError,
			"fleet peer request failed: "+err.Error(),
			map[string]any{"hostKey": peer.Key},
		))
		return
	}
	defer resp.Body.Close()

	copyProxyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		slog.Debug(
			"copy fleet peer response",
			"host_key", peer.Key,
			"target", targetPath,
			"err", err,
		)
	}
}

func (s *Server) serveFleetWebSocketProxy(
	w http.ResponseWriter,
	r *http.Request,
	targetPath string,
) {
	target, ok := s.resolveFleetHostTarget(r.PathValue("host_key"))
	if !ok {
		writeProblemResponse(w, fleetHostNotFoundProblem(r.PathValue("host_key")))
		return
	}
	if target.self {
		if s.handler == nil {
			writeProblemResponse(w, newProblem(
				http.StatusServiceUnavailable,
				CodeServiceUnavailable,
				"server handler not configured",
				nil,
			))
			return
		}
		proxyReq := cloneRequestForLocalPath(r, s.localProxyPath(targetPath))
		s.handler.ServeHTTP(w, proxyReq)
		return
	}

	if target.sshPeer != nil {
		// Terminals for ssh peers attach natively through the peer's
		// ControlMaster (the attach-spec carries the wrapped ssh
		// command); the hub does not bridge them over WebSocket.
		writeProblemResponse(w, newProblem(
			http.StatusNotImplemented,
			CodeUnsupportedCapability,
			"WebSocket terminals are not supported for ssh fleet hosts;"+
				" fetch the session's attach-spec and run the returned"+
				" command instead",
			map[string]any{"hostKey": target.sshPeer.Key},
		))
		return
	}

	peerURL := remoteWebSocketURL(target.peer.BaseURL, targetPath, r.URL.RawQuery)
	dialHeader := make(http.Header)
	copyProxyWebSocketRequestHeaders(dialHeader, r.Header)
	dialHeader.Set("X-Middleman-Fleet-Host", target.peer.Key)
	peerConn, _, err := websocket.Dial(r.Context(), peerURL, &websocket.DialOptions{
		HTTPHeader: dialHeader,
	})
	if err != nil {
		writeProblemResponse(w, newProblem(
			http.StatusBadGateway,
			CodeUpstreamError,
			"fleet peer websocket failed: "+err.Error(),
			map[string]any{"hostKey": target.peer.Key},
		))
		return
	}
	defer peerConn.Close(websocket.StatusNormalClosure, "hub detached")

	clientConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Debug(
			"fleet websocket accept failed",
			"host_key", target.peer.Key,
			"err", err,
		)
		return
	}
	defer clientConn.Close(websocket.StatusNormalClosure, "hub detached")

	bridgeWebSocketProxy(r.Context(), clientConn, peerConn)
}

func bridgeWebSocketProxy(ctx context.Context, client, peer *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go proxyWebSocketMessages(ctx, &wg, client, peer, cancel)
	go proxyWebSocketMessages(ctx, &wg, peer, client, cancel)
	wg.Wait()
}

func proxyWebSocketMessages(
	ctx context.Context,
	wg *sync.WaitGroup,
	from *websocket.Conn,
	to *websocket.Conn,
	cancel context.CancelFunc,
) {
	defer wg.Done()
	defer cancel()
	for {
		typ, data, err := from.Read(ctx)
		if err != nil {
			return
		}
		if err := to.Write(ctx, typ, data); err != nil {
			return
		}
	}
}

// fleetSelfHostAlias is a reserved host-key path segment that always
// addresses the local host: a daemon with no fleet.key (anonymous,
// hostname-derived self key) cannot put an empty key in a
// /fleet/hosts/{host_key}/... path, so clients route local operations
// through this alias instead. A configured peer key takes precedence
// over the alias.
const fleetSelfHostAlias = "self"

func (s *Server) resolveFleetHostTarget(hostKey string) (fleetHostTarget, bool) {
	hostKey = strings.TrimSpace(hostKey)
	if hostKey == "" {
		return fleetHostTarget{}, false
	}
	if hostKey == s.fleetSelfKey("") {
		return fleetHostTarget{self: true}, true
	}
	if s.cfg != nil {
		for _, peer := range s.cfg.Fleet.Peers {
			if peer.Key == hostKey {
				return fleetHostTarget{peer: peer}, true
			}
		}
	}
	if s.sshFleet != nil {
		if peer, ok := s.sshFleet.peer(hostKey); ok {
			return fleetHostTarget{sshPeer: &peer}, true
		}
	}
	if hostKey == fleetSelfHostAlias {
		return fleetHostTarget{self: true}, true
	}
	return fleetHostTarget{}, false
}

func (s *Server) fleetSelfKey(localHostname string) string {
	if s.cfg != nil && strings.TrimSpace(s.cfg.Fleet.Key) != "" {
		return strings.TrimSpace(s.cfg.Fleet.Key)
	}
	if strings.TrimSpace(localHostname) != "" {
		return strings.TrimSpace(localHostname)
	}
	return hostnameOrEmpty()
}

func (s *Server) localProxyPath(targetPath string) string {
	if s.basePath == "" || s.basePath == "/" {
		return targetPath
	}
	return strings.TrimSuffix(s.basePath, "/") + targetPath
}

func cloneRequestForLocalPath(r *http.Request, targetPath string) *http.Request {
	proxyReq := r.Clone(r.Context())
	u := *r.URL
	if path, err := url.PathUnescape(targetPath); err == nil {
		u.Path = path
		if path == targetPath {
			u.RawPath = ""
		} else {
			u.RawPath = targetPath
		}
	} else {
		u.Path = targetPath
		u.RawPath = ""
	}
	proxyReq.URL = &u
	proxyReq.RequestURI = ""
	return proxyReq
}

func remoteHTTPURL(baseURL, targetPath, rawQuery string) string {
	return strings.TrimRight(baseURL, "/") + targetPath + querySuffix(rawQuery)
}

func remoteWebSocketURL(baseURL, targetPath, rawQuery string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return remoteHTTPURL(baseURL, targetPath, rawQuery)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + targetPath
	u.RawPath = ""
	u.RawQuery = rawQuery
	return u.String()
}

func querySuffix(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	return "?" + rawQuery
}

func escapePath(value string) string {
	return url.PathEscape(value)
}

func copyProxyRequestHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHopHeader(key) || isPeerProxyClientHeader(key) ||
			isPeerProxyCredentialHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func copyProxyWebSocketRequestHeaders(dst, src http.Header) {
	for key, values := range src {
		lower := strings.ToLower(key)
		if isHopByHopHeader(key) || isPeerProxyClientHeader(key) ||
			isPeerProxyCredentialHeader(key) ||
			strings.HasPrefix(lower, "sec-websocket-") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// isPeerProxyCredentialHeader reports whether key carries the caller's
// credentials, which must never ride along on a server-to-server fleet proxy
// request. The hub's Authorization bearer and session cookie (including
// middleman_auth) authenticate the *hub*, not the peer: each daemon mints its
// own token, so forwarding them cannot authenticate to the peer and would only
// leak the hub credential to it. HTTP fleet peers are therefore credential-free
// and must sit behind a trusted transport boundary; SSH peers are the
// authenticated peer path.
func isPeerProxyCredentialHeader(key string) bool {
	switch strings.ToLower(key) {
	case "authorization", "cookie":
		return true
	default:
		return false
	}
}

// isPeerProxyClientHeader reports whether key is client/proxy metadata that
// must not ride along on a server-to-server fleet proxy request. The hub fans
// out on behalf of a browser and may sit behind a reverse proxy, so inbound
// Origin, Sec-Fetch-*, Forwarded, and X-Forwarded-* metadata describe the hub
// request, not the peer request. Peers validate those values when host-
// authority protection or trust_reverse_proxy is enabled.
func isPeerProxyClientHeader(key string) bool {
	lower := strings.ToLower(key)
	return lower == "origin" ||
		lower == "forwarded" ||
		strings.HasPrefix(lower, "sec-fetch-") ||
		strings.HasPrefix(lower, "x-forwarded-")
}

func copyProxyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func isHopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection",
		"keep-alive",
		"proxy-authenticate",
		"proxy-authorization",
		"te",
		"trailer",
		"transfer-encoding",
		"upgrade":
		return true
	default:
		return false
	}
}

func fleetHostNotFoundProblem(hostKey string) *ProblemError {
	return newProblem(
		http.StatusNotFound,
		CodeNotFound,
		"fleet host not found",
		map[string]any{"hostKey": hostKey},
	)
}

func writeProblemResponse(w http.ResponseWriter, problem *ProblemError) {
	if problem == nil {
		problem = newProblem(
			http.StatusInternalServerError,
			CodeInternalError,
			"internal error",
			nil,
		)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problem.Status)
	_ = json.NewEncoder(w).Encode(problem)
}
