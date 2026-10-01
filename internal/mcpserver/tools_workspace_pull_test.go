package mcpserver

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspacePullLinkUsesACPScopeOverHTTP(t *testing.T) {
	assert := assert.New(t)
	calls := 0
	s := newMCPTestServer(t, &fakeBackend{linkWorkspacePullFn: func(_ context.Context, id string, number int, url string) (bool, error) {
		calls++
		assert.Equal("ws-chat", id)
		assert.Equal(42, number)
		assert.Equal("https://github.com/acme/widget/pull/42", url)
		return true, nil
	}})
	handler := s.HTTPHandler()
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Kenn-Forge-Workspace-ID", "ws-chat")
		handler.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint.URL + "/mcp", DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	defer session.Close()
	response, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_link_workspace_pull_request", Arguments: map[string]any{
		"number": 42, "url": "https://github.com/acme/widget/pull/42",
	}})
	require.NoError(t, err)
	require.False(t, response.IsError)
	var result linkWorkspacePullOutput
	require.NoError(t, json.Unmarshal([]byte(response.Content[0].(*mcp.TextContent).Text), &result))
	assert.True(result.AlreadyLinked)
	assert.Equal("ws-chat", result.WorkspaceID)
	rejected, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_link_workspace_pull_request", Arguments: map[string]any{
		"workspace_id": "ws-other", "number": 42, "url": "https://github.com/acme/widget/pull/42",
	}})
	require.NoError(t, err)
	assert.True(rejected.IsError)
	assert.Equal(1, calls)
}

func TestWorkspacePullLinkAcceptsTerminalWorkspaceID(t *testing.T) {
	called := false
	s := newMCPTestServer(t, &fakeBackend{linkWorkspacePullFn: func(_ context.Context, id string, _ int, _ string) (bool, error) {
		called = true
		assert.Equal(t, "ws-terminal", id)
		return false, nil
	}})
	client := connectMCPTestSession(t, s)
	response, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_link_workspace_pull_request", Arguments: map[string]any{
		"workspace_id": "ws-terminal", "number": 42, "url": "https://github.com/acme/widget/pull/42",
	}})
	require.NoError(t, err)
	assert.False(t, response.IsError)
	assert.True(t, called)
}
