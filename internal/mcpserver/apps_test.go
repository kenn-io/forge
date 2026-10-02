package mcpserver

import (
	"context"
	"encoding/json/v2"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderAppUsesPortableResourceAndACPDescriptor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	session := connectMCPTestSession(t, newMCPTestServer(t, &fakeBackend{}))
	tools, err := session.ListTools(t.Context(), nil)
	require.NoError(err)
	for _, tool := range tools.Tools {
		if tool.Name == "kenn_forge_render_app" {
			assert.Equal(AppResourceURI, tool.Meta["ui"].(map[string]any)["resourceUri"])
		}
	}
	resource, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: AppResourceURI})
	require.NoError(err)
	require.Len(resource.Contents, 1)
	assert.Equal("text/html;profile=mcp-app", resource.Contents[0].MIMEType)
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "kenn_forge_render_app", Arguments: map[string]any{"title": "My component", "html": "<button>Refresh</button>"},
	})
	require.NoError(err)
	require.False(result.IsError)
	require.Len(result.Content, 1)
	descriptor, ok := result.Content[0].(*mcp.TextContent)
	require.True(ok)
	var generated renderAppOutput
	require.NoError(json.Unmarshal([]byte(descriptor.Text), &generated))
	assert.Equal(AppDescriptorKind, generated.Kind)
	assert.Equal(AppDescriptorKind, result.StructuredContent.(map[string]any)["kind"])
	assert.Equal("<button>Refresh</button>", generated.HTML)
	assert.Equal("My component", generated.Title)
}

func TestAppReadsCurrentCacheAndRejectsMutations(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	count := 1
	backend := &fakeBackend{listRepositoriesFn: func(context.Context) ([]RepositorySummary, error) {
		return []RepositorySummary{{OpenPRCount: count}}, nil
	}}
	for _, wanted := range []int{1, 2} {
		count = wanted
		result, err := CallAppTool(t.Context(), backend, "kenn_forge_list_repos", map[string]any{})
		require.NoError(err)
		require.False(result.IsError)
		data, err := json.Marshal(result.StructuredContent)
		require.NoError(err)
		var output listReposOutput
		require.NoError(json.Unmarshal(data, &output))
		require.Len(output.Repos, 1)
		assert.Equal(wanted, output.Repos[0].OpenPRCount)
	}
	for _, name := range []string{"kenn_forge_set_item_workflow_state", "kenn_forge_spawn_workspace_with_agent", "kenn_forge_get_item_diff", "kenn_forge_render_app"} {
		_, err := CallAppTool(t.Context(), backend, name, map[string]any{})
		var denied *Error
		require.ErrorAs(err, &denied)
		assert.Equal("forbidden", denied.Kind)
	}
}

func TestAppToolKeepsTypedCacheFailure(t *testing.T) {
	backend := &fakeBackend{listRepositoriesFn: func(context.Context) ([]RepositorySummary, error) {
		return nil, &Error{Kind: "unavailable", Code: "hubUnavailable", Message: "hub unavailable", Retryable: true}
	}}
	result, err := CallAppTool(t.Context(), backend, "kenn_forge_list_repos", map[string]any{})
	require.NoError(t, err)
	require.True(t, result.IsError)
	assert.Contains(t, result.Meta, toolErrorMetaKey)
}
