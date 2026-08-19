package mcpserver

import (
	"context"
	"encoding/json"
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

func TestToolErrorsPreserveStructuredEvidenceThroughClientSession(t *testing.T) {
	backend := &fakeBackend{listRepositoriesFn: func(context.Context) ([]RepositorySummary, error) {
		return nil, &Error{
			Kind: "agent_handoff_failed", Code: "runtime_launch_failed",
			Message: "agent launch outcome is unknown", Ambiguous: true,
			Details: map[string]any{
				"workspace_id": "ws-1", "runtime_session_key": "runtime-1",
				"failed_stage": "message_delivered",
			},
		}
	}}
	s := newMCPTestServer(t, backend)
	result, err := connectMCPTestSession(t, s).CallTool(
		t.Context(), &mcp.CallToolParams{Name: "kenn_forge_list_repos"},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	type evidence struct {
		Kind      string         `json:"kind"`
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		Retryable bool           `json:"retryable"`
		Ambiguous bool           `json:"ambiguous"`
		Details   map[string]any `json:"details"`
	}
	metadata, found := result.Meta[toolErrorMetaKey]
	require.True(t, found)
	data, err := json.Marshal(metadata)
	require.NoError(t, err)
	var got evidence
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "agent_handoff_failed", got.Kind)
	assert.Equal(t, "runtime_launch_failed", got.Code)
	assert.Equal(t, "agent launch outcome is unknown", got.Message)
	assert.False(t, got.Retryable)
	assert.True(t, got.Ambiguous)
	assert.Equal(t, "ws-1", got.Details["workspace_id"])
	assert.Equal(t, "runtime-1", got.Details["runtime_session_key"])
	assert.Equal(t, "message_delivered", got.Details["failed_stage"])

	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	var payload struct {
		Error evidence `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))
	assert.Equal(t, got, payload.Error)
}

func TestSpawnToolFailurePreservesPartialHandoffEvidenceThroughClientSession(t *testing.T) {
	backend := successfulSpawnBackend("ws-1", "runtime-1", "coding-1")
	backend.launchWorkspaceRuntimeFn = func(context.Context, string, string) (RuntimeSession, error) {
		return RuntimeSession{}, &Error{
			Kind: "unavailable", Code: "runtimeLaunchFailed",
			Message: "agent runtime failed to launch", Retryable: true,
		}
	}
	s := newMCPTestServer(t, backend)

	result, err := connectMCPTestSession(t, s).CallTool(
		t.Context(), &mcp.CallToolParams{
			Name: "kenn_forge_spawn_workspace_with_agent",
			Arguments: map[string]any{
				"source": map[string]any{
					"type": "item",
					"item": map[string]any{
						"type": "pr", "provider": "github", "platform_host": "github.com",
						"platform_repo_id": "repo-acme-widget",
						"owner":            "acme", "name": "widget", "number": 42,
					},
				},
				"agent_target":    "codex",
				"initial_message": "review this",
				"timeout":         "2s",
			},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)

	metadata, found := result.Meta[toolErrorMetaKey]
	require.True(t, found)
	raw, err := json.Marshal(metadata)
	require.NoError(t, err)
	var evidence toolErrorEvidence
	require.NoError(t, json.Unmarshal(raw, &evidence))
	assert.Equal(t, "unavailable", evidence.Kind)
	assert.Equal(t, "runtimeLaunchFailed", evidence.Code)
	assert.Equal(t, "runtime_launched", evidence.Details["failed_stage"])
	assert.Equal(t, "workspace_ready", evidence.Details["last_completed_stage"])
	assert.Equal(t, "ws-1", evidence.Details["workspace_id"])
	assert.Equal(t, "ready", evidence.Details["workspace_status"])

	// The partial handoff output survives alongside the structured error so
	// clients can locate the workspace that was already created.
	structured, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var partial struct {
		Stage     string `json:"stage"`
		Workspace struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"workspace"`
	}
	require.NoError(t, json.Unmarshal(structured, &partial))
	assert.Equal(t, "workspace_ready", partial.Stage)
	assert.Equal(t, "ws-1", partial.Workspace.ID)
	assert.Equal(t, "ready", partial.Workspace.Status)
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
