package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisteredToolsResourcesAndPromptsAreCurated(t *testing.T) {
	s := newMCPTestServer(t, &fakeBackend{})
	cs := connectMCPTestSession(t, s)

	tools, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	var toolNames []string
	for _, tool := range tools.Tools {
		toolNames = append(toolNames, tool.Name)
	}
	slices.Sort(toolNames)
	assert.Equal(t, []string{
		"kenn_forge_find_review_candidates",
		"kenn_forge_get_item_context",
		"kenn_forge_get_item_diff",
		"kenn_forge_get_stack_context",
		"kenn_forge_list_activity",
		"kenn_forge_list_agent_targets",
		"kenn_forge_list_items_by_workflow_state",
		"kenn_forge_list_repos",
		"kenn_forge_list_workspace_agent_sessions",
		"kenn_forge_search_items",
		"kenn_forge_set_item_workflow_state",
		"kenn_forge_spawn_workspace_with_agent",
	}, toolNames)

	resources, err := cs.ListResources(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, resources.Resources, 1)
	assert.Equal(t, "kenn-forge://mcp/guidance", resources.Resources[0].URI)
	read, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "kenn-forge://mcp/guidance"})
	require.NoError(t, err)
	require.Len(t, read.Contents, 1)
	assert.Contains(t, read.Contents[0].Text, "kenn_forge_find_review_candidates")

	prompts, err := cs.ListPrompts(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, prompts.Prompts, 1)
	prompt, err := cs.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: "kenn-forge-review-candidates"})
	require.NoError(t, err)
	require.Len(t, prompt.Messages, 1)
	content, ok := prompt.Messages[0].Content.(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, content.Text, "kenn_forge_get_item_diff")
	assert.Contains(t, content.Text, "expected_status")
}

func TestServerUses20260728ProtocolCapabilities(t *testing.T) {
	s := newMCPTestServer(t, &fakeBackend{})

	initialized := connectMCPTestSession(t, s).InitializeResult()

	require.NotNil(t, initialized)
	assert.Equal(t, "2026-07-28", initialized.ProtocolVersion)
	//nolint:staticcheck // Verify the 2026-07-28 server does not advertise deprecated logging.
	assert.Nil(t, initialized.Capabilities.Logging)
	require.NotNil(t, initialized.Capabilities.Tools)
	assert.False(t, initialized.Capabilities.Tools.ListChanged)
	require.NotNil(t, initialized.Capabilities.Prompts)
	assert.False(t, initialized.Capabilities.Prompts.ListChanged)
	require.NotNil(t, initialized.Capabilities.Resources)
	assert.False(t, initialized.Capabilities.Resources.ListChanged)
	assert.False(t, initialized.Capabilities.Resources.Subscribe)
}

func TestHTTPHandlerServesOnlyStatelessMCPPath(t *testing.T) {
	s := newMCPTestServer(t, &fakeBackend{})
	handler := s.HTTPHandler()

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "http://localhost/other", nil))
	assert.Equal(t, http.StatusNotFound, missing.Code)

	mcpResponse := httptest.NewRecorder()
	handler.ServeHTTP(mcpResponse, httptest.NewRequest(http.MethodGet, "http://localhost/mcp", nil))
	assert.NotEqual(t, http.StatusNotFound, mcpResponse.Code)
}

func connectMCPTestSession(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := s.mcp.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, serverSession.Close()) })
	client := mcp.NewClient(&mcp.Implementation{Name: "kenn-forge-test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, clientSession.Close()) })
	return clientSession
}
