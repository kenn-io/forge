package fleetapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty/v2"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/tracing"
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

func (s *Handler) registerFleetOperationRoutes(api huma.API) {
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
			operationID: "push-fleet-workspace-branch",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/push",
			summary:     "Push workspace branch on fleet host",
			pathParams:  []string{"host_key", "id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) + "/push"
			},
		},
		{
			operationID: "pull-fleet-workspace-branch",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/pull",
			summary:     "Pull workspace branch on fleet host",
			pathParams:  []string{"host_key", "id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) + "/pull"
			},
		},
		{
			operationID: "reveal-fleet-workspace",
			method:      http.MethodPost,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/reveal",
			summary:     "Reveal workspace folder on fleet host",
			pathParams:  []string{"host_key", "id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) + "/reveal"
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
			operationID: "get-fleet-workspace-commits",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/commits",
			summary:     "Get workspace commits on fleet host",
			pathParams:  []string{"host_key", "id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) + "/commits"
			},
		},
		{
			operationID: "get-fleet-workspace-diff",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/diff",
			summary:     "Get workspace diff on fleet host",
			pathParams:  []string{"host_key", "id"},
			queryParams: fleetWorkspaceDiffQueryParams(),
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) + "/diff"
			},
		},
		{
			operationID: "get-fleet-workspace-file-preview",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/file-preview",
			summary:     "Get workspace file preview on fleet host",
			pathParams:  []string{"host_key", "id"},
			queryParams: fleetWorkspaceFilePreviewQueryParams(),
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) + "/file-preview"
			},
		},
		{
			operationID: "get-fleet-workspace-files",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/files",
			summary:     "Get workspace files on fleet host",
			pathParams:  []string{"host_key", "id"},
			queryParams: fleetWorkspaceDiffScopeQueryParams(),
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) + "/files"
			},
		},
		{
			operationID: "watch-fleet-workspace-diff",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/workspaces/{id}/diff/watch",
			summary:     "Watch selected workspace diff on fleet host",
			pathParams:  []string{"host_key", "id"},
			queryParams: []*huma.Param{
				fleetStringQueryParam("version", "Last observed workspace diff snapshot version."),
			},
			targetPath: func(r *http.Request) string {
				return "/api/v1/workspaces/" + escapePath(r.PathValue("id")) + "/diff/watch"
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
			operationID: "get-fleet-project",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}",
			summary:     "Get project on fleet host",
			pathParams:  []string{"host_key", "project_id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id"))
			},
		},
		{
			operationID: "list-fleet-project-worktrees",
			method:      http.MethodGet,
			path:        "/fleet/hosts/{host_key}/projects/{project_id}/worktrees",
			summary:     "List project worktrees on fleet host",
			pathParams:  []string{"host_key", "project_id"},
			targetPath: func(r *http.Request) string {
				return "/api/v1/projects/" + escapePath(r.PathValue("project_id")) +
					"/worktrees"
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

// RegisterTerminal registers hidden Fleet websocket proxy routes.
func (s *Handler) RegisterTerminal(api huma.API) {
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

func fleetWorkspaceDiffQueryParams() []*huma.Param {
	return append(
		fleetWorkspaceDiffScopeQueryParams(),
		fleetStringQueryParam("revision", "Pinned workspace snapshot version."),
	)
}

func fleetWorkspaceDiffScopeQueryParams() []*huma.Param {
	return []*huma.Param{
		fleetStringQueryParam("base", "Workspace diff base."),
		fleetStringQueryParam("whitespace", "Whitespace filtering mode."),
		fleetStringQueryParam("commit", "Commit SHA scope."),
		fleetStringQueryParam("from", "Older range commit SHA."),
		fleetStringQueryParam("to", "Newer range commit SHA."),
	}
}

func fleetWorkspaceFilePreviewQueryParams() []*huma.Param {
	return append(
		fleetWorkspaceDiffQueryParams(),
		fleetStringQueryParam("path", "Workspace file path to preview."),
		fleetStringQueryParam("side", "Preview side."),
	)
}

func fleetStringQueryParam(name, description string) *huma.Param {
	return &huma.Param{
		Name:        name,
		In:          "query",
		Description: description,
		Schema:      fleetStringSchema(),
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

func (s *Handler) serveFleetRESTProxy(
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

func (s *Handler) serveLocalFleetRESTProxy(
	w http.ResponseWriter,
	r *http.Request,
	targetPath string,
) {
	if s.localHandler == nil || s.localHandler() == nil {
		writeProblemResponse(w, httpapi.NewProblem(
			http.StatusServiceUnavailable,
			httpapi.CodeServiceUnavailable,
			"server handler not configured",
			nil,
		))
		return
	}
	proxyReq := cloneRequestForLocalPath(r, s.localProxyPath(targetPath))
	s.localHandler().ServeHTTP(w, proxyReq)
}

func (s *Handler) serveRemoteFleetRESTProxy(
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
		writeProblemResponse(w, httpapi.NewProblem(
			http.StatusBadGateway,
			httpapi.CodeUpstreamError,
			"build fleet peer request: "+err.Error(),
			map[string]any{"hostKey": peer.Key},
		))
		return
	}
	copyProxyRequestHeaders(req.Header, r.Header)
	req.Header.Set("X-Kenn-Forge-Fleet-Host", peer.Key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeProblemResponse(w, httpapi.NewProblem(
			http.StatusBadGateway,
			httpapi.CodeUpstreamError,
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

func (s *Handler) serveFleetWebSocketProxy(
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
		if s.localHandler == nil || s.localHandler() == nil {
			writeProblemResponse(w, httpapi.NewProblem(
				http.StatusServiceUnavailable,
				httpapi.CodeServiceUnavailable,
				"server handler not configured",
				nil,
			))
			return
		}
		proxyReq := cloneRequestForLocalPath(r, s.localProxyPath(targetPath))
		s.localHandler().ServeHTTP(w, proxyReq)
		return
	}

	if target.sshPeer != nil {
		s.serveSSHFleetWebSocketTerminal(w, r, *target.sshPeer, targetPath)
		return
	}
	r, attachSpan, endAttachSpan := startFleetAttachSpan(r)
	defer endAttachSpan()

	peerURL := remoteWebSocketURL(target.peer.BaseURL, targetPath, r.URL.RawQuery)
	dialHeader := make(http.Header)
	copyProxyWebSocketRequestHeaders(dialHeader, r.Header)
	dialHeader.Set("X-Kenn-Forge-Fleet-Host", target.peer.Key)
	peerConn, _, err := websocket.Dial(r.Context(), peerURL, &websocket.DialOptions{
		HTTPHeader: dialHeader,
	})
	if err != nil {
		attachSpan.SetAttributes(attribute.Bool("error", true))
		writeProblemResponse(w, httpapi.NewProblem(
			http.StatusBadGateway,
			httpapi.CodeUpstreamError,
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
		attachSpan.SetAttributes(attribute.Bool("error", true))
		slog.Debug(
			"fleet websocket accept failed",
			"host_key", target.peer.Key,
			"err", err,
		)
		return
	}
	defer clientConn.Close(websocket.StatusNormalClosure, "hub detached")

	endAttachSpan()
	bridgeWebSocketProxy(r.Context(), clientConn, peerConn)
}

func startFleetAttachSpan(r *http.Request) (*http.Request, trace.Span, func()) {
	ctx, attachSpan := tracing.StartAttachSpan(r, "terminal.attach")
	endAttachSpan := sync.OnceFunc(func() { attachSpan.End() })
	return r.WithContext(ctx), attachSpan, endAttachSpan
}

func (s *Handler) serveSSHFleetWebSocketTerminal(
	w http.ResponseWriter,
	r *http.Request,
	peer config.FleetSSHPeer,
	targetPath string,
) {
	r, attachSpan, endAttachSpan := startFleetAttachSpan(r)
	defer endAttachSpan()

	attachSpecPath, ok := attachSpecPathForFleetTerminalTarget(targetPath)
	if !ok {
		attachSpan.SetAttributes(attribute.Bool("error", true))
		writeProblemResponse(w, httpapi.NewProblem(
			http.StatusNotImplemented,
			httpapi.CodeUnsupportedCapability,
			"workspace-level WebSocket terminals are not supported for ssh fleet hosts; use a runtime session terminal",
			map[string]any{"hostKey": peer.Key},
		))
		return
	}
	if s.sshFleet == nil {
		attachSpan.SetAttributes(attribute.Bool("error", true))
		writeProblemResponse(w, fleetHostNotFoundProblem(peer.Key))
		return
	}

	result, err := s.sshFleet.relay(
		r.Context(), peer, http.MethodGet, attachSpecPath, nil,
	)
	if err != nil {
		attachSpan.SetAttributes(attribute.Bool("error", true))
		writeProblemResponse(w, httpapi.NewProblem(
			http.StatusBadGateway,
			httpapi.CodeUpstreamError,
			"fleet ssh relay failed: "+err.Error(),
			map[string]any{"hostKey": peer.Key},
		))
		return
	}
	resp := result.response
	out := resp.Body
	if resp.Status/100 == 2 {
		command, commandErr := s.sshFleet.runner.SSHCommand(
			result.connection, true,
		)
		if commandErr != nil {
			attachSpan.SetAttributes(attribute.Bool("error", true))
			writeProblemResponse(w, httpapi.NewProblem(
				http.StatusBadGateway,
				httpapi.CodeUpstreamError,
				"fleet ssh connection changed: "+commandErr.Error(),
				map[string]any{"hostKey": peer.Key},
			))
			return
		}
		if wrapped, ok := wrapAttachSpecForSSH(out, command); ok {
			out = wrapped
		}
	}
	if resp.Status/100 != 2 {
		attachSpan.SetAttributes(attribute.Bool("error", true))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.Status)
		_, _ = w.Write(out)
		return
	}

	var spec workspaceapi.RuntimeAttachSpecResponse
	if err := json.Unmarshal(out, &spec); err != nil {
		attachSpan.SetAttributes(attribute.Bool("error", true))
		writeProblemResponse(w, httpapi.NewProblem(
			http.StatusBadGateway,
			httpapi.CodeUpstreamError,
			"fleet ssh attach-spec was invalid: "+err.Error(),
			map[string]any{"hostKey": peer.Key},
		))
		return
	}
	attach, err := startFleetSSHAttachPTY(r.Context(), spec, r)
	if err != nil {
		attachSpan.SetAttributes(attribute.Bool("error", true))
		writeProblemResponse(w, httpapi.NewProblem(
			http.StatusBadGateway,
			httpapi.CodeUpstreamError,
			"start fleet ssh terminal attach: "+err.Error(),
			map[string]any{"hostKey": peer.Key},
		))
		return
	}

	clientConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		attachSpan.SetAttributes(attribute.Bool("error", true))
		attach.close()
		slog.Debug(
			"fleet ssh websocket accept failed",
			"host_key", peer.Key,
			"err", err,
		)
		return
	}
	defer clientConn.Close(websocket.StatusNormalClosure, "hub detached")
	endAttachSpan()
	cols, rows, sizeOK := workspaceapi.ParseRuntimeTerminalSize(r)
	if !sizeOK {
		cols, rows = 120, 30
	}
	resizeMember := s.registerFleetSSHResizeMember(
		peer.Key+"\x00"+targetPath,
		attach,
		workspaceapi.ParseRuntimeTerminalResizeActive(r),
		cols,
		rows,
	)
	defer resizeMember.close()
	bridgeFleetSSHAttachPTY(r.Context(), clientConn, attach, resizeMember)
}

func attachSpecPathForFleetTerminalTarget(targetPath string) (string, bool) {
	const wsPrefix = "/ws/v1/"
	if !strings.HasPrefix(targetPath, wsPrefix) ||
		!strings.HasSuffix(targetPath, "/terminal") ||
		!strings.Contains(targetPath, "/runtime/sessions/") {
		return "", false
	}
	path := "/api/v1/" + strings.TrimPrefix(targetPath, wsPrefix)
	path = strings.TrimSuffix(path, "/terminal") + "/attach-spec"
	return path, true
}

type fleetSSHPTYAttachment struct {
	cmd  *os.Process
	ptmx *os.File
	done <-chan int
}

func startFleetSSHAttachPTY(
	ctx context.Context,
	spec workspaceapi.RuntimeAttachSpecResponse,
	r *http.Request,
) (*fleetSSHPTYAttachment, error) {
	if len(spec.Command) == 0 || strings.TrimSpace(spec.Command[0]) == "" {
		return nil, errors.New("attach-spec command is empty")
	}
	active := workspaceapi.ParseRuntimeTerminalResizeActive(r)
	cols, rows, ok := workspaceapi.ParseRuntimeTerminalSize(r)
	if !ok || !active {
		cols, rows = 120, 30
	}
	release, err := procutil.TryAcquire(ctx, "fleet ssh terminal attach")
	if err != nil {
		return nil, err
	}

	cmd := procutil.Command(spec.Command[0], spec.Command[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: workspaceapi.ClampTerminalDim(cols),
		Rows: workspaceapi.ClampTerminalDim(rows),
	})
	if err != nil {
		release()
		return nil, fmt.Errorf("start attach pty: %w", err)
	}
	done := make(chan int, 1)
	go func() {
		err := cmd.Wait()
		release()
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		} else if err == nil {
			code = 0
		}
		done <- code
		close(done)
	}()
	return &fleetSSHPTYAttachment{
		cmd:  cmd.Process,
		ptmx: ptmx,
		done: done,
	}, nil
}

func (a *fleetSSHPTYAttachment) close() {
	if a == nil {
		return
	}
	if a.ptmx != nil {
		_ = a.ptmx.Close()
	}
	if a.cmd != nil {
		_ = a.cmd.Kill()
	}
}

func (a *fleetSSHPTYAttachment) resize(cols, rows int) {
	if a == nil || cols <= 0 || rows <= 0 {
		return
	}
	_ = pty.Setsize(a.ptmx, &pty.Winsize{
		Cols: workspaceapi.ClampTerminalDim(cols),
		Rows: workspaceapi.ClampTerminalDim(rows),
	})
}

type fleetSSHResizeGroup struct {
	mu        sync.Mutex
	nextID    uint64
	nextClaim uint64
	ownerID   uint64
	members   map[uint64]*fleetSSHResizeMember
}

type fleetSSHResizeMember struct {
	group      *fleetSSHResizeGroup
	id         uint64
	attachment *fleetSSHPTYAttachment
	active     bool
	claim      uint64
	cols       int
	rows       int
	hasSize    bool
	unregister func() bool
}

func (s *Handler) registerFleetSSHResizeMember(
	key string,
	attachment *fleetSSHPTYAttachment,
	active bool,
	cols int,
	rows int,
) *fleetSSHResizeMember {
	s.sshResizeMu.Lock()
	defer s.sshResizeMu.Unlock()
	if s.sshResizeGroups == nil {
		s.sshResizeGroups = make(map[string]*fleetSSHResizeGroup)
	}
	group := s.sshResizeGroups[key]
	if group == nil {
		group = &fleetSSHResizeGroup{}
		s.sshResizeGroups[key] = group
	}
	member := group.register(attachment, active, cols, rows)
	member.unregister = func() bool {
		s.sshResizeMu.Lock()
		defer s.sshResizeMu.Unlock()
		empty := group.unregisterMember(member.id)
		if empty && s.sshResizeGroups[key] == group {
			delete(s.sshResizeGroups, key)
		}
		return empty
	}
	return member
}

func (g *fleetSSHResizeGroup) register(
	attachment *fleetSSHPTYAttachment,
	active bool,
	cols int,
	rows int,
) *fleetSSHResizeMember {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nextID++
	member := &fleetSSHResizeMember{
		group:      g,
		id:         g.nextID,
		attachment: attachment,
		active:     active,
		cols:       cols,
		rows:       rows,
		hasSize:    cols > 0 && rows > 0,
	}
	if g.members == nil {
		g.members = make(map[uint64]*fleetSSHResizeMember)
	}
	g.members[member.id] = member
	if active && g.ownerID == 0 {
		g.ownerID = member.id
	}
	owner := g.members[g.ownerID]
	if owner != nil && owner.hasSize {
		attachment.resize(owner.cols, owner.rows)
	}
	return member
}

func (m *fleetSSHResizeMember) close() {
	if m == nil || m.unregister == nil {
		return
	}
	m.unregister()
	m.unregister = nil
}

func (g *fleetSSHResizeGroup) unregisterMember(id uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.members, id)
	if g.ownerID == id {
		g.ownerID = 0
		g.selectOwnerLocked()
		g.applyOwnerSizeLocked()
	}
	return len(g.members) == 0
}

func (m *fleetSSHResizeMember) setActive(active bool) {
	if m == nil || m.group == nil {
		return
	}
	g := m.group
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.members[m.id] != m {
		return
	}
	m.active = active
	if active {
		if g.ownerID == 0 {
			g.ownerID = m.id
			g.applyOwnerSizeLocked()
		}
		return
	}
	if g.ownerID == m.id {
		g.ownerID = 0
		g.selectOwnerLocked()
		g.applyOwnerSizeLocked()
	}
}

func (m *fleetSSHResizeMember) resize(cols, rows int, claim bool) {
	if m == nil || m.group == nil || cols <= 0 || rows <= 0 {
		return
	}
	g := m.group
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.members[m.id] != m || !m.active {
		return
	}
	clampedCols := int(workspaceapi.ClampTerminalDim(cols))
	clampedRows := int(workspaceapi.ClampTerminalDim(rows))
	sizeChanged := !m.hasSize || m.cols != clampedCols || m.rows != clampedRows
	m.cols = clampedCols
	m.rows = clampedRows
	m.hasSize = true
	ownerChanged := false
	if claim {
		g.nextClaim++
		m.claim = g.nextClaim
		ownerChanged = g.ownerID != m.id
		g.ownerID = m.id
	}
	if g.ownerID == m.id && (!claim || ownerChanged || sizeChanged) {
		g.applySizeLocked(m.cols, m.rows)
	}
}

func (g *fleetSSHResizeGroup) selectOwnerLocked() {
	for id, member := range g.members {
		if !member.active {
			continue
		}
		if g.ownerID == 0 {
			g.ownerID = id
			continue
		}
		owner := g.members[g.ownerID]
		if owner == nil || member.claim > owner.claim ||
			(member.claim == owner.claim && id < g.ownerID) {
			g.ownerID = id
		}
	}
}

func (g *fleetSSHResizeGroup) applyOwnerSizeLocked() {
	owner := g.members[g.ownerID]
	if owner != nil && owner.hasSize {
		g.applySizeLocked(owner.cols, owner.rows)
	}
}

func (g *fleetSSHResizeGroup) applySizeLocked(cols, rows int) {
	for _, member := range g.members {
		member.attachment.resize(cols, rows)
	}
}

func bridgeFleetSSHAttachPTY(
	ctx context.Context,
	conn *websocket.Conn,
	attach *fleetSSHPTYAttachment,
	resizeMember *fleetSSHResizeMember,
) {
	defer attach.close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			switch typ {
			case websocket.MessageBinary:
				if _, err := attach.ptmx.Write(data); err != nil {
					return
				}
			case websocket.MessageText:
				handleFleetSSHAttachControl(resizeMember, data)
			}
		}
	}()

	outputDone := make(chan bool, 1)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := attach.ptmx.Read(buf)
			if n > 0 {
				if writeErr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); writeErr != nil {
					outputDone <- false
					return
				}
			}
			if err != nil {
				outputDone <- true
				return
			}
		}
	}()

	select {
	case code := <-attach.done:
		select {
		case <-outputDone:
		case <-time.After(100 * time.Millisecond):
		}
		writeTerminalExitFrame(conn, code)
		cancel()
	case <-inputDone:
		cancel()
	case ptyEOF := <-outputDone:
		if !ptyEOF {
			cancel()
			return
		}
		select {
		case code := <-attach.done:
			writeTerminalExitFrame(conn, code)
		case <-time.After(100 * time.Millisecond):
			writeTerminalExitFrame(conn, -1)
		}
		cancel()
	case <-ctx.Done():
	}
}

func handleFleetSSHAttachControl(member *fleetSSHResizeMember, data []byte) {
	var msg workspaceapi.RuntimeTerminalControlMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		slog.Warn("bad fleet ssh terminal control message", "err", err)
		return
	}
	switch msg.Type {
	case "refresh", "resize":
		member.resize(msg.Cols, msg.Rows, false)
	case "claim_resize":
		member.resize(msg.Cols, msg.Rows, true)
	case "resize_active":
		if msg.Active != nil {
			member.setActive(*msg.Active)
		}
	}
}

func writeTerminalExitFrame(conn *websocket.Conn, code int) {
	exitMsg, _ := json.Marshal(map[string]any{
		"type": "exited",
		"code": code,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, exitMsg)
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
// through this alias instead. Config validation reserves this key so
// configured peers cannot shadow local self routing.
const fleetSelfHostAlias = "self"

func (s *Handler) resolveFleetHostTarget(hostKey string) (fleetHostTarget, bool) {
	hostKey = strings.TrimSpace(hostKey)
	if hostKey == "" {
		return fleetHostTarget{}, false
	}
	if hostKey == s.fleetSelfKey("") {
		return fleetHostTarget{self: true}, true
	}
	fleetCfg := s.configSnapshot().Fleet
	federationEnabled := fleetCfg.Enabled
	if federationEnabled {
		for _, peer := range fleetCfg.Peers {
			if peer.Key == hostKey {
				return fleetHostTarget{peer: peer}, true
			}
		}
	}
	if federationEnabled && s.sshFleet != nil {
		if peer, ok := s.sshFleet.peer(hostKey); ok {
			return fleetHostTarget{sshPeer: &peer}, true
		}
	}
	if hostKey == fleetSelfHostAlias {
		return fleetHostTarget{self: true}, true
	}
	return fleetHostTarget{}, false
}

func (s *Handler) fleetSelfKey(localHostname string) string {
	if key := strings.TrimSpace(s.configSnapshot().Fleet.Key); key != "" {
		return key
	}
	if strings.TrimSpace(localHostname) != "" {
		return strings.TrimSpace(localHostname)
	}
	return hostnameOrEmpty()
}

func (s *Handler) localProxyPath(targetPath string) string {
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
// forge_auth) authenticate the *hub*, not the peer: each daemon mints its
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

func fleetHostNotFoundProblem(hostKey string) *httpapi.ProblemError {
	return httpapi.NewProblem(
		http.StatusNotFound,
		httpapi.CodeNotFound,
		"fleet host not found",
		map[string]any{"hostKey": hostKey},
	)
}

func writeProblemResponse(w http.ResponseWriter, problem *httpapi.ProblemError) {
	if problem == nil {
		problem = httpapi.NewProblem(
			http.StatusInternalServerError,
			httpapi.CodeInternalError,
			"internal error",
			nil,
		)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problem.Status)
	_ = json.NewEncoder(w).Encode(problem)
}
