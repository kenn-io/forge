package server

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/httpapi"
)

type appResourceOutput struct {
	Body struct {
		HTML string `json:"html"`
	}
}

type appToolInput struct {
	Body struct {
		Name      string         `json:"name" minLength:"1"`
		Arguments appToolPayload `json:"arguments"`
	}
}

type appToolPayload map[string]any

func (appToolPayload) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: "object", AdditionalProperties: true}
}

// The result follows the MCP schema, including heterogeneous content blocks.
// Keep that external protocol payload intact rather than duplicating it here.
type appToolOutput struct {
	Body appToolPayload
}

func (s *Server) registerMCPAppsAPI(api huma.API) {
	huma.Get(api, "/mcp-apps/resource", func(_ context.Context, _ *struct{}) (*appResourceOutput, error) {
		out := &appResourceOutput{}
		out.Body.HTML = mcpserver.AppHTML
		return out, nil
	}, httpapi.DocumentOperation("get-mcp-app-resource", "Get the generated MCP App renderer", "MCP Apps"))
	huma.Post(api, "/mcp-apps/call-tool", s.callAppTool,
		httpapi.DocumentOperation("call-mcp-app-tool", "Read cached Forge data for a generated component", "MCP Apps"))
}

func (s *Server) callAppTool(ctx context.Context, in *appToolInput) (*appToolOutput, error) {
	result, err := mcpserver.CallAppTool(ctx, s.MCPBackend(), in.Body.Name, in.Body.Arguments)
	if err != nil {
		if typed, ok := errors.AsType[*mcpserver.Error](err); ok && typed.Kind == "forbidden" {
			return nil, httpapi.NewProblem(http.StatusForbidden, httpapi.CodeForbidden, typed.Message, nil)
		}
		return nil, httpapi.BadRequest(httpapi.CodeValidationError, "App tool request failed: "+err.Error(), nil)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, httpapi.NewProblem(http.StatusInternalServerError, httpapi.CodeInternalError, "Encode app tool result: "+err.Error(), nil)
	}
	out := &appToolOutput{}
	if err := json.Unmarshal(data, &out.Body); err != nil {
		return nil, httpapi.NewProblem(http.StatusInternalServerError, httpapi.CodeInternalError, "Encode app tool result: "+err.Error(), nil)
	}
	return out, nil
}
