package mcpserver

import (
	"context"
	_ "embed"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const AppResourceURI = "ui://kenn-forge/generated-app"

// AppDescriptorURI identifies the first-party ACP handoff, not an MCP Apps
// resource. Portable MCP Apps hosts discover AppResourceURI from tool metadata.
const AppDescriptorURI = "kenn-forge://apps/generated"

//go:embed app.html
var AppHTML string

type renderAppInput struct {
	Title string `json:"title" jsonschema:"short title for the generated component"`
	HTML  string `json:"html" jsonschema:"self-contained HTML fragment with inline style and script; use window.app.callServerTool({name,arguments}) for cached Forge reads and window.app.openLink({url}) for links; no imports or external network"`
}

func (s *Server) registerAppTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_render_app",
		Description: "Render a custom interactive component you author in the chat. Compose dashboards, tables or other views with HTML, inline CSS and JavaScript. " +
			"window.app.callServerTool({name,arguments}) returns MCP results with structuredContent and isError. " +
			"Use kenn_forge_list_repos then kenn_forge_list_pull_contexts for cached PR data, preserving stable repository IDs and pagination. " +
			"The app may call cached read tools only. Refresh rereads Forge's cache, not providers. Show cache freshness; mergeability is not permission to merge. " +
			"No external scripts, imports, network, forms or provider writes. Use DOM textContent for data. " +
			"window.app.openLink({url}) asks the host to open an HTTP(S) link. Scripts run after the app bridge is ready. " +
			"Include a useful text explanation for clients without widgets. Prototype: generated components are retained in the chat transcript.",
		Meta: mcp.Meta{"ui": map[string]any{"resourceUri": AppResourceURI, "visibility": []string{"model"}}},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in renderAppInput) (*mcp.CallToolResult, renderAppInput, error) {
		if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.HTML) == "" {
			return nil, in, errors.New("title and html are required")
		}
		if len(in.HTML) > 256<<10 || len(in.Title) > 200 {
			return nil, in, errors.New("app exceeds the 256 KiB HTML or 200-byte title limit")
		}
		data, err := json.Marshal(in)
		if err != nil {
			return nil, in, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: "Generated interactive component: " + in.Title},
			&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{
				URI: AppDescriptorURI, MIMEType: "application/json", Text: string(data),
			}},
		}}, in, nil
	})
	s.mcp.AddResource(&mcp.Resource{
		URI: AppResourceURI, Name: "kenn-forge-generated-app", Title: "Generated component renderer",
		MIMEType: "text/html;profile=mcp-app",
	}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: AppResourceURI, MIMEType: "text/html;profile=mcp-app", Text: AppHTML,
			Meta: mcp.Meta{"ui": map[string]any{"csp": map[string]any{}, "prefersBorder": true}},
		}}}, nil
	})
}

// CallAppTool uses the same tool validation, error envelope and federated cache
// backend as agent calls. The allowlist is independent of model-supplied HTML.
func CallAppTool(ctx context.Context, backend Backend, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	switch name {
	case "kenn_forge_list_repos", "kenn_forge_list_pull_contexts", "kenn_forge_get_item_context",
		"kenn_forge_list_activity", "kenn_forge_search_items", "kenn_forge_find_review_candidates",
		"kenn_forge_get_stack_context", "kenn_forge_list_items_by_workflow_state":
	default:
		return nil, &Error{Kind: "forbidden", Message: "generated apps may only call cached Forge read tools"}
	}
	s, err := New(Options{Backend: backend, Version: "app-bridge"})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := s.mcp.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect app server: %w", err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "kenn-forge-app-host", Version: "prototype"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect app client: %w", err)
	}
	defer session.Close()
	return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
}
